package workbook

import (
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/electrokomplekt/replenishment/internal/planning"
)

type File struct {
	Name string
	Data []byte
}

// Options are explicit because neither a file modification timestamp nor the
// last nonzero sale establishes the coverage of a warehouse snapshot.
type Options struct {
	AsOf         string
	HistoryStart string
}

var (
	yearPattern    = regexp.MustCompile(`\d{4}`)
	datePattern    = regexp.MustCompile(`\d{2}\.\d{2}\.\d{4}`)
	arrivalPattern = regexp.MustCompile(`поступление до (\d{2}\.\d{2}\.\d{4})`)
	dayMonth       = regexp.MustCompile(`\d{2}\.\d{2}`)
)

var kinds = []string{"stock", "monthly_sales", "transactions", "seasonality", "moq", "incoming"}
var kindLabels = map[string]string{
	"stock": "Ежемесячные остатки", "monthly_sales": "Ежемесячные продажи", "transactions": "Динамика продаж",
	"seasonality": "Сезонность", "moq": "MOQ", "incoming": "Путь / Товар в пути",
}
var months = map[string]int{"янв": 1, "фев": 2, "мар": 3, "апр": 4, "май": 5, "июн": 6, "июл": 7, "авг": 8, "сен": 9, "окт": 10, "ноя": 11, "дек": 12}

func classify(name string) (supplier, kind string, err error) {
	name = strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if !strings.HasSuffix(name, ".xlsx") || strings.HasPrefix(name, "~$") {
		return "", "", fmt.Errorf("expected an .xlsx supplier workbook")
	}
	switch {
	case strings.Contains(name, "system") || strings.Contains(name, "syseme"):
		supplier = "systeme"
	case strings.Contains(name, "иэк") || strings.Contains(name, "iek"):
		supplier = "iek"
	case name == "динамика продаж_2025-2026.xlsx" || name == "ежемесячные продажи в количественном выражении за последние 2 года.xlsx":
		supplier = "iek"
	default:
		return "", "", fmt.Errorf("unrecognized supplier filename; use the original IEK or Systeme Electric workbook names")
	}
	switch {
	case strings.HasPrefix(name, "ежемесячные остатки"):
		kind = "stock"
	case strings.HasPrefix(name, "ежемесячные продажи"):
		kind = "monthly_sales"
	case strings.HasPrefix(name, "динамика продаж"):
		kind = "transactions"
	case strings.HasPrefix(name, "сезонность"):
		kind = "seasonality"
	case strings.HasPrefix(name, "moq"):
		kind = "moq"
	case strings.HasPrefix(name, "путь") || strings.HasPrefix(name, "товар в пути"):
		kind = "incoming"
	default:
		return "", "", fmt.Errorf("unsupported workbook type")
	}
	return supplier, kind, nil
}

// Import converts a complete six-workbook export for one or both suppliers. It
// returns a preview and never changes persisted data. Input order is irrelevant.
func Import(ctx context.Context, files []File, options Options) (planning.Dataset, error) {
	if err := ctx.Err(); err != nil {
		return planning.Dataset{}, err
	}
	for _, date := range []string{options.AsOf, options.HistoryStart} {
		if _, err := planning.ParseDate(date); err != nil {
			return planning.Dataset{}, fmt.Errorf("as_of and history_start are required YYYY-MM-DD dates: %w", err)
		}
	}
	if options.HistoryStart > options.AsOf {
		return planning.Dataset{}, fmt.Errorf("history_start must not be after as_of")
	}
	if len(files) == 0 || len(files) > 12 {
		return planning.Dataset{}, fmt.Errorf("select six workbooks per supplier (6 or 12 files)")
	}
	groups := map[string]map[string]File{}
	var compressed int64
	for _, file := range files {
		compressed += int64(len(file.Data))
		if compressed > 64<<20 {
			return planning.Dataset{}, fmt.Errorf("uploaded workbooks exceed 64 MiB")
		}
		supplier, kind, err := classify(file.Name)
		if err != nil {
			return planning.Dataset{}, fmt.Errorf("%s: %w", file.Name, err)
		}
		if groups[supplier] == nil {
			groups[supplier] = map[string]File{}
		}
		if previous, exists := groups[supplier][kind]; exists {
			return planning.Dataset{}, fmt.Errorf("duplicate %s %s workbooks: %s and %s", supplier, kindLabels[kind], previous.Name, file.Name)
		}
		groups[supplier][kind] = file
	}
	imp := importer{ctx: ctx, options: options, data: planning.EmptyDataset(), products: map[string]*planning.Product{}, stock: map[string]planning.Stock{}, sales: map[saleKey]float64{}, monthly: map[saleKey]float64{}, monthlyDaily: map[saleKey]float64{}, articles: map[string]string{}, orderRules: map[string]bool{}}
	limits := &budget{}
	for _, supplier := range []string{"iek", "systeme"} {
		group := groups[supplier]
		if group == nil {
			continue
		}
		var missing []string
		for _, kind := range kinds {
			if _, exists := group[kind]; !exists {
				missing = append(missing, kindLabels[kind])
			}
		}
		if len(missing) > 0 {
			return planning.Dataset{}, fmt.Errorf("%s: missing required workbooks: %s; select all six files together", supplier, strings.Join(missing, ", "))
		}
		stamp, err := time.Parse("02.01.2006", datePattern.FindString(group["incoming"].Name))
		if err != nil || stamp.Format(planning.DateLayout) != options.AsOf {
			return planning.Dataset{}, fmt.Errorf("as_of must match the snapshot date in incoming workbook filename %q", group["incoming"].Name)
		}
		name := "IEK"
		if supplier == "systeme" {
			name = "Systeme Electric"
		}
		imp.data.Suppliers = append(imp.data.Suppliers, planning.Supplier{ID: supplier, Name: name, LeadTimeUnconfirmed: true})
		for _, kind := range kinds {
			file := group[kind]
			b, err := openBook(ctx, file.Data, limits)
			if err == nil {
				err = imp.consume(b, supplier, kind)
			}
			if err != nil {
				return planning.Dataset{}, fmt.Errorf("%s: %w", file.Name, err)
			}
		}
	}
	return imp.finish()
}

