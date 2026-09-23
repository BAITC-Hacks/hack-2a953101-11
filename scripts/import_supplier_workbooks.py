#!/usr/bin/env python3
"""Convert the supplied IEK/Systeme Electric XLSX exports without changing live data.

Uses cached cell values, never executes Excel formulas/macros, and writes an audit
alongside the import. Run with --help for explicit planning assumptions.
"""
import argparse
from collections import Counter, defaultdict
from datetime import datetime, date
from decimal import Decimal, InvalidOperation
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys
import xml.etree.ElementTree as ET
from zipfile import ZipFile

NS = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
REL = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id"
MONTHS = {"янв": 1, "фев": 2, "мар": 3, "апр": 4, "май": 5, "июн": 6,
          "июл": 7, "авг": 8, "сен": 9, "окт": 10, "ноя": 11, "дек": 12}


def numeric(value):
    try:
        number = Decimal(str(value or "0").replace("\xa0", "").replace(" ", "").replace(",", "."))
    except InvalidOperation as exc:
        raise ValueError(f"Not a quantity: {value!r}") from exc
    if not number.is_finite():
        raise ValueError(f"Not a finite quantity: {value!r}")
    return number


def json_number(value):
    return int(value) if value == int(value) else float(value)


def month_key(value):
    match = re.search(r"(\d{4})", value)
    month = MONTHS.get(value.lower()[:3])
    return f"{match[1]}-{month:02d}" if match and month else None


class Workbook:
    def __init__(self, path, report):
        self.path = path
        self.zip = ZipFile(path)
        if sum(i.file_size for i in self.zip.infolist()) > 512 * 1024 * 1024:
            self.zip.close()
            raise ValueError(f"Workbook exceeds 512 MiB expanded: {path}")
        self.audit = {"file": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "sheets": []}
        report["files"].append(self.audit)
        self.strings = []
        if "xl/sharedStrings.xml" in self.zip.namelist():
            with self.zip.open("xl/sharedStrings.xml") as stream:
                for _, element in ET.iterparse(stream, events=("end",)):
                    if element.tag == NS + "si":
                        self.strings.append("".join(t.text or "" for t in element.iter(NS + "t")))
                        element.clear()
        relationships = {r.attrib["Id"]: r.attrib["Target"] for r in ET.fromstring(self.zip.read("xl/_rels/workbook.xml.rels"))}
        self.sheets = []
        for sheet in ET.fromstring(self.zip.read("xl/workbook.xml")).find(NS + "sheets"):
            target = relationships[sheet.attrib[REL]]
            target = target.lstrip("/") if target.startswith("/") else "xl/" + target
            if ".." in PurePosixPath(target).parts:
                raise ValueError("Unsupported workbook relationship")
            self.sheets.append((sheet.attrib["name"], target))

    def rows(self, index):
        name, target = self.sheets[index]
        audit = {"name": name, "nonempty_rows": 0, "formula_errors": 0, "uncached_formulas": 0}
        self.audit["sheets"].append(audit)
        with self.zip.open(target) as stream:
            for _, element in ET.iterparse(stream, events=("end",)):
                if element.tag != NS + "row":
                    continue
                cells = {}
                for cell in element:
                    raw = cell.find(NS + "v")
                    value = raw.text or "" if raw is not None else ""
                    kind = cell.get("t")
                    if kind == "s" and value:
                        value = self.strings[int(value)]
                    elif kind == "inlineStr":
                        value = "".join(t.text or "" for t in cell.iter(NS + "t"))
                    if kind == "e":
                        audit["formula_errors"] += 1
                    if cell.find(NS + "f") is not None and raw is None:
                        audit["uncached_formulas"] += 1
                    if value:
                        cells[re.sub(r"\d", "", cell.attrib["r"])] = value.strip()
                element.clear()
                if cells:
                    audit["nonempty_rows"] += 1
                    yield cells

    def close(self):
        self.zip.close()


