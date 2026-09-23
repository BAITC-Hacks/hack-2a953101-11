package workbook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/electrokomplekt/replenishment/internal/planning"
)

var fixtureOptions = Options{AsOf: "2026-09-22", HistoryStart: "2025-01-01"}

func fixtureFiles(t *testing.T) []File {
	t.Helper()
	paths, err := filepath.Glob("testdata/iek/*.xlsx")
	if err != nil || len(paths) != 6 {
		t.Fatalf("six IEK fixtures required: %v %v", paths, err)
	}
	var files []File
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, File{Name: filepath.Base(path), Data: data})
	}
	return files
}

func zipParts(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, content := range parts {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func replacePart(t *testing.T, data []byte, part, old, replacement string) []byte {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[f.Name] = string(content)
	}
	if !strings.Contains(parts[part], old) {
		t.Fatalf("part %s does not contain %q", part, old)
	}
	parts[part] = strings.ReplaceAll(parts[part], old, replacement)
	return zipParts(t, parts)
}

func TestImportIEKPreservesBusinessRules(t *testing.T) {
	files := fixtureFiles(t)
	data, err := Import(context.Background(), files, fixtureOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Products) != 1 || len(data.Sales) != 1 || len(data.Stock) != 1 || len(data.Shipments) != 1 {
		t.Fatalf("unexpected result: %+v", data)
	}
	p := data.Products[0]
	if p.ID != "iek:001_" || p.InternalCode != "001_" || p.SKU != "SKU-001" || p.PackSize != 1 || p.MinOrderQuantity != 50 || p.Unit != "м" {
		t.Fatalf("lost exact code, article, units, or MOQ semantics: %+v", p)
	}
	if data.Sales[0].Quantity != 8.3 || data.Stock[0].OnHand != 43.2 || data.Stock[0].AsOf != "2026-09-01" || data.Shipments[0].Quantity != 5.5 || data.Shipments[0].ExpectedDate != "2026-10-10" {
		t.Fatalf("lost fractional quantities, returns, stock or arrival dates: %+v", data)
	}
	if !data.Suppliers[0].LeadTimeUnconfirmed || len(data.Suppliers[0].Seasonality) != 12 || strings.Contains(strings.Join(data.Source.Warnings, " "), "Расхождения") {
		t.Fatalf("incorrect lead time, seasonality, or reconciliation: %+v", data)
	}
	for left, right := 0, len(files)-1; left < right; left, right = left+1, right-1 {
		files[left], files[right] = files[right], files[left]
	}
	reversed, err := Import(context.Background(), files, fixtureOptions)
	if err != nil || !reflect.DeepEqual(data, reversed) {
		t.Fatalf("import must not depend on file order: %v", err)
	}
}

func TestInvalidRulesAndReelConversionsStayBlocked(t *testing.T) {
	files := fixtureFiles(t)
	for index := range files {
		if strings.HasPrefix(files[index].Name, "MOQ") {
			files[index].Data = replacePart(t, files[index].Data, "xl/worksheets/sheet1.xml", ">50<", ">#N/A<")
		}
		if strings.HasPrefix(files[index].Name, "Путь") {
			files[index].Data = replacePart(t, files[index].Data, "xl/worksheets/sheet1.xml", "Cable", "ЗАКУПАЮТСЯ БУХТАМИ, САДЯТСЯ МЕТРАЖОМ")
		}
	}
	data, err := Import(context.Background(), files, fixtureOptions)
	if err != nil {
		t.Fatal(err)
	}
	reasons := strings.Join(data.Products[0].ReviewReasons, ",")
	if !strings.Contains(reasons, "missing_order_rules") || !strings.Contains(reasons, "purchase_unit_conversion_unconfirmed") || len(data.Shipments) != 0 {
		t.Fatalf("invalid ordering or unconverted quantities were accepted: %+v", data)
	}
}

func bookRows(t *testing.T, rows []row) []byte {
	t.Helper()
	var sheet strings.Builder
	sheet.WriteString("<worksheet><sheetData>")
	for index, values := range rows {
		sheet.WriteString("<row>")
		var columns []string
		for col := range values {
			columns = append(columns, col)
		}
		sort.Strings(columns)
		for _, col := range columns {
			fmt.Fprintf(&sheet, `<c r="%s%d" t="inlineStr"><is><t>`, col, index+1)
			if err := xml.EscapeText(&sheet, []byte(values[col])); err != nil {
				t.Fatal(err)
			}
			sheet.WriteString("</t></is></c>")
		}
		sheet.WriteString("</row>")
	}
	sheet.WriteString("</sheetData></worksheet>")
	return zipParts(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="urn:rels"><sheets><sheet name="Data" r:id="r1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
	})
}