type saleKey struct{ product, date string }
type importer struct {
	ctx          context.Context
	options      Options
	data         planning.Dataset
	products     map[string]*planning.Product
	stock        map[string]planning.Stock
	sales        map[saleKey]float64
	monthly      map[saleKey]float64
	monthlyDaily map[saleKey]float64
	articles     map[string]string
	orderRules   map[string]bool
}

func number(value string) (float64, error) {
	value = strings.NewReplacer(" ", "", "\u00a0", "", ",", ".").Replace(value)
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > planning.MaxQuantity {
		return 0, fmt.Errorf("invalid quantity %q (must be finite and within ±%g)", value, planning.MaxQuantity)
	}
	return n, nil
}

func month(value string) int {
	r := []rune(strings.ToLower(value))
	if len(r) < 3 {
		return 0
	}
	return months[string(r[:3])]
}

func monthKey(value string) string {
	year := yearPattern.FindString(value)
	if m := month(value); m > 0 && year != "" {
		return fmt.Sprintf("%s-%02d", year, m)
	}
	return ""
}

func issue(p *planning.Product, reason string) {
	for _, previous := range p.ReviewReasons {
		if previous == reason {
			return
		}
	}
	p.ReviewReasons = append(p.ReviewReasons, reason)
}

func (imp *importer) product(supplier, code, name, unit string) *planning.Product {
	code = strings.TrimSpace(code)
	id := supplier + ":" + code
	p := imp.products[id]
	if p == nil {
		if name == "" {
			name = code
		}
		p = &planning.Product{ID: id, InternalCode: code, SKU: code, Name: name, SupplierID: supplier, Unit: unit, PackSize: 1}
		imp.products[id] = p
	}
	if name != "" && p.Name == p.InternalCode {
		p.Name = name
	}
	if unit != "" {
		if p.Unit != "" && p.Unit != unit {
			issue(p, "unit_conflict")
		} else {
			p.Unit = unit
		}
	}
	return p
}

func (imp *importer) article(p *planning.Product, value string) {
	if value == "" || strings.HasPrefix(value, "#") {
		return
	}
	if previous := imp.articles[p.ID]; previous != "" && previous != value {
		issue(p, "supplier_article_conflict")
	} else {
		imp.articles[p.ID], p.SKU = value, value
	}
}

func findColumn(header row, names ...string) string {
	for col, value := range header {
		for _, name := range names {
			if value == name {
				return col
			}
		}
	}
	return ""
}