class Importer:
    def __init__(self, as_of, history_start):
        self.as_of, self.history_start = as_of, history_start
        self.products, self.stock, self.suppliers = {}, {}, []
        self.sales = defaultdict(Decimal)
        self.monthly, self.monthly_daily = {}, defaultdict(Decimal)
        self.shipments = []
        self.report = {"files": [], "notes": [], "issues": [], "summary": {}}
        self.article_sources = {}
        self.order_rules = set()

    def issue(self, product, reason, detail):
        if reason not in product["review_reasons"]:
            product["review_reasons"].append(reason)
            self.report["issues"].append({"product_id": product["id"], "code": reason, "detail": detail})

    def product(self, supplier, code, name="", unit=""):
        identifier = f"{supplier}:{code.strip()}"
        if identifier not in self.products:
            self.products[identifier] = {"id": identifier, "internal_code": code.strip(), "sku": code.strip(), "name": name or code.strip(),
                "supplier_id": supplier, "unit": unit, "pack_size": 1, "min_order_quantity": 0, "review_reasons": []}
        p = self.products[identifier]
        if name and p["name"] == p["internal_code"]:
            p["name"] = name
        if unit:
            if p["unit"] and p["unit"] != unit:
                self.issue(p, "unit_conflict", f"Conflicting base units: {p['unit']} and {unit}")
            else:
                p["unit"] = unit
        return p

    def article(self, product, value):
        if not value or value.startswith("#"):
            return
        previous = self.article_sources.get(product["id"])
        if previous and previous != value:
            self.issue(product, "supplier_article_conflict", f"Supplier articles disagree: {previous} / {value}")
        else:
            self.article_sources[product["id"]] = value
            product["sku"] = value

    def consume(self, path, reader):
        workbook = Workbook(path, self.report)
        try:
            reader(workbook.rows(0))
            # Embedded seasonal worksheets repeat reference data, not product sales.
            # Read every sheet and record it without adding its values to sales again.
            for index in range(1, len(workbook.sheets)):
                rows = list(workbook.rows(index))
                self.report["notes"].append(f"Reviewed auxiliary sheet {path.name}/{workbook.sheets[index][0]} ({len(rows)} rows); not added as product sales.")
        finally:
            workbook.close()

    def stock_months(self, supplier, rows):
        header = next(rows)
        code_col = next(k for k, v in header.items() if v == "Номенклатура.Код")
        name_col = next(k for k, v in header.items() if v == "Номенклатура")
        unit_col = next(k for k, v in header.items() if v in ("Ед.", "Ед.изм"))
        months = {k: month_key(v) for k, v in header.items() if month_key(v)}
        eligible = [col for col, month in months.items() if month + "-01" <= self.as_of]
        if not eligible:
            raise ValueError("No stock month at or before the snapshot date")
        latest_col = max(eligible, key=lambda c: months[c])
        stamp = months[latest_col] + "-01"
        for row in rows:
            if not row.get(code_col):
                continue
            p = self.product(supplier, row[code_col], row.get(name_col, ""), row.get(unit_col, ""))
            amount = numeric(row.get(latest_col))
            if amount < 0:
                self.issue(p, "negative_stock", f"Negative opening stock {amount}")
            self.stock[p["id"]] = {"product_id": p["id"], "on_hand": json_number(max(0, amount)), "reserved": 0, "as_of": stamp}
        self.report["notes"].append(f"{supplier}: monthly stocks are opening balances; latest date {stamp}, not {self.as_of}.")

    def monthly_sales(self, supplier, rows):
        header = next(rows)
        code_col = next(k for k, v in header.items() if v == "Номенклатура.Код")
        name_col = next(k for k, v in header.items() if v == "Номенклатура")
        article_col = next((k for k, v in header.items() if v == "Артикул"), None)
        months = {k: month_key(v) for k, v in header.items() if month_key(v)}
        for row in rows:
            if not row.get(code_col):
                continue
            p = self.product(supplier, row[code_col], row.get(name_col, ""))
            self.article(p, row.get(article_col))
            for col, month in months.items():
                self.monthly[(p["id"], month)] = numeric(row.get(col))

    def transactions(self, supplier, rows):
        next(rows)
        counts = Counter()
        dates = []
        for row in rows:
            if not row.get("D") or row.get("A") == "Итого":
                continue
            day = datetime.strptime(row["A"], "%d.%m.%Y %H:%M:%S").date().isoformat()
            quantity = numeric(row.get("H"))
            p = self.product(supplier, row["D"], row.get("E", ""), row.get("F", ""))
            dates.append(day)
            counts["rows"] += 1
            counts["negative_rows"] += quantity < 0
            if not self.history_start <= day <= self.as_of:
                counts["outside_history"] += 1
                continue
            if row.get("G") != "Алматы":
                raise ValueError(f"Unexpected warehouse: {row.get('G')}")
            self.sales[(p["id"], day)] += quantity
            self.monthly_daily[(p["id"], day[:7])] += quantity
        self.report["summary"][supplier + "_transactions"] = {**counts, "first_date": min(dates), "last_date": max(dates)}

    def moq(self, supplier, rows):
        next(rows)
        seen = {}
        for row in rows:
            code_col, article_col, name_col = ("B", "C", "D") if supplier == "iek" else ("C", "D", "B")
            if not row.get(code_col):
                continue
            p = self.product(supplier, row[code_col], row.get(name_col, ""))
            self.article(p, row.get(article_col))
            try:
                amount = numeric(row.get("E"))
            except ValueError:
                self.issue(p, "missing_order_rules", f"MOQ cell is {row.get('E')!r}")
                continue
            if amount <= 0:
                self.issue(p, "missing_order_rules", "MOQ/multiple must be positive")
                continue
            if p["id"] in seen and seen[p["id"]] != amount:
                self.issue(p, "conflicting_order_rules", "Different MOQ values for one 1C code")
            seen[p["id"]] = amount
            p["min_order_quantity" if supplier == "iek" else "pack_size"] = json_number(amount)
            self.order_rules.add(p["id"])

    def seasonality(self, supplier, rows):
        factors = {}
        for row in rows:
            month = MONTHS.get(row.get("B", "").lower()[:3])
            if month and "L" in row and row.get("A") != "год":
                factors[month] = float(numeric(row["L"]))
        if len(factors) != 12 or any(v <= 0 for v in factors.values()):
            raise ValueError(f"Missing seasonal profile for {supplier}")
        next(s for s in self.suppliers if s["id"] == supplier)["seasonality"] = [factors[m] for m in range(1, 13)]
        self.report["notes"].append(f"{supplier}: use the supplied cached СЕЗОННОСТЬ column L, not revenue as unit demand; 2026 contains a partial September and forecast Q4.")

    def iek_shipments(self, rows):
        header = next(rows)
        arrivals = {}
        for col, value in header.items():
            match = re.search(r"поступление до (\d{2}\.\d{2}\.\d{4})", value)
            if match:
                arrivals[col] = datetime.strptime(match[1], "%d.%m.%Y").date().isoformat()
        seen = set()
        for row_number, row in enumerate(rows, 2):
            if not row.get("A"):
                continue
            p = self.product("iek", row["A"], row.get("C", ""))
            self.article(p, row.get("B"))
            conversion = "ЗАКУПАЮТСЯ БУХТАМИ" in row.get("C", "").upper()
            if conversion:
                self.issue(p, "purchase_unit_conversion_unconfirmed", "Purchased as reels, stocked as metres: confirm conversion before ordering; unconverted incoming quantities excluded")
            fingerprint = tuple(sorted(row.items()))
            if fingerprint in seen:
                self.report["notes"].append(f"IEK incoming duplicate row {row_number} excluded.")
                continue
            seen.add(fingerprint)
            for col, expected in arrivals.items():
                quantity = numeric(row.get(col))
                if quantity > 0 and not conversion:
                    self.shipments.append({"id": f"iek:{row_number}:{col}", "product_id": p["id"], "quantity": json_number(quantity), "expected_date": expected})

    def systeme_shipments(self, rows):
        header = next(row for row in rows if row.get("C") == "Код 1с")
        match = re.search(r"(\d{2}\.\d{2})", header["BC"])
        if not match:
            raise ValueError("Systeme Electric shipment header has no arrival date")
        expected = datetime.strptime(match[1] + "." + self.as_of[:4], "%d.%m.%Y").date().isoformat()
        for index, row in enumerate(rows, 3):
            if not row.get("C"):
                continue
            p = self.product("systeme", row["C"], row.get("D", ""))
            self.article(p, row.get("B"))
            on_hand, reserved = numeric(row.get("AX")), numeric(row.get("AY"))
            if on_hand < 0 or reserved < 0 or reserved > on_hand:
                self.issue(p, "invalid_current_stock", f"Stock={on_hand}, reserved={reserved}; kept historical balance")
            else:
                self.stock[p["id"]] = {"product_id": p["id"], "on_hand": json_number(on_hand), "reserved": json_number(reserved), "as_of": self.as_of}
                if numeric(row.get("AZ")) != on_hand - reserved:
                    self.issue(p, "stock_reservation_mismatch", "AX - AY differs from AZ")
            quantity = numeric(row.get("BC"))
            if quantity > 0:
                self.shipments.append({"id": f"systeme:{index}:BC", "product_id": p["id"], "quantity": json_number(quantity), "expected_date": expected})

    def finish(self):
        for p in self.products.values():
            if p["id"] not in self.order_rules:
                self.issue(p, "missing_order_rules", "No matching valid MOQ/multiple in supplied workbook")
            if p["id"] not in self.article_sources:
                self.issue(p, "supplier_article_missing", "No supplier article matched to the 1C code")
            if p["id"] not in self.stock:
                self.stock[p["id"]] = {"product_id": p["id"], "on_hand": 0, "reserved": 0, "unverified": True}
        mismatches = []
        for (product, month), expected in self.monthly.items():
            if self.history_start[:7] <= month <= self.as_of[:7]:
                actual = self.monthly_daily.get((product, month), Decimal(0))
                if expected != actual:
                    mismatches.append({"product_id": product, "month": month, "monthly_export": json_number(expected), "transaction_net": json_number(actual)})
        self.report["monthly_reconciliation"] = mismatches
        self.report["notes"].append("Monthly sales are used for reconciliation only; daily transaction quantities are the demand source. They are never summed together. Signed returns remain in daily net quantities.")
        self.report["notes"].append("IEK Мин. разр. к отгр. is a minimum, not assumed to be a multiple. Systeme Кратность is an order multiple. Supplier article mappings use exact 1C codes, not product-name guesses.")
        warnings = ["IEK: остатки на начало сентября; нужен актуальный снимок.", "Сезонность взята из предоставленных таблиц: сентябрь 2026 неполный, IV квартал прогнозный."]
        if mismatches:
            warnings.append(f"Расхождения месячных отчётов и операций: {len(mismatches)} пар товар/месяц. Для спроса используются операции; детали в отчёте импорта.")
        if any(s["lead_time_unconfirmed"] for s in self.suppliers):
            warnings.append("Подтвердите сроки новых заказов у поставщиков. Даты товаров в пути не задают срок нового заказа.")
        data = {"suppliers": self.suppliers, "products": sorted(self.products.values(), key=lambda p: p["id"]),
            "sales": [{"product_id": p, "date": day, "quantity": json_number(q), "exclude_from_demand": False} for (p, day), q in sorted(self.sales.items()) if q != 0],
            "stock": sorted(self.stock.values(), key=lambda s: s["product_id"]), "shipments": self.shipments,
            "source": {"label": "IEK / Systeme Electric · Алматы", "as_of": self.as_of, "history_start": self.history_start, "history_end": self.as_of, "warnings": warnings}}
        self.report["summary"].update({"products": len(data["products"]), "daily_sales": len(data["sales"]), "shipments": len(data["shipments"]), "issues": dict(Counter(i["code"] for i in self.report["issues"])), "reconciliation_mismatches": len(mismatches)})
        return data


