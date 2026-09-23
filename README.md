# Warehouse replenishment

Go application with a Russian-language purchasing dashboard for Электрокомплект: import sales, stock, suppliers, and open shipments; calculate proposed purchase orders grouped by supplier; remove exceptional sales spikes from regular demand; export orders to CSV for Excel.

## Run locally

Requires Go 1.23+; there are no external Go dependencies. Use a supported Go release for deployment.

```sh
go run .
```

Open **http://127.0.0.1:8080** for the dashboard. The HTML, CSS, and JavaScript are embedded in the Go binary; no frontend build or Node server is needed. The same UI is included in the Docker image.

### Dashboard workflow

- On an empty warehouse, choose **Попробовать демо** and confirm to load a six-product demo with 30 days of sales and three spikes. The demo is stored only after confirmation; it is never loaded automatically.
- Choose **Загрузить данные** to import a JSON dataset. Review the record counts and confirm replacement. Existing data can be downloaded as a backup before replacement. Imports also accept the GET dataset response wrapper.
- Adjust the planning date, sales window, review period, and safety days under **Параметры расчёта**. Stock must describe the warehouse on the selected date.
- Filter recommendations by supplier, name, supplier SKU, 1C code, or attention status. Catalogues are paginated at 100 products per page. The arrow on each product opens its calculation, spike adjustments, and warnings. **Товары и остатки** shows all products; **Товары в пути** shows open/overdue shipments.
- **Экспорт CSV** downloads all current recommended order lines, regardless of table filters, with a UTF-8 BOM for Excel. An export is rejected if the warehouse changed since the displayed calculation.
- If `API_KEY` is configured, the page remains public but warehouse data requires the key. Use **Подключение** to enter it; the key stays in tab memory, never local/session storage, and must be re-entered after reload.

Charts sum quantities across product base units for an overview; procurement calculations still run separately for each product. The dashboard is responsive, supports keyboard navigation and reduced motion, and uses local assets without external fonts or scripts.

The API listens on `http://127.0.0.1:8080` and persists data to `data/warehouse.json`. In a separate terminal, load the example into a **new** instance (revision 0):

```sh
curl -i http://127.0.0.1:8080/api/v1/dataset
curl --fail-with-body -X PUT http://127.0.0.1:8080/api/v1/dataset \
  -H 'Content-Type: application/json' -H 'If-Match: "0"' \
  --data-binary @examples/dataset.json
curl --fail-with-body http://127.0.0.1:8080/api/v1/recommendations \
  -H 'Content-Type: application/json' \
  --data-binary @examples/recommendation.json
curl --fail-with-body http://127.0.0.1:8080/api/v1/recommendations.csv \
  -H 'Content-Type: application/json' \
  --data-binary @examples/recommendation.json -o /tmp/supplier-orders.csv
```

Expected demo: cable demand drops from approximately 88.57 to 20 units/day after adjusting the 500-unit sale to the 20-unit baseline. Order **220 cable units** and **100 lamps**; breakers have enough stock and generate no order. Re-imports require the current `ETag` from GET; stale writes return 412.

## API

All API responses and imports use JSON except CSV exports. Dates use `YYYY-MM-DD`; quantities are numeric values in each product's base unit, including fractional metres; sales may be signed net quantities to preserve returns/corrections. Application errors use `{"error":{"code":"...","message":"..."}}`; unmatched routes and unsupported methods use standard HTTP 404/405 responses.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Process liveness, public |
| GET | `/readyz` | Startup completed and stored snapshot validated, public |
| GET | `/api/v1/dataset` | Complete snapshot, revision, update time; revision also in `ETag` |
| PUT | `/api/v1/dataset` | Atomically replace the complete dataset; requires `If-Match` |
| POST | `/api/v1/recommendations` | Supplier orders and explanations for every product |
| POST | `/api/v1/recommendations.csv` | Same calculation exported as supplier order lines |

PUT accepts the dataset itself, as in `examples/dataset.json`, rather than the GET response wrapper. All five arrays are required; use `[]` for empty collections. A missing stock row is rejected rather than treated as an empty warehouse. Each product belongs to one supplier. Each product/date pair must have one aggregated sales row; duplicated daily rows, unknown references, negative stock/shipments, invalid dates, and reserved quantities above stock are rejected. `pack_size` is required and positive; `min_order_quantity` defaults to zero. Import limit: 64 MiB. This is a full replacement, so fetch, edit, and submit the complete snapshot when changing one record.

POST accepts `{}` for defaults: today's UTC date, 90 history days, 14 review days, 7 safety days. Override with:

```json
{"as_of":"2026-09-08","lookback_days":7,"review_period_days":7,"safety_stock_days":2}
```