func (imp *importer) consume(b *book, supplier, kind string) error {
	var visit func(int, row) error
	switch kind {
	case "stock", "monthly_sales":
		visit = imp.monthlyReader(supplier, kind == "stock")
	case "transactions":
		visit = imp.transactionReader(supplier)
	case "moq":
		visit = imp.moqReader(supplier)
	case "seasonality":
		visit = imp.seasonalityReader(supplier)
	case "incoming":
		if supplier == "iek" {
			visit = imp.iekReader()
		} else {
			visit = imp.systemeReader()
		}
	}
	rows := 0
	if err := b.rows(0, func(index int, values row) error { rows++; return visit(index, values) }); err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("worksheet has no header")
	}
	if kind == "incoming" && supplier == "systeme" {
		if err := visit(0, nil); err != nil {
			return err
		}
	}
	for index := 1; index < len(b.sheets); index++ {
		if err := b.rows(index, nil); err != nil {
			return err
		}
	}
	if kind == "seasonality" {
		for _, factor := range imp.data.Suppliers[len(imp.data.Suppliers)-1].Seasonality {
			if factor <= 0 || factor > 10 {
				return fmt.Errorf("seasonality must contain 12 positive cached coefficients <= 10 in column L")
			}
		}
	}
	return nil
}

func (imp *importer) monthlyReader(supplier string, stock bool) func(int, row) error {
	var codeCol, nameCol, unitCol, articleCol, latestCol, stamp string
	monthCols := map[string]string{}
	return func(index int, values row) error {
		if index == 1 {
			codeCol, nameCol = findColumn(values, "Номенклатура.Код"), findColumn(values, "Номенклатура")
			unitCol, articleCol = findColumn(values, "Ед.", "Ед.изм"), findColumn(values, "Артикул")
			if codeCol == "" || nameCol == "" || (stock && unitCol == "") {
				return fmt.Errorf("unsupported monthly export headers")
			}
			for col, value := range values {
				if key := monthKey(value); key != "" {
					monthCols[col] = key
					if key+"-01" <= imp.options.AsOf && key+"-01" > stamp {
						latestCol, stamp = col, key+"-01"
					}
				}
			}
			if len(monthCols) == 0 || (stock && latestCol == "") {
				return fmt.Errorf("no dated monthly columns at or before as_of")
			}
			return nil
		}
		if values[codeCol] == "" {
			return nil
		}
		p := imp.product(supplier, values[codeCol], values[nameCol], values[unitCol])
		if stock {
			amount, err := number(values[latestCol])
			if err != nil {
				return err
			}
			if amount < 0 {
				issue(p, "negative_stock")
			}
			imp.stock[p.ID] = planning.Stock{ProductID: p.ID, OnHand: max(0, amount), AsOf: stamp}
		} else {
			imp.article(p, values[articleCol])
			for col, key := range monthCols {
				amount, err := number(values[col])
				if err != nil {
					return err
				}
				imp.monthly[saleKey{p.ID, key}] = amount
			}
		}
		return nil
	}
}

func (imp *importer) transactionReader(supplier string) func(int, row) error {
	return func(index int, values row) error {
		if index == 1 {
			if values["A"] != "Дата" || values["D"] != "Код" || values["E"] != "Номенклатура" || values["F"] != "Ед." || values["G"] != "Склад" || values["H"] != "Количество" {
				return fmt.Errorf("unsupported transaction headers")
			}
			return nil
		}
		if values["D"] == "" || values["A"] == "Итого" {
			return nil
		}
		stamp, err := time.Parse("02.01.2006 15:04:05", values["A"])
		if err != nil {
			return fmt.Errorf("invalid transaction date %q; expected DD.MM.YYYY HH:MM:SS", values["A"])
		}
		quantity, err := number(values["H"])
		if err != nil {
			return err
		}
		p := imp.product(supplier, values["D"], values["E"], values["F"])
		day := stamp.Format(planning.DateLayout)
		if day < imp.options.HistoryStart || day > imp.options.AsOf {
			return nil
		}
		if values["G"] != "Алматы" {
			return fmt.Errorf("unexpected warehouse %q; this import requires Алматы", values["G"])
		}
		imp.sales[saleKey{p.ID, day}] += quantity
		imp.monthlyDaily[saleKey{p.ID, day[:7]}] += quantity
		return nil
	}
}

