package workbook

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The columns mirror the original Systeme exports, including the leading №
// column in stock, Кратность in sales, and the second-row incoming header.
func systemeLayoutFiles(t *testing.T) []File {
	t.Helper()
	seasonality := []row{{}, {}, {"A": "год", "B": "янв", "L": "ноя"}}
	for _, month := range []string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"} {
		seasonality = append(seasonality, row{"B": month, "L": "1.2"})
	}
	reports := []struct {
		name string
		rows []row
	}{
		{"Ежемесячные остатки SystemElectric 2024-2026.xlsx", []row{
			{"A": "№", "B": "Номенклатура", "C": "Номенклатура.Код", "D": "Ед.изм", "E": "сент. 2026"},
			{"A": "1", "B": "Switch", "C": "001_", "D": "шт", "E": "100"},
		}},
		{"Ежемесячные продажи в кол-м выражении SystemElectric 2024-2026.xlsx", []row{
			{"A": "Номенклатура", "B": "Номенклатура.Код", "C": "Артикул", "D": "Кратность", "E": "сент. 2026"},
			{"A": "Switch", "B": "001_", "C": "SE-1", "D": "12", "E": "4"},
		}},
		{"Динамика продаж_Syseme Electric_2025-2026.xlsx", []row{
			{"A": "Дата", "D": "Код", "E": "Номенклатура", "F": "Ед.", "G": "Склад", "H": "Количество"},
			{"A": "01.09.2026 12:00:00", "D": "001_", "E": "Switch", "F": "шт", "G": "Алматы", "H": "4"},
		}},
		{"Сезонность SystemElectric 2024-2026.xlsx", seasonality},
		{"MOQ SystemElectric.xlsx", []row{
			{"A": "№", "B": "Номенклатура", "C": "Номенклатура.Код", "D": "Артикул", "E": "Кратность"},
			{"A": "1", "B": "Switch", "C": "001_", "D": "SE-1", "E": "12"},
		}},
		{"Товар в пути_SystemElectric на 22.09.2026.xlsx", []row{
			{"AT": "СКЛАДЫ"},
			{"A": "№", "B": "Артикул поставщика", "C": "Код 1с", "D": "Наименование", "AX": "Остаток", "AY": "Зарезервировано", "AZ": "Свободный остаток", "BC": "СЭ в пути 24.09"},
			{"A": "1", "B": "SE-1", "C": "001_", "D": "Switch", "AX": "10.5", "AY": "2.1", "AZ": "8.4", "BC": "6"},
		}},
	}
	files := make([]File, 0, len(reports))
	for _, report := range reports {
		files = append(files, File{Name: report.name, Data: bookRows(t, report.rows)})
	}
	return files
}

func genericReportNames(files []File) []File {
	files = append([]File(nil), files...)
	for index := range files {
		// Keep only the snapshot date required by incoming validation. Even the
		// report type is intentionally absent from these names.
		files[index].Name = fmt.Sprintf("report_%d_22.09.2026.xlsx", index+1)
	}
	return files
}

func TestWorkbookClassificationUsesOriginalColumnLayouts(t *testing.T) {
	for _, supplier := range []string{"iek", "systeme"} {
		files := fixtureFiles(t)
		if supplier == "systeme" {
			files = systemeLayoutFiles(t)
		}
		for _, file := range files {
			t.Run(supplier+"/"+file.Name, func(t *testing.T) {
				b, err := openBook(context.Background(), file.Data, &budget{})
				if err != nil {
					t.Fatal(err)
				}
				gotSupplier, kind, err := classifyBook(b)
				if err != nil {
					t.Fatal(err)
				}
				wantKind := ""
				for prefix, candidate := range map[string]string{"Ежемесячные остатки": "stock", "Ежемесячные продажи": "monthly_sales", "Динамика": "transactions", "Сезонность": "seasonality", "MOQ": "moq", "Путь": "incoming", "Товар в пути": "incoming"} {
					if strings.HasPrefix(file.Name, prefix) {
						wantKind = candidate
					}
				}
				wantSupplier := supplier
				if wantKind == "transactions" || wantKind == "seasonality" {
					wantSupplier = ""
				}
				if gotSupplier != wantSupplier || kind != wantKind {
					t.Fatalf("columns classified as %q/%q, want %q/%q", gotSupplier, kind, wantSupplier, wantKind)
				}
			})
		}
	}
}

func TestSupplierBatchesUseContentsAndAreOrderIndependent(t *testing.T) {
	iek, systeme := fixtureFiles(t), systemeLayoutFiles(t)
	legacyNames := append([]File(nil), systeme...)
	legacyNames[1].Name = "Ежемесячные продажи в количественном выражении за последние 2 года.xlsx"
	legacyNames[2].Name = "Динамика продаж_2025-2026.xlsx"
	for _, tc := range []struct {
		name      string
		files     []File
		suppliers []string
	}{
		{"IEK", iek, []string{"iek"}},
		{"Systeme", systeme, []string{"systeme"}},
		{"both", append(append([]File(nil), iek...), systeme...), []string{"iek", "systeme"}},
		{"IEK without supplier or report names", genericReportNames(iek), []string{"iek"}},
		{"Systeme without supplier or report names", genericReportNames(systeme), []string{"systeme"}},
		{"Systeme with legacy generic names", legacyNames, []string{"systeme"}},
		{"both with generic IEK names", append(genericReportNames(iek), systeme...), []string{"iek", "systeme"}},
		{"both with generic Systeme names", append(append([]File(nil), iek...), genericReportNames(systeme)...), []string{"iek", "systeme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Import(context.Background(), tc.files, fixtureOptions)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, supplier := range data.Suppliers {
				got = append(got, supplier.ID)
			}
			if !reflect.DeepEqual(got, tc.suppliers) || len(data.Products) != len(tc.suppliers) {
				t.Fatalf("unexpected supplier/product grouping: suppliers %v, products %+v", got, data.Products)
			}
			reversed := append([]File(nil), tc.files...)
			for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
				reversed[left], reversed[right] = reversed[right], reversed[left]
			}
			again, err := Import(context.Background(), reversed, fixtureOptions)
			if err != nil || !reflect.DeepEqual(data, again) {
				t.Fatalf("file order changed import: %v", err)
			}
		})
	}
}

