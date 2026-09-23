# Warehouse replenishment

Go application with a Russian-language purchasing dashboard for Электрокомплект: import sales, stock, suppliers, and open shipments; calculate proposed purchase orders grouped by supplier; remove exceptional sales spikes from regular demand; export orders to CSV for Excel.

Live presentation: [3-minute demo script and scalability roadmap](DEMO.md). Use **Пример расчёта** for a reproducible real-SKU walkthrough in the existing detail window.

## Real Excel data (hackathon)

The existing dashboard now uses the supplied IEK and System Electric files. A new default warehouse is initialized with **3,907 products, 99,561 monthly records, 313 shipment lines and 24 seasonal factors**. Existing warehouse data is preserved: use **Данные Excel**, review the counts, and confirm replacement to switch an existing demo. No order is sent to a supplier.

```sh
python3 scripts/import_excel.py   # regenerate after editing the source workbooks; Python stdlib only
go run .
```

The converter reads all 12 workbooks and every sheet, including cached formula results and shared strings. It creates an embedded compressed snapshot, so the Go binary and Docker image need neither Python nor Excel at runtime. Rebuild/restart after regenerating, then reload using **Данные Excel**. With an explicit DATA_FILE, set SEED_EXCEL=1 to initialize an empty store automatically. Default planning for these historical exports is **2026-09-23**, with 365 history days.

See [the import audit](internal/realdata/import-report.json) for actual filenames, sheet names, headers, row counts and Excel errors. Join key is **supplier + exact trimmed text code 1С**, preserving leading zeroes and underscores. Supplier articles are display/export identifiers; duplicate articles never merge distinct 1С codes.

### Source mapping and documented fallbacks

- Monthly sales: «Номенклатура.Код», quantities by month, Jan 2024–Sep 2026. Monthly totals are authoritative; document dynamics are not added again.
- Monthly stock: «Номенклатура.Код». IEK explicitly labels balances as beginning-of-month. September balances are a **01.09 proxy**, not current inventory. System Electric products present in its transit workbook instead use «Остаток», «Зарезервировано» and «Свободный остаток» dated 22.09.
- Transit IEK: separate shipment columns with arrival dates parsed from their headers. System Electric: **«СЭ в пути 24.09»**, due 2026-09-24. The manual «Заказ» column is not an existing shipment.
- Seasonality: the supplied **«СЕЗОННОСТЬ» column L**, by supplier and calendar month. These supplier-wide coefficients are a proxy for SKU seasonality, not SKU-specific measured factors.
- MOQ IEK: «Мин. разр. к отгр.» is a minimum, **not a proven order multiple**; multiple defaults to 1. System Electric «Кратность» is the order multiple. Zero, missing and #N/A values fall back to 1 with a product warning. No pack sizes are extracted from product names.
- New order lead time is absent: **14 days** for each supplier, explicitly disclosed in every explanation. Review and safety days remain configurable in the existing UI.
- Blank cells in these monthly pivot tables mean zero; missing product stock rows remain unknown (provisional zero with a warning). Negative net monthly sales/stock are clipped to zero. Fractional sales remain fractional; available inventory and transit are conservatively floored to whole base units because the existing order model is integer-based.
- IEK current inventory cannot be reconstructed without receipts after 01.09. Products sold in meters but purchased in coils require unit confirmation; workbook quantities are retained in their recorded units.
- Source-date changes do not reconstruct historical inventory. Check the dated balance and assumptions before approving an order.

### Monthly replenishment calculation

Only completed months overlapping the selected history window are used; partial September 2026 is excluded. The window is expanded to whole overlapping months, not fabricated daily transactions.

1. Aggregate signed outgoing invoice lines by SKU/day from dynamics (exclude purchase receipts and order documents). With four positive days, detect spikes above max(3 × median, median + 3 × 1.4826 × MAD). Apply the exceptional fraction proportionally to the corresponding monthly total to reconcile differences between document and summary exports; never add the two sources.
2. Divide monthly demand by calendar days and its supplier seasonal coefficient. Apply the same robust median/MAD filter to deseasonalized monthly rates when at least four positive months exist.
3. A known zero opening inventory and demand at most 20% of the median normal in-stock rate indicates a **possible stockout**. Replace that rate with the median of normal in-stock months in the selected window. Unknown stock does not trigger compensation. Without normal months, demand is not invented. Monthly snapshots cannot establish exact outage days.
4. Compare medians of the latest three and preceding three corrected months, when six months exist; bound the trend to 0.75–1.25. Otherwise use 1.
5. Apply the future calendar-month seasonal factors day by day to the adjusted rate and trend. Forecast covers lead time + review; safety stock covers the additional safety days.
6. Subtract available stock and shipments arriving within the horizon. Overdue shipments are flagged and excluded. Raise positive need to MOQ and round to the order multiple.
7. Return the forecast, safety stock, adjustments, warnings and a Russian explanation for every SKU. CSV includes forecast, safety stock and explanation. The chart distributes monthly totals uniformly for display only; it is not observed daily history.