func (imp *importer) moqReader(supplier string) func(int, row) error {
	codeCol, articleCol, nameCol := "B", "C", "D"
	if supplier == "systeme" {
		codeCol, articleCol, nameCol = "C", "D", "B"
	}
	seen := map[string]float64{}
	return func(index int, values row) error {
		if index == 1 {
			if (supplier == "iek" && (values["B"] != "Код 1с" || values["C"] != "Артикул поставщика" || values["D"] != "Наименование" || values["E"] != "Мин. разр. к отгр.")) || (supplier == "systeme" && (values["B"] != "Номенклатура" || values["C"] != "Номенклатура.Код" || values["D"] != "Артикул" || values["E"] != "Кратность")) {
				return fmt.Errorf("unsupported MOQ headers")
			}
			return nil
		}
		if values[codeCol] == "" {
			return nil
		}
		p := imp.product(supplier, values[codeCol], values[nameCol], "")
		imp.article(p, values[articleCol])
		amount, err := number(values["E"])
		if err != nil || amount <= 0 {
			issue(p, "missing_order_rules")
			return nil
		}
		if previous, exists := seen[p.ID]; exists && previous != amount {
			issue(p, "conflicting_order_rules")
		}
		seen[p.ID] = amount
		if supplier == "iek" {
			p.MinOrderQuantity = amount
		} else {
			p.PackSize = amount
		}
		imp.orderRules[p.ID] = true
		return nil
	}
}

func (imp *importer) seasonalityReader(supplier string) func(int, row) error {
	index := len(imp.data.Suppliers) - 1
	imp.data.Suppliers[index].Seasonality = make([]float64, 12)
	return func(_ int, values row) error {
		m := month(values["B"])
		if m == 0 || values["L"] == "" || values["A"] == "год" {
			return nil
		}
		factor, err := number(values["L"])
		if err != nil {
			return fmt.Errorf("%s seasonal coefficient: %w", supplier, err)
		}
		imp.data.Suppliers[index].Seasonality[m-1] = factor
		return nil
	}
}

func (imp *importer) iekReader() func(int, row) error {
	arrivals := map[string]string{}
	var columns []string
	seen := map[string]bool{}
	return func(index int, values row) error {
		if index == 1 {
			if values["A"] != "Код 1с" || values["B"] != "Артикул ИЭК" {
				return fmt.Errorf("unsupported IEK incoming headers")
			}
			for col, value := range values {
				if match := arrivalPattern.FindStringSubmatch(value); len(match) > 0 {
					stamp, err := time.Parse("02.01.2006", match[1])
					if err != nil {
						return fmt.Errorf("invalid incoming arrival date: %w", err)
					}
					arrivals[col] = stamp.Format(planning.DateLayout)
					columns = append(columns, col)
				}
			}
			sort.Slice(columns, func(i, j int) bool {
				return len(columns[i]) < len(columns[j]) || (len(columns[i]) == len(columns[j]) && columns[i] < columns[j])
			})
			if len(columns) == 0 {
				return fmt.Errorf("no incoming arrival dates in IEK headers")
			}
			return nil
		}
		if values["A"] == "" {
			return nil
		}
		p := imp.product("iek", values["A"], values["C"], "")
		imp.article(p, values["B"])
		conversion := strings.Contains(strings.ToUpper(values["C"]), "ЗАКУПАЮТСЯ БУХТАМИ")
		if conversion {
			issue(p, "purchase_unit_conversion_unconfirmed")
		}
		var keys []string
		for col := range values {
			keys = append(keys, col)
		}
		sort.Strings(keys)
		var fingerprint strings.Builder
		for _, col := range keys {
			fmt.Fprintf(&fingerprint, "%d:%s%d:%s", len(col), col, len(values[col]), values[col])
		}
		if seen[fingerprint.String()] {
			return nil
		}
		seen[fingerprint.String()] = true
		for _, col := range columns {
			quantity, err := number(values[col])
			if err != nil {
				return err
			}
			if quantity > 0 && !conversion {
				imp.data.Shipments = append(imp.data.Shipments, planning.Shipment{ID: fmt.Sprintf("iek:%d:%s", index, col), ProductID: p.ID, Quantity: quantity, ExpectedDate: arrivals[col]})
			}
		}
		return nil
	}
}