def one(folder, pattern):
    paths = list(folder.glob(pattern))
    if len(paths) != 1:
        raise ValueError(f"Expected exactly one {folder}/{pattern}, found {len(paths)}")
    return paths[0]


def convert(source, as_of, history_start, lead_times):
    importer = Importer(as_of, history_start)
    for supplier, folder, name in [("iek", source / "IEK", "IEK"), ("systeme", source / "systemElectric", "Systeme Electric")]:
        lead = lead_times.get(supplier)
        if lead is not None and not 0 <= lead <= 365:
            raise ValueError("Lead time must be 0..365")
        importer.suppliers.append({"id": supplier, "name": name, "lead_time_days": lead or 0, "lead_time_unconfirmed": lead is None})
        for pattern, reader in [("Ежемесячные остатки*.xlsx", importer.stock_months), ("Ежемесячные продажи*.xlsx", importer.monthly_sales), ("Динамика*.xlsx", importer.transactions), ("Сезонность*.xlsx", importer.seasonality)]:
            importer.consume(one(folder, pattern), lambda rows, reader=reader: reader(supplier, rows))
        moq_folder = folder if list(folder.glob("MOQ*.xlsx")) else source
        importer.consume(one(moq_folder, "MOQ*.xlsx"), lambda rows: importer.moq(supplier, rows))
        incoming = one(folder, "Путь*.xlsx" if supplier == "iek" else "Товар в пути*.xlsx")
        file_date = re.search(r"\d{2}\.\d{2}\.\d{4}", incoming.name)
        if not file_date or datetime.strptime(file_date[0], "%d.%m.%Y").date().isoformat() != as_of:
            raise ValueError(f"Snapshot date must agree with the incoming workbook filename: {incoming.name}")
        importer.consume(incoming, importer.iek_shipments if supplier == "iek" else importer.systeme_shipments)
    return importer.finish(), importer.report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path("."))
    parser.add_argument("--as-of", default="2026-09-22", help="Date of supplied current-stock/incoming exports")
    parser.add_argument("--history-start", default="2025-01-01", help="Start of the complete transaction reporting period")
    parser.add_argument("--iek-lead-days", type=int)
    parser.add_argument("--systeme-lead-days", type=int)
    parser.add_argument("--output", type=Path, default=Path("data/supplier-import.json"))
    parser.add_argument("--report", type=Path, default=Path("data/supplier-import-report.json"))
    args = parser.parse_args()
    date.fromisoformat(args.as_of); date.fromisoformat(args.history_start)
    if args.history_start > args.as_of:
        parser.error("history-start must not be after as-of")
    data, report = convert(args.source, args.as_of, args.history_start, {"iek": args.iek_lead_days, "systeme": args.systeme_lead_days})
    for path, value in [(args.output, data), (args.report, report)]:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n", encoding="utf-8")
    print(json.dumps(report["summary"], ensure_ascii=False, indent=2))
    print(f"Dataset: {args.output}\nAudit: {args.report}\nLive warehouse data was not modified.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, ET.ParseError) as exc:
        print(f"Import failed: {exc}", file=sys.stderr)
        sys.exit(1)
