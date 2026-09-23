# Warehouse replenishment backend

Go backend for Электрокомплект: import sales, stock, suppliers, and open shipments; calculate proposed purchase orders grouped by supplier; remove exceptional sales spikes from regular demand; export orders to CSV for Excel.

## Run locally

Requires Go 1.23+; there are no external Go dependencies. Use a supported Go release for deployment.

```sh
go run .
```

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
| GET | `/api/v1/dataset` | Complete snapshot, revision, update time; revision also in `ETag` |
| PUT | `/api/v1/dataset` | Atomically replace the complete dataset; requires `If-Match` |
| POST | `/api/v1/recommendations` | Supplier orders and explanations for every product |
| POST | `/api/v1/recommendations.csv` | Same calculation exported as supplier order lines |

PUT accepts the dataset itself, as in `examples/dataset.json`, rather than the GET response wrapper. All five arrays are required; use `[]` for empty collections. A missing stock row is rejected rather than treated as an empty warehouse. Each product belongs to one supplier. Each product/date pair must have one aggregated sales row; duplicated daily rows, unknown references, negative quantities, invalid dates, and reserved quantities above stock are rejected. `pack_size` is required and positive; `min_order_quantity` defaults to zero. Import limit: 8 MiB. This is a full replacement, so fetch, edit, and submit the complete snapshot when changing one record.

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

These are transparent planning heuristics, not a seasonal forecast. The operator must supply a complete sales window; missing records mean zero sales, not unavailable history. Stockouts, new-product launch dates, price changes, weekday patterns, and promotions are not modeled. Repeated project sales can dominate the median; use manual exclusion when business context identifies them. The lead-time warning is an aggregate check and cannot identify every temporary shortage before individual shipments arrive.

Stock and shipments must describe the warehouse at `as_of`; selecting an earlier date does not reconstruct historical stock. Shipments represent only remaining, unreceived quantities. Upon receipt, update stock and remove/reduce that shipment in one snapshot import to avoid double counting. Recommendations are proposals; generating/exporting one neither sends it to a supplier nor records a new shipment.

## Configuration and deployment

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | Listen address |
| `DATA_FILE` | `data/warehouse.json` | Persistent snapshot path |
| `API_KEY` | empty | Bearer token; required for non-loopback binding, minimum 24 characters |
| `CORS_ORIGIN` | empty | One exact allowed browser origin, e.g. `http://localhost:3000` |

When `API_KEY` is set, send `Authorization: Bearer <API_KEY>` with every `/api/v1/` request. Keep this shared operator credential on trusted clients or a frontend server; there are no per-user roles. CORS exposes `ETag` and `X-Dataset-Revision` for browser clients.

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