Lookback: 7–730 days; review: 1–365 days; safety: 0–365 days; supplier lead time: 0–365 days. Raw imported quantities and pack/minimum sizes are capped at 1 billion units per record. Returned order quantities can exceed that cap. The JSON response includes `dataset_revision`, `parameters`, history boundaries, `orders` (only positive quantities), and `products` (including products needing no order). Each line exposes raw/adjusted demand, stock, counted/overdue shipments, target stock, net requirement, final order quantity, adjustments, and warning codes. Responses also include `X-Dataset-Revision`.

## Calculation rules

1. Use complete calendar days in `[as_of - lookback_days, as_of)`, intersected with `source.history_start`/`source.history_end` when supplied. Missing sales days **within that known coverage** count as zero; unknown days outside it are excluded from the denominator. Today's partial sales and future sales are ignored. Negative daily net sales are retained in raw demand but contribute zero to replenishment demand, with an explicit `net_returns` adjustment.
2. Explicitly marked `exclude_from_demand: true` sales contribute zero to regular demand. They remain visible in raw demand and the adjustment audit.
3. With at least four positive, unexcluded days, calculate their median and median absolute deviation (MAD). A daily quantity strictly above `max(3 × median, median + 3 × 1.4826 × MAD)` is replaced with the median. This removes the exceptional portion while retaining normal daily demand. With fewer observations, retain sales and return `insufficient_positive_days_for_spike_detection`.
4. Divide adjusted sales by observed calendar days, including zeros. Coverage is supplier lead time + review period + safety days. With a supplied 12-month supplier profile, de-seasonalize each adjusted historical day, then apply each future day's monthly factor across the coverage horizon. `daily_demand` remains historical regular demand; `forecast_daily_demand` and `seasonal_factor` explain the projected demand. Target stock is the ceiling of projected coverage demand. Without a profile, the original average-demand calculation applies.
5. Available stock is `on_hand - reserved`. Count open shipments due between `as_of` and `as_of + coverage` inclusive. Overdue shipments are excluded and flagged; later shipments do not cover this order horizon.
6. Net requirement is `max(0, target - available - incoming)`. A positive requirement is raised to the product minimum and rounded up to a full pack. No minimum is applied when the requirement is zero.
7. Stock older than the day before `as_of` (or dated in the future), explicitly unverified stock, unconfirmed lead times, missing history, and product `review_reasons` block export. A blocked line exposes a provisional `suggested_quantity` but has `order_quantity: 0`. Group only positive, unblocked lines by supplier. Proposed arrival is `as_of + lead_time_days`. `insufficient_supply_during_lead_time` warns when available stock plus incoming supply due by that date cannot cover forecast average lead-time demand.

These are transparent planning heuristics with optional supplier-level monthly seasonality. Complete source-history boundaries must be supplied accurately. Daily stockouts, new-product launch dates, price changes, weekday patterns, and promotions are not modeled; monthly opening balances cannot establish exact days of availability. Repeated project sales can dominate the median; use manual exclusion when business context identifies them. The lead-time warning is an aggregate check and cannot identify every temporary shortage before individual shipments arrive.

Stock and shipments must describe the warehouse at `as_of`; selecting an earlier date does not reconstruct historical stock. Shipments represent only remaining, unreceived quantities. Upon receipt, update stock and remove/reduce that shipment in one snapshot import to avoid double counting. Recommendations are proposals; generating/exporting one neither sends it to a supplier nor records a new shipment.


## Supplied IEK and Systeme Electric workbooks

All 12 source workbooks (14 sheets), including the IEK MOQ workbook at the repository root, were reviewed. See [the source review](docs/SUPPLIER_DATA_REVIEW.md) for mappings, discrepancies, and known limitations.

Prepare an import and detailed audit without modifying the live warehouse:

```sh
python3 scripts/import_supplier_workbooks.py
```

This writes `data/supplier-import.json` (about 15 MB) and `data/supplier-import-report.json`. In the dashboard choose **Загрузить данные**, select `supplier-import.json`, review the preview, and confirm. The planning date becomes 23 September 2026, following the supplied snapshot/history date of 22 September. The data-quality banner and **Требуют внимания** view show blocked items; these are not exported as orders.

Standard lead times are absent from the workbooks. Enter confirmed values in **Параметры расчёта**; changes persist with the same revision checks as imports. Alternatively, pass explicitly confirmed values with `--iek-lead-days` and `--systeme-lead-days` when running the converter. They default to **unconfirmed**, not an assumed same-day delivery.