Daily JSON imports retain the original daily calculation below.

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
- Filter recommendations by supplier, name, SKU, or attention status. The arrow on each product opens its calculation, spike adjustments, and warnings. **Товары и остатки** shows all products; **Товары в пути** shows open/overdue shipments.
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

All API responses and imports use JSON except CSV exports. Dates use `YYYY-MM-DD`; quantities are nonnegative integers in each product's base unit (use a smaller unit for fractional goods). Application errors use `{"error":{"code":"...","message":"..."}}`; unmatched routes and unsupported methods use standard HTTP 404/405 responses.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Process liveness, public |
| GET | `/readyz` | Startup completed and stored snapshot validated, public |
| GET | `/api/v1/excel-dataset` | Reproducible dataset converted from the supplied Excel files |
| GET | `/api/v1/dataset` | Complete snapshot, revision, update time; revision also in `ETag` |
| PUT | `/api/v1/dataset` | Atomically replace the complete dataset; requires `If-Match` |
| POST | `/api/v1/recommendations` | Supplier orders and explanations for every product |
| POST | `/api/v1/recommendations.csv` | Same calculation exported as supplier order lines |

PUT accepts the dataset itself, as in `examples/dataset.json`, rather than the GET response wrapper. All five arrays are required; use `[]` for empty collections. A missing stock row is rejected rather than treated as an empty warehouse. Each product belongs to one supplier. Each product/date pair must have one aggregated sales row; duplicated daily rows, unknown references, negative quantities, invalid dates, and reserved quantities above stock are rejected. `pack_size` is required and positive; `min_order_quantity` defaults to zero. Import limit: 64 MiB. This is a full replacement, so fetch, edit, and submit the complete snapshot when changing one record.

POST accepts `{}` for defaults: today's UTC date, 90 history days, 14 review days, 7 safety days. Override with:

```json
{"as_of":"2026-09-08","lookback_days":7,"review_period_days":7,"safety_stock_days":2}
```

Lookback: 7–730 days; review: 1–365 days; safety: 0–365 days; supplier lead time: 0–365 days. Raw imported quantities and pack/minimum sizes are capped at 1 billion units per record. Returned order quantities can exceed that cap. The JSON response includes `dataset_revision`, `parameters`, history boundaries, `orders` (only positive quantities), and `products` (including products needing no order). Each line exposes raw/adjusted demand, stock, counted/overdue shipments, target stock, net requirement, final order quantity, adjustments, and warning codes. Responses also include `X-Dataset-Revision`.

## Calculation rules

1. Use complete calendar days in `[as_of - lookback_days, as_of)`. Missing sales days count as zero; today's partial sales and future sales are ignored.
2. Explicitly marked `exclude_from_demand: true` sales contribute zero to regular demand. They remain visible in raw demand and the adjustment audit.
3. With at least four positive, unexcluded days, calculate their median and median absolute deviation (MAD). A daily quantity strictly above `max(3 × median, median + 3 × 1.4826 × MAD)` is replaced with the median. This removes the exceptional portion while retaining normal daily demand. With fewer observations, retain sales and return `insufficient_positive_days_for_spike_detection`.
4. Divide adjusted sales by all lookback days, including zeros. Coverage is supplier lead time + review period + safety days. Target stock is `ceil(adjusted sales × coverage / lookback)`.
5. Available stock is `on_hand - reserved`. Count open shipments due between `as_of` and `as_of + coverage` inclusive. Overdue shipments are excluded and flagged; later shipments do not cover this order horizon.
6. Net requirement is `max(0, target - available - incoming)`. A positive requirement is raised to the product minimum and rounded up to a full pack. No minimum is applied when the requirement is zero.
7. Group positive lines by supplier. Proposed arrival is `as_of + lead_time_days`. `insufficient_supply_during_lead_time` warns when available stock plus incoming supply due by that date cannot cover average lead-time demand.

For legacy daily JSON imports, the operator must supply a complete sales window; missing records mean zero sales. Monthly Excel imports use the seasonal and stockout method documented above. New-product launch dates, price changes, weekday patterns, and promotions are not inferred. Repeated project sales can dominate the median; use manual exclusion when business context identifies them. The lead-time warning is an aggregate check and cannot identify every temporary shortage before individual shipments arrive.

Stock and shipments must describe the warehouse at `as_of`; selecting an earlier date does not reconstruct historical stock. Shipments represent only remaining, unreceived quantities. Upon receipt, update stock and remove/reduce that shipment in one snapshot import to avoid double counting. Recommendations are proposals; generating/exporting one neither sends it to a supplier nor records a new shipment.

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