func TestSystemeAloneAndCombined(t *testing.T) {
	var seasonality []row
	seasonality = append(seasonality, row{"A": "год", "B": "янв", "L": "ноя"})
	for _, month := range []string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"} {
		seasonality = append(seasonality, row{"B": month, "L": "1.2"})
	}
	var files []File
	for name, rows := range map[string][]row{
		"Ежемесячные остатки SystemElectric.xlsx": {
			{"A": "Номенклатура", "B": "Номенклатура.Код", "C": "Ед.изм", "D": "сент. 2026"},
			{"A": "Switch", "B": "001_", "C": "шт", "D": "100"},
		},
		"Ежемесячные продажи SystemElectric.xlsx": {
			{"A": "Номенклатура", "B": "Номенклатура.Код", "C": "Артикул", "D": "сент. 2026"},
			{"A": "Switch", "B": "001_", "C": "SE-1", "D": "4"},
		},
		"Динамика продаж SystemElectric.xlsx": {
			{"A": "Дата", "D": "Код", "E": "Номенклатура", "F": "Ед.", "G": "Склад", "H": "Количество"},
			{"A": "01.09.2026 12:00:00", "D": "001_", "E": "Switch", "F": "шт", "G": "Алматы", "H": "4"},
		},
		"MOQ SystemElectric.xlsx": {
			{"B": "Номенклатура", "C": "Номенклатура.Код", "D": "Артикул", "E": "Кратность"},
			{"B": "Switch", "C": "001_", "D": "SE-1", "E": "12"},
		},
		"Сезонность SystemElectric.xlsx": seasonality,
		"Товар в пути SystemElectric 22.09.2026.xlsx": {
			{"AT": "СКЛАДЫ"},
			{"B": "Артикул поставщика", "C": "Код 1с", "D": "Наименование", "AX": "Остаток", "AY": "Зарезервировано", "AZ": "Свободный остаток", "BC": "СЭ в пути 24.09"},
			{"B": "SE-1", "C": "001_", "D": "Switch", "AX": "10.5", "AY": "2.1", "AZ": "8.4", "BC": "6"},
		},
	} {
		files = append(files, File{Name: name, Data: bookRows(t, rows)})
	}
	data, err := Import(context.Background(), files, fixtureOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Products) != 1 || data.Products[0].PackSize != 12 || data.Products[0].MinOrderQuantity != 0 || len(data.Products[0].ReviewReasons) != 0 {
		t.Fatalf("Systeme multiple/current stock: %+v", data)
	}
	if data.Stock[0].OnHand != 10.5 || data.Stock[0].Reserved != 2.1 || data.Stock[0].AsOf != "2026-09-22" || data.Shipments[0].ExpectedDate != "2026-09-24" {
		t.Fatalf("Systeme current stock and incoming: %+v", data)
	}
	if strings.Contains(data.Source.Label, "IEK") || strings.Contains(strings.Join(data.Source.Warnings, " "), "IEK") {
		t.Fatalf("warnings mention an absent supplier: %+v", data.Source)
	}
	combined, err := Import(context.Background(), append(files, fixtureFiles(t)...), fixtureOptions)
	if err != nil || len(combined.Products) != 2 || len(combined.Suppliers) != 2 {
		t.Fatalf("combined import: %+v %v", combined, err)
	}
}

func TestImportRejectsInvalidBatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]File, *Options) []File
		want string
	}{
		{"missing", func(f []File, _ *Options) []File { return f[:5] }, "missing required"},
		{"duplicate", func(f []File, _ *Options) []File { return append(f, f[0]) }, "duplicate"},
		{"unsupported", func(f []File, _ *Options) []File { f[0].Name = "report.xlsx"; return f }, "unrecognized supplier"},
		{"snapshot", func(f []File, o *Options) []File { o.AsOf = "2026-09-23"; return f }, "snapshot date"},
		{"date", func(f []File, o *Options) []File { o.HistoryStart = ""; return f }, "required YYYY-MM-DD"},
		{"interval", func(f []File, o *Options) []File { o.HistoryStart = "2027-01-01"; return f }, "must not be after"},
		{"archive", func(f []File, _ *Options) []File { f[0].Data = []byte("not zip"); return f }, "invalid XLSX ZIP"},
		{"headers", func(f []File, _ *Options) []File {
			for i := range f {
				if strings.Contains(f[i].Name, "Динамика") {
					f[i].Data = replacePart(t, f[i].Data, "xl/worksheets/sheet1.xml", "Количество", "Cost")
				}
			}
			return f
		}, "unsupported transaction headers"},
		{"warehouse", func(f []File, _ *Options) []File {
			for i := range f {
				if strings.Contains(f[i].Name, "Динамика") {
					f[i].Data = replacePart(t, f[i].Data, "xl/worksheets/sheet1.xml", "Алматы", "Other")
				}
			}
			return f
		}, "unexpected warehouse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := fixtureOptions
			files := tc.edit(fixtureFiles(t), &options)
			_, err := Import(context.Background(), files, options)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Import(ctx, fixtureFiles(t), fixtureOptions); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import: %v", err)
	}
}

