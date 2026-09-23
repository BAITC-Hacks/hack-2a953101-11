import tempfile
import unittest
from pathlib import Path
from zipfile import ZipFile

from import_supplier_workbooks import Importer, Workbook, numeric


class ImportTests(unittest.TestCase):
    def test_xlsx_sparse_cells_cached_formulas_and_errors(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "sample.xlsx"
            with ZipFile(path, "w") as z:
                z.writestr("xl/workbook.xml", '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" r:id="r1"/></sheets></workbook>')
                z.writestr("xl/_rels/workbook.xml.rels", '<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/></Relationships>')
                z.writestr("xl/sharedStrings.xml", '<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><r><t>Код</t></r><r><t> 1с</t></r></si></sst>')
                z.writestr("xl/worksheets/sheet1.xml", '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="D1"><f>40+3.2</f><v>43.2</v></c><c r="E1" t="e"><v>#N/A</v></c></row><row r="2"><c r="B2" t="inlineStr"><is><t>001_</t></is></c></row></sheetData></worksheet>')
            report = {"files": []}
            workbook = Workbook(path, report)
            try:
                rows = list(workbook.rows(0))
            finally:
                workbook.close()
            self.assertEqual(rows, [{"A": "Код 1с", "D": "43.2", "E": "#N/A"}, {"B": "001_"}])
            self.assertEqual(report["files"][0]["sheets"][0]["formula_errors"], 1)

    def test_minimum_differs_from_multiple_and_errors_stay_visible(self):
        imp = Importer("2026-09-22", "2025-01-01")
        imp.moq("iek", iter([{}, {"B": "001_", "C": "SKU", "D": "Cable", "E": "50"}, {"B": "002_", "C": "BAD", "E": "#N/A"}]))
        imp.moq("systeme", iter([{}, {"C": "003_", "D": "SE", "B": "Switch", "E": "12"}]))
        self.assertEqual(imp.products["iek:001_"]["min_order_quantity"], 50)
        self.assertEqual(imp.products["iek:001_"]["pack_size"], 1)
        self.assertEqual(imp.products["systeme:003_"]["pack_size"], 12)
        self.assertIn("missing_order_rules", imp.products["iek:002_"]["review_reasons"])

    def test_transactions_are_signed_and_not_added_to_monthly_totals(self):
        imp = Importer("2026-09-22", "2025-01-01")
        imp.monthly_sales("iek", iter([{"A": "Номенклатура", "B": "Номенклатура.Код", "C": "сент. 2026"}, {"A": "Cable", "B": "001_", "C": "8"}]))
        imp.transactions("iek", iter([{}, {"A": "01.09.2026 12:00:00", "D": "001_", "E": "Cable", "F": "м", "G": "Алматы", "H": "10"}, {"A": "01.09.2026 13:00:00", "D": "001_", "F": "м", "G": "Алматы", "H": "-2"}]))
        data = imp.finish()
        self.assertEqual(data["sales"][0]["quantity"], 8)
        self.assertEqual(imp.report["monthly_reconciliation"], [])

    def test_seasonal_header_not_parsed_as_coefficient(self):
        imp = Importer("2026-09-22", "2025-01-01")
        imp.suppliers = [{"id": "iek"}]
        months = ["янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"]
        imp.seasonality("iek", iter([{"A": "год", "B": "янв", "L": "ноя"}] + [{"B": m, "L": "1"} for m in months]))
        self.assertEqual(imp.suppliers[0]["seasonality"], [1] * 12)

    def test_incoming_eta_and_unconfirmed_reel_conversion(self):
        imp = Importer("2026-09-22", "2025-01-01")
        imp.iek_shipments(iter([{"D": "поступление до 10.10.2026"}, {"A": "001_", "B": "cable", "C": "ЗАКУПАЮТСЯ БУХТАМИ, САДЯТСЯ МЕТРАЖОМ", "D": "2"}, {"A": "002_", "B": "switch", "D": "12"}]))
        self.assertEqual(len(imp.shipments), 1)
        self.assertEqual(imp.shipments[0]["expected_date"], "2026-10-10")
        self.assertIn("purchase_unit_conversion_unconfirmed", imp.products["iek:001_"]["review_reasons"])

    def test_decimal_quantities_and_invalid_values(self):
        self.assertEqual(str(numeric("43,2")), "43.2")
        for value in ("#N/A", "NaN", "Infinity"):
            with self.assertRaises(ValueError): numeric(value)


if __name__ == "__main__":
    unittest.main()