func TestSystemeFilenameAliasesRemainSupplementary(t *testing.T) {
	for _, alias := range []string{"System", "Systeme", "Sysème", "Syseme", "SYSTEME"} {
		t.Run(alias, func(t *testing.T) {
			files := genericReportNames(systemeLayoutFiles(t))
			for index := range files {
				files[index].Name = strings.TrimSuffix(files[index].Name, ".xlsx") + " " + alias + " Electric.xlsx"
			}
			data, err := Import(context.Background(), files, fixtureOptions)
			if err != nil || len(data.Suppliers) != 1 || data.Suppliers[0].ID != "systeme" {
				t.Fatalf("alias %q: suppliers %+v, error %v", alias, data.Suppliers, err)
			}
		})
	}
}

func TestIncompleteBatchesReportOnlyDetectedSuppliers(t *testing.T) {
	without := func(files []File, prefix string) []File {
		var kept []File
		for _, file := range files {
			if !strings.HasPrefix(file.Name, prefix) {
				kept = append(kept, file)
			}
		}
		return kept
	}
	iek, systeme := fixtureFiles(t), systemeLayoutFiles(t)
	for _, tc := range []struct {
		name    string
		files   []File
		want    []string
		exclude string
	}{
		{"IEK only", without(iek, "Сезонность"), []string{"IEK: не хватает файлов: Сезонность."}, "Systeme Electric"},
		{"Systeme only", without(systeme, "Ежемесячные остатки"), []string{"Systeme Electric: не хватает файлов: Ежемесячные остатки."}, "IEK"},
		{"both incomplete", append(without(iek, "Сезонность"), without(systeme, "Ежемесячные остатки")...), []string{"IEK: не хватает файлов: Сезонность.", "Systeme Electric: не хватает файлов: Ежемесячные остатки."}, ""},
		{"complete IEK and incomplete Systeme", append(append([]File(nil), iek...), without(systeme, "Ежемесячные остатки")...), []string{"Systeme Electric: не хватает файлов: Ежемесячные остатки."}, "IEK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Import(context.Background(), tc.files, fixtureOptions)
			var public *ValidationError
			if !errors.As(err, &public) {
				t.Fatalf("expected public Russian validation error, got %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(public.Message, want) {
					t.Fatalf("error %q does not name missing reports %q", public.Message, want)
				}
			}
			if tc.exclude != "" && strings.Contains(public.Message, tc.exclude) {
				t.Fatalf("error names an absent or complete supplier: %s", public.Message)
			}
		})
	}
}

func TestSupplierClassificationRejectsConflictsAndAmbiguity(t *testing.T) {
	conflictingSysteme := systemeLayoutFiles(t)
	conflictingSysteme[4].Name = "MOQ IEK.xlsx"
	conflictingIEK := fixtureFiles(t)
	for index := range conflictingIEK {
		if strings.HasPrefix(conflictingIEK[index].Name, "MOQ") {
			conflictingIEK[index].Name = "MOQ Systeme Electric.xlsx"
		}
	}
	shared := genericReportNames(systemeLayoutFiles(t))
	for _, tc := range []struct {
		name  string
		files []File
		want  string
	}{
		{"Systeme contents with IEK name", conflictingSysteme, "структура соответствует Systeme Electric, а имя указывает на IEK"},
		{"IEK contents with Systeme name", conflictingIEK, "структура соответствует IEK, а имя указывает на Systeme Electric"},
		{"shared layouts without supplier evidence", []File{shared[2], shared[3]}, "Не удалось однозначно определить поставщика"},
		{"both anonymous shared layouts", append(genericReportNames(fixtureFiles(t)), shared...), "Не удалось однозначно определить поставщика"},
		{"unknown columns", []File{{Name: "report.xlsx", Data: bookRows(t, []row{{"A": "Unknown", "B": "Columns"}})}}, "Не удалось распознать структуру отчёта"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Import(context.Background(), tc.files, fixtureOptions)
			var public *ValidationError
			if !errors.As(err, &public) || !strings.Contains(public.Message, tc.want) {
				t.Fatalf("want Russian validation error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRenamedDuplicateCannotFillAnotherSuppliersMissingReport(t *testing.T) {
	iek := fixtureFiles(t)
	files := append([]File(nil), iek...)
	for _, file := range systemeLayoutFiles(t) {
		if !strings.HasPrefix(file.Name, "Динамика") {
			files = append(files, file)
		}
	}
	for _, file := range iek {
		if strings.HasPrefix(file.Name, "Динамика") {
			file.Name = "daily-copy.xlsx"
			files = append(files, file)
		}
	}
	_, err := Import(context.Background(), files, fixtureOptions)
	var public *ValidationError
	if !errors.As(err, &public) || !strings.Contains(public.Message, "совпадает с уже добавленным отчётом") {
		t.Fatalf("renamed IEK duplicate must not become Systeme sales: %v", err)
	}
}