func TestReaderSparseRichStringsAndCachedFormulas(t *testing.T) {
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="urn:rels"><sheets><sheet name="Data" r:id="r1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><r><t>Код</t></r><r><t> 1с</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="D1"><f>40+3.2</f><v>43.2</v></c><c r="E1" t="e"><v>#N/A</v></c></row><row r="5"><c r="B5" t="inlineStr"><is><r><t>001</t></r><r><t>_</t></r></is></c></row></sheetData></worksheet>`,
	}
	b, err := openBook(context.Background(), zipParts(t, parts), &budget{})
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	if err := b.rows(0, func(_ int, r row) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []row{{"A": "Код 1с", "D": "43.2", "E": "#N/A"}, {"B": "001_"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for _, tc := range []struct{ name, part, old, replacement, want string }{
		{"DTD", "xl/workbook.xml", "<workbook", "<!DOCTYPE workbook><workbook", "DTD"},
		{"external", "xl/_rels/workbook.xml.rels", "Target=", "TargetMode=\"External\" Target=", "external"},
		{"traversal", "xl/_rels/workbook.xml.rels", "worksheets/sheet1.xml", "../sheet1.xml", "unsafe"},
		{"index", "xl/worksheets/sheet1.xml", "<v>0</v>", "<v>999</v>", "shared string index"},
		{"uncached", "xl/worksheets/sheet1.xml", "<v>43.2</v>", "", "without a cached value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := replacePart(t, zipParts(t, parts), tc.part, tc.old, tc.replacement)
			b, err := openBook(context.Background(), data, &budget{})
			if err == nil {
				err = b.rows(0, nil)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := openBook(context.Background(), zipParts(t, parts), &budget{expanded: maxExpandedBytes}); err == nil {
		t.Fatal("aggregate expansion limit was ignored")
	}
	b, err = openBook(context.Background(), zipParts(t, parts), &budget{cells: maxCells})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rows(0, nil); err == nil {
		t.Fatal("aggregate cell limit was ignored")
	}
}

func TestRealSupplierWorkbooks(t *testing.T) {
	root := os.Getenv("SUPPLIER_WORKBOOK_DIR")
	if root == "" {
		t.Skip("set SUPPLIER_WORKBOOK_DIR to the directory with IEK, systemElectric, and the root IEK MOQ workbook")
	}
	var files []File
	for _, pattern := range []string{"IEK/*.xlsx", "systemElectric/*.xlsx"} {
		paths, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, File{Name: filepath.Base(path), Data: data})
		}
	}
	// Older source layouts keep IEK's MOQ only in the root. Do not also add
	// root copies when the supplier directory already contains the workbook.
	if _, err := os.Stat(filepath.Join(root, "IEK", "MOQ  ИЭК.xlsx")); os.IsNotExist(err) {
		name := "MOQ  ИЭК.xlsx"
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, File{Name: name, Data: data})
	} else if err != nil {
		t.Fatal(err)
	}
	data, err := Import(context.Background(), files, fixtureOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Products) != 3909 || len(data.Sales) != 140922 || len(data.Shipments) != 313 {
		t.Fatalf("unexpected real import totals: products=%d sales=%d shipments=%d", len(data.Products), len(data.Sales), len(data.Shipments))
	}
	if original, err := os.ReadFile(filepath.Join(root, "data/supplier-import.json")); err == nil {
		var previous planning.Dataset
		if err := json.Unmarshal(original, &previous); err != nil {
			t.Fatal(err)
		}
		for _, pair := range [][2]any{{data.Suppliers, previous.Suppliers}, {data.Products, previous.Products}, {data.Stock, previous.Stock}, {data.Shipments, previous.Shipments}} {
			a, _ := json.Marshal(pair[0])
			b, _ := json.Marshal(pair[1])
			if !bytes.Equal(a, b) {
				t.Errorf("Go conversion differs from Python reference for %T", pair[0])
			}
		}
		if len(data.Sales) != len(previous.Sales) {
			t.Fatal("daily sales count differs from Python reference")
		}
		for index, sale := range data.Sales {
			old := previous.Sales[index]
			if sale.ProductID != old.ProductID || sale.Date != old.Date || math.Abs(sale.Quantity-old.Quantity) > 1e-8 {
				t.Fatalf("daily sale differs from Python reference: %+v / %+v", sale, old)
			}
		}
	}
}
