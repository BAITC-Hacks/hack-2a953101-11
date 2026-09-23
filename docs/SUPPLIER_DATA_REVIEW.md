# IEK / Systeme Electric source review

Reviewed every populated worksheet in all 11 workbooks under `IEK/` and `systemElectric/`, and the related `MOQ  ИЭК.xlsx` at the repository root: **12 files, 14 sheets**. Raw files are preserved. The generated audit records filenames, SHA-256 hashes, sheet row counts, cached formula errors, mapping issues, and each monthly reconciliation difference.

## File mappings

| File | Interpretation and changes |
| --- | --- |
| `IEK/Динамика продаж_2025-2026.xlsx` | 171,603 transaction rows, all labelled Алматы; 115 negative. Aggregate signed quantities by exact 1C code and calendar date. The filename does not describe the full contents: 104 records precede 2025 and are outside the selected complete history. |
| `IEK/Ежемесячные продажи в количественном выражении за последние 2 года.xlsx` | Monthly quantity pivot, January 2024–September 2026. Used for product mapping and reconciliation, never added to daily transaction demand. |
| `IEK/Ежемесячные остатки продукции за последние 2 года  ИЭК.xlsx` | Explicitly **нач. остаток**. Latest snapshot is 1 September 2026. Preserve fractional quantities (including 43.2 metres for code `130200015_`). Do not pretend this is a 22 September balance or subtract sales without knowing receipts/transfers. |
| `IEK/Путь ИЭК 22.09.2026.xlsx` | Exact 1C-to-supplier-article mapping and six shipment columns. Arrival dates are read from “поступление до”: 30 September, 1 October, 10 October, and 15 October. Distinct shipments are retained separately. Exact duplicate rows are excluded. |
| `IEK/Сезонность ИЭК.xlsx` | Supplied `СЕЗОННОСТЬ` profile in column L; auxiliary normalized table cross-describes the same profile. Factors are applied to demand, not interpreted as unit sales. |
| `MOQ  ИЭК.xlsx` | `Мин. разр. к отгр.` means minimum shipment quantity. Map it to minimum order quantity, without assuming it also defines an order multiple. Fifteen cached `#N/A` cells remain unresolved and block ordering. |
| `systemElectric/Динамика продаж_Syseme Electric_2025-2026.xlsx` | 77,312 transaction rows labelled Алматы; 302 negative. Aggregate signed daily values. Exclude 298 pre-2025 corrections from the selected complete history. |
| `systemElectric/Ежемесячные продажи в кол-м выражении SystemElectric 2024-2026.xlsx` | Monthly quantities and article mappings. The main sheet's zero multiples are not usable order constraints; the dedicated MOQ file takes precedence. The second sheet is repeated monetary seasonality reference data, not additional product sales. |
| `systemElectric/Ежемесячные остатки SystemElectric 2024-2026.xlsx` | Monthly opening-balance history. Use 1 September only as a historical fallback where a current row is unavailable. |
| `systemElectric/MOQ SystemElectric.xlsx` | `Кратность` is an order multiple. Map to `pack_size`; do not invent a separate minimum. |
| `systemElectric/Сезонность SystemElectric 2024-2026.xlsx` | Use the supplied 12 cached column-L factors. Do not add the 2026/2025 trend adjustment again: the workbook's Q4 values already incorporate a correction. |
| `systemElectric/Товар в пути_SystemElectric на 22.09.2026.xlsx` | `TDSheet`: current stock from AX, reservation from AY, verify AX − AY = AZ. Do not add the other warehouse columns to AX. BC contains seven positive incoming lines due 24 September 2026. A second sheet repeats seasonal revenue reference data and is read but not double-counted. |

## Prepared dataset

| Metric | Count |
| --- | ---: |
| Products | 3,909 (3,185 IEK; 724 Systeme Electric) |
| Nonzero daily net sales | 140,922 |
| Incoming shipment lines | 313 |
| Systeme Electric stock rows dated 22 September | 497 |
| Historical opening-stock rows | 3,080 |
| Products without a stock row in either source | 332 |
| Products without a valid matched MOQ/multiple | 1,433 |
| Products without a matched supplier article | 680 |
| Products requiring reel/metre conversion confirmation | 6 |
| Monthly quantity discrepancies | 13,891 (8,929 IEK; 4,962 Systeme Electric) |

Issue categories overlap. Records are not silently dropped because a lookup failed. IDs are namespaced supplier + exact 1C code; supplier articles and units are separate fields. The same article may appear under different 1C codes and is not used as a unique inventory key.

The transaction histories cover complete reporting from 1 January 2025 through 22 September 2026. Source boundaries prevent treating unavailable earlier/later periods as zero demand. September monthly totals are partial; the importer does not spread them over invented daily sales. Net negative days remain visible in raw history but contribute zero to demand, with an explicit adjustment. Returns within a day offset positive quantities before aggregation.

## Assumptions requiring confirmation

- **New-order lead times are not specified.** Existing shipment arrival dates cannot establish them. Generated suppliers remain `lead_time_unconfirmed`; the dashboard accepts explicit confirmed values.
- **Current IEK stock is absent.** Historical opening balances cannot establish current stock without receipts, transfers, and reservations. Those lines remain blocked from orders/CSV until refreshed.
- **Warehouse scopes may differ.** Transaction rows explicitly name Алматы; the monthly pivots do not state an equivalent scope. The Systeme Electric current-stock sheet includes several warehouse columns. This may explain discrepancies, but the files do not establish the cause. Use dated transactions for demand, retain the differences in the audit, and confirm scope with the data owner.
- **Seasonal factors are supplier-level monetary profiles.** Applying them to SKU unit demand is a planning assumption, not a statistically validated per-product forecast. The provided 2026 September values are partial and Q4 factors are forecast values. Their provenance is shown in the UI; no additional trend multiplier is invented.
- **Reels and metres need a conversion.** Six IEK mapping rows explicitly say goods are purchased as reels but recorded in metres. Unconverted incoming quantities for such rows are excluded, and purchasing remains blocked until base-unit order constraints and incoming quantities are confirmed.
- **Missing MOQ, article, or unit information is actionable data quality.** Provisional quantities may be inspected, but blocked products have zero executable order quantity and never enter CSV orders.

For this supplied snapshot, use a planning date of **23 September 2026** to include all complete transaction days through the 22nd. The application accepts a stock snapshot dated that planning day or its immediately preceding day; other supplied dates require review. Monthly opening balances do not establish day-by-day stock availability, so the app does not infer stockout-adjusted demand from them.

## Reproduction

Run `python3 scripts/import_supplier_workbooks.py` from the repository root. This writes the import and full audit under `data/`, without modifying `data/warehouse.json`. Load the generated import through the dashboard preview or the existing revision-checked API. See the README for validation commands and lead-time configuration.
