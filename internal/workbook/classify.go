package workbook

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ValidationError is safe to show to the operator. Parser diagnostics remain
// internal errors and are not exposed as untranslated upload messages.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func validationError(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

var supplierNames = map[string]string{"iek": "IEK", "systeme": "Systeme Electric"}

func filenameSupplier(name string) (string, error) {
	name = strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if !strings.HasSuffix(name, ".xlsx") || strings.HasPrefix(name, "~$") {
		return "", validationError("Выберите исходные файлы .xlsx, без временных файлов Excel (~$).")
	}
	name = strings.NewReplacer("è", "e", "é", "e").Replace(name)
	systeme := strings.Contains(name, "system") || strings.Contains(name, "syseme")
	iek := strings.Contains(name, "иэк") || strings.Contains(name, "iek")
	if systeme && iek {
		return "", validationError("В имени файла «%s» указаны оба поставщика. Уточните поставщика в имени файла.", name)
	}
	if systeme {
		return "systeme", nil
	}
	if iek {
		return "iek", nil
	}
	// Generic report names do not identify a supplier, including the historical
	// IEK sales filenames. Their supplier must be resolved from the batch.
	return "", nil
}

// classifyBook recognizes the supported export layouts. Some layouts (daily
// transactions and seasonality) are shared, so their supplier remains unknown.
func classifyBook(b *book) (supplier, kind string, err error) {
	stop := errors.New("header recognized")
	err = b.rows(0, func(index int, values row) error {
		switch {
		case values["A"] == "Код 1с" && values["B"] == "Артикул ИЭК":
			supplier, kind = "iek", "incoming"
		case values["C"] == "Код 1с" && values["AX"] == "Остаток" && values["AY"] == "Зарезервировано" && values["AZ"] == "Свободный остаток":
			supplier, kind = "systeme", "incoming"
		case index == 1 && values["B"] == "Код 1с" && values["C"] == "Артикул поставщика" && values["D"] == "Наименование" && values["E"] == "Мин. разр. к отгр.":
			supplier, kind = "iek", "moq"
		case index == 1 && values["B"] == "Номенклатура" && values["C"] == "Номенклатура.Код" && values["D"] == "Артикул" && values["E"] == "Кратность":
			supplier, kind = "systeme", "moq"
		case index == 1 && values["A"] == "Дата" && values["D"] == "Код" && values["E"] == "Номенклатура" && values["F"] == "Ед." && values["G"] == "Склад" && values["H"] == "Количество":
			kind = "transactions"
		case index == 1 && findColumn(values, "Номенклатура.Код") != "" && findColumn(values, "Номенклатура") != "":
			for _, value := range values {
				if monthKey(value) != "" {
					kind = "monthly_sales"
					break
				}
			}
			if kind != "" {
				if findColumn(values, "Ед.", "Ед.изм") != "" {
					kind = "stock"
					if values["A"] == "Номенклатура" && values["B"] == "Ед." && values["C"] == "Номенклатура.Код" {
						supplier = "iek"
					} else if values["B"] == "Номенклатура" && values["C"] == "Номенклатура.Код" && values["D"] == "Ед.изм" {
						supplier = "systeme"
					}
				} else if values["A"] == "Номенклатура" && values["B"] == "Номенклатура.Код" {
					if values["C"] == "Артикул" {
						supplier = "systeme"
					} else if monthKey(values["C"]) != "" {
						supplier = "iek"
					}
				}
			}
		case month(values["B"]) > 0 && values["L"] != "":
			kind = "seasonality"
		}
		if kind != "" || index >= 10 {
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return "", "", err
	}
	if kind == "" {
		return "", "", validationError("Не удалось распознать структуру отчёта. Выберите исходную выгрузку с сохранёнными листами и заголовками колонок.")
	}
	return supplier, kind, nil
}