func (imp *importer) systemeReader() func(int, row) error {
	expected := ""
	return func(index int, values row) error {
		if index == 0 {
			if expected == "" {
				return fmt.Errorf("Systeme Electric incoming header not found")
			}
			return nil
		}
		if expected == "" {
			if values["C"] != "Код 1с" {
				if index > 10 {
					return fmt.Errorf("Systeme Electric incoming header not found in first ten rows")
				}
				return nil
			}
			stamp, err := time.Parse("02.01.2006", dayMonth.FindString(values["BC"])+"."+imp.options.AsOf[:4])
			if err != nil || values["AX"] != "Остаток" || values["AY"] != "Зарезервировано" || values["AZ"] != "Свободный остаток" {
				return fmt.Errorf("unsupported Systeme Electric incoming stock or arrival headers")
			}
			expected = stamp.Format(planning.DateLayout)
			return nil
		}
		if values["C"] == "" {
			return nil
		}
		p := imp.product("systeme", values["C"], values["D"], "")
		imp.article(p, values["B"])
		onHand, err := number(values["AX"])
		if err != nil {
			return err
		}
		reserved, err := number(values["AY"])
		if err != nil {
			return err
		}
		free, err := number(values["AZ"])
		if err != nil {
			return err
		}
		if onHand < 0 || reserved < 0 || reserved > onHand {
			issue(p, "invalid_current_stock")
		} else {
			imp.stock[p.ID] = planning.Stock{ProductID: p.ID, OnHand: onHand, Reserved: reserved, AsOf: imp.options.AsOf}
			if math.Abs(free-(onHand-reserved)) > 1e-8 {
				issue(p, "stock_reservation_mismatch")
			}
		}
		quantity, err := number(values["BC"])
		if err != nil {
			return err
		}
		if quantity > 0 {
			imp.data.Shipments = append(imp.data.Shipments, planning.Shipment{ID: fmt.Sprintf("systeme:%d:BC", index), ProductID: p.ID, Quantity: quantity, ExpectedDate: expected})
		}
		return nil
	}
}

func (imp *importer) finish() (planning.Dataset, error) {
	for _, p := range imp.products {
		if err := imp.ctx.Err(); err != nil {
			return planning.Dataset{}, err
		}
		if !imp.orderRules[p.ID] {
			issue(p, "missing_order_rules")
		}
		if imp.articles[p.ID] == "" {
			issue(p, "supplier_article_missing")
		}
		if _, exists := imp.stock[p.ID]; !exists {
			imp.stock[p.ID] = planning.Stock{ProductID: p.ID, Unverified: true}
		}
		imp.data.Products = append(imp.data.Products, *p)
		imp.data.Stock = append(imp.data.Stock, imp.stock[p.ID])
	}
	for key, quantity := range imp.sales {
		quantity = math.Round(quantity*1e9) / 1e9
		if quantity != 0 {
			imp.data.Sales = append(imp.data.Sales, planning.Sale{ProductID: key.product, Date: key.date, Quantity: quantity})
		}
	}
	sort.Slice(imp.data.Products, func(i, j int) bool { return imp.data.Products[i].ID < imp.data.Products[j].ID })
	sort.Slice(imp.data.Stock, func(i, j int) bool { return imp.data.Stock[i].ProductID < imp.data.Stock[j].ProductID })
	sort.Slice(imp.data.Sales, func(i, j int) bool {
		a, b := imp.data.Sales[i], imp.data.Sales[j]
		return a.ProductID < b.ProductID || (a.ProductID == b.ProductID && a.Date < b.Date)
	})
	var names, warnings []string
	for _, supplier := range imp.data.Suppliers {
		names = append(names, supplier.Name)
		if supplier.ID == "iek" {
			warnings = append(warnings, "IEK: месячные остатки — начальные балансы; подтвердите актуальный снимок.")
		}
	}
	warnings = append(warnings, "Сезонность взята из предоставленных таблиц; проверьте неполные и прогнозные месяцы.")
	mismatches := 0
	for key, expected := range imp.monthly {
		if key.date >= imp.options.HistoryStart[:7] && key.date <= imp.options.AsOf[:7] && math.Abs(expected-imp.monthlyDaily[key]) > 1e-8 {
			mismatches++
		}
	}
	if mismatches > 0 {
		warnings = append(warnings, fmt.Sprintf("Расхождения месячных отчётов и операций: %d пар товар/месяц. Для спроса используются операции; проверьте охват складов в исходных файлах.", mismatches))
	}
	warnings = append(warnings, "Подтвердите сроки новых заказов у поставщиков. Даты товаров в пути не задают срок нового заказа.")
	imp.data.Source = planning.SourceInfo{Label: strings.Join(names, " / ") + " · Алматы", AsOf: imp.options.AsOf, HistoryStart: imp.options.HistoryStart, HistoryEnd: imp.options.AsOf, Warnings: warnings}
	if len(imp.data.Products) == 0 {
		return planning.Dataset{}, fmt.Errorf("no products found in uploaded workbooks")
	}
	if err := imp.ctx.Err(); err != nil {
		return planning.Dataset{}, err
	}
	if err := imp.data.Validate(); err != nil {
		return planning.Dataset{}, fmt.Errorf("converted workbook data: %w", err)
	}
	return imp.data, nil
}