IEK September stock is an opening balance dated **1 September**, so it cannot authorize orders on 23 September. Update the dataset with an actual current-stock export (`on_hand`, `reserved`, `as_of`) before clearing that block. For products with unresolved `review_reasons`, correct the article/order rules/unit conversion against the source and remove the resolved reasons in the JSON before importing. `pack_size` and `min_order_quantity` are expressed in the same base unit as stock and demand; do not combine reel counts with metres without a confirmed conversion.

The converter uses the Python standard library, streams worksheet XML, reads cached formula values without executing formulas, preserves leading zeros in 1C codes, and checks workbook snapshot dates. It reconciles monthly totals but does **not** add them to transaction totals. Full source documents and generated warehouse data are excluded from Docker builds. No workbook or existing live snapshot is rewritten.

```sh
python3 -m unittest discover -s scripts -p 'test_*.py'
SUPPLIER_IMPORT_FILE="$PWD/data/supplier-import.json" go test ./internal/planning -run TestActualSupplierImport -v
# Include the actual import in isolated browser tests:
SUPPLIER_IMPORT_FILE="$PWD/data/supplier-import.json" CHROME_BIN=/path/to/chrome npm run test:browser
```

The extended JSON model adds `supplier.seasonality` (12 positive monthly factors), `lead_time_unconfirmed`, product `internal_code`, `unit`, and `review_reasons`, stock `as_of`/`unverified`, and a `source` object with label, snapshot date, history boundaries, and audit warnings. Older datasets without these optional fields retain their original behavior. CSV export appends 1C code, unit, forecast demand, and seasonal factor.

## Configuration and deployment

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | Listen address |
| `DATA_FILE` | `data/warehouse.json` | Persistent snapshot path |
| `API_KEY` | empty | Bearer token; required for non-loopback binding, minimum 24 characters |
| `CORS_ORIGIN` | empty | One exact allowed browser origin, e.g. `http://localhost:3000` |

When `API_KEY` is set, send `Authorization: Bearer <API_KEY>` with every `/api/v1/` request. Share this operator credential only with trusted operators; there are no per-user roles. The bundled dashboard adds the header after the operator enters the key. CORS exposes `ETag` and `X-Dataset-Revision` for separately hosted browser clients; the bundled UI uses the same origin.

```sh
export API_KEY="$(openssl rand -hex 32)"
docker compose up --build -d
curl -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8080/api/v1/dataset
docker compose logs -f backend
```

The container runs as a non-root user and stores data in a named volume. Put a TLS reverse proxy in front of any public deployment. The build uses the [Go 1.26 release line](https://go.dev/doc/go1.26) and an [Alpine 3.23 runtime](https://alpinelinux.org/releases/).

Storage is an atomic JSON snapshot with an in-process lock, optimistic revision checks, file sync, and rename before publishing the new in-memory state. Failed writes leave the previous in-memory snapshot intact. Corrupt stored data fails startup. Run **one process/replica per data file**; this storage is intended for a small warehouse/hackathon deployment. It has no cross-process locking, database query layer, audit history, or replication, and rename metadata is not directory-synced against abrupt power failure. Back up the snapshot; use a transactional database before horizontal scaling or larger imports.

The service has structured JSON logs, bounded request bodies, HTTP timeouts, graceful SIGTERM shutdown, constant-time credential comparison, spreadsheet formula escaping, and exact-origin CORS. `/readyz` reflects startup readiness, not a continuous disk-writability check.

## Verification

```sh
go test -race ./...
go vet ./...
go build -o bin/backend .
# Optional, with a golangci-lint version compatible with your Go toolchain:
golangci-lint run
```

Tests cover spike filtering, sparse/zero demand, explicit exclusions, date boundaries, incoming/overdue stock, reservation and pack rounding, supplier grouping, validation, cancellation, persistence/restart, concurrent revision conflicts, failed writes, API authentication/import/export, request limits, CORS, and spreadsheet injection. No external services are needed.

Browser tests are optional development tooling (Node 20+). They start a separate backend with a temporary data file, exercise the real UI/API, and leave existing warehouse data untouched:

```sh
go build -o bin/backend .
npm ci
npx playwright install chromium
npm run test:syntax
npm run test:browser
# Or use an existing Chrome installation:
CHROME_BIN=/path/to/chrome npm run test:browser
```

Set `UI_SCREENSHOT_DIR=/tmp/warehouse-screenshots` to capture desktop and mobile screenshots during the browser tests. The tests cover protected login, empty state, explicit demo import, demand results, search/supplier filters, product explanations, planning parameters, CSV downloads, inventory/shipments, mobile overflow, import revision conflicts, and HTML escaping. Go tests also verify public static assets, security headers, and API authentication boundaries.
