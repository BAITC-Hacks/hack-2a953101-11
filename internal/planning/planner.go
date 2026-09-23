package planning

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type Request struct {
	AsOf             string `json:"as_of"`
	LookbackDays     int    `json:"lookback_days"`
	ReviewPeriodDays int    `json:"review_period_days"`
	SafetyStockDays  int    `json:"safety_stock_days"`
}

func (r Request) Validate() error {
	if _, err := ParseDate(r.AsOf); err != nil {
		return err
	}
	if r.LookbackDays < 7 || r.LookbackDays > 730 {
		return fmt.Errorf("lookback_days must be 7..730")
	}
	if r.ReviewPeriodDays < 1 || r.ReviewPeriodDays > 365 {
		return fmt.Errorf("review_period_days must be 1..365")
	}
	if r.SafetyStockDays < 0 || r.SafetyStockDays > 365 {
		return fmt.Errorf("safety_stock_days must be 0..365")
	}
	return nil
}

type Adjustment struct {
	Date             string  `json:"date"`
	OriginalQuantity float64 `json:"original_quantity"`
	UsedQuantity     float64 `json:"used_quantity"`
	Reason           string  `json:"reason"`
}

type HistoryPoint struct {
	Date           string   `json:"date"`
	Raw            float64  `json:"raw"`
	AfterSpikes    float64  `json:"after_spikes"`
	AfterStockouts float64  `json:"after_stockouts"`
	Stock          *float64 `json:"stock"`
	Seasonality    float64  `json:"seasonality"`
}
type DemandAudit struct {
	History             []HistoryPoint `json:"history"`
	HistoryDays         int            `json:"history_days"`
	RawTotal            float64        `json:"raw_total"`
	AfterSpikesTotal    float64        `json:"after_spikes_total"`
	AfterStockoutsTotal float64        `json:"after_stockouts_total"`
	BaseDailyDemand     float64        `json:"base_daily_demand"`
	TrendDailyDemand    float64        `json:"trend_daily_demand"`
}
type Line struct {
	Audit *DemandAudit `json:"audit,omitempty"`

	ForecastDemand    float64 `json:"forecast_demand"`
	SafetyStock       float64 `json:"safety_stock"`
	TrendFactor       float64 `json:"trend_factor"`
	SeasonalityFactor float64 `json:"seasonality_factor"`
	Explanation       string  `json:"explanation"`

	ProductID           string       `json:"product_id"`
	SKU                 string       `json:"sku"`
	Name                string       `json:"name"`
	SupplierID          string       `json:"supplier_id"`
	RawDemand           float64      `json:"raw_daily_demand"`
	DailyDemand         float64      `json:"daily_demand"`
	CoverageDays        int          `json:"coverage_days"`
	CoverageEnd         string       `json:"coverage_end"`
	AvailableStock      float64      `json:"available_stock"`
	IncomingQuantity    float64      `json:"incoming_quantity"`
	OverdueQuantity     float64      `json:"overdue_quantity"`
	TargetStock         float64      `json:"target_stock"`
	NetRequirement      float64      `json:"net_requirement"`
	OrderQuantity       float64      `json:"order_quantity"`
	Adjustments         []Adjustment `json:"adjustments"`
	Warnings            []string     `json:"warnings"`
	InternalCode        string       `json:"internal_code,omitempty"`
	Unit                string       `json:"unit,omitempty"`
	ForecastDailyDemand float64      `json:"forecast_daily_demand"`
	SeasonalFactor      float64      `json:"seasonal_factor"`
	Blocked             bool         `json:"blocked"`
	SuggestedQuantity   float64      `json:"suggested_quantity"`
}

type Order struct {
	SupplierID    string  `json:"supplier_id"`
	SupplierName  string  `json:"supplier_name"`
	ExpectedDate  string  `json:"expected_date"`
	TotalQuantity float64 `json:"total_quantity"`
	Lines         []Line  `json:"lines"`
}

type Result struct {
	Parameters          Request `json:"parameters"`
	HistoryStart        string  `json:"history_start"`
	HistoryEndExclusive string  `json:"history_end_exclusive"`
	Orders              []Order `json:"orders"`
	Products            []Line  `json:"products"`
}

func median(values []float64) float64 {
	sort.Float64s(values)
	n := len(values)
	if n%2 == 1 {
		return values[n/2]
	}
	return (values[n/2-1] + values[n/2]) / 2
}

// Calculate uses complete calendar days before AsOf. Zero-sale days contribute
// to the denominator. A spike requires at least four positive, unexcluded days;
// otherwise there is insufficient evidence to automatically discard demand.
func Calculate(ctx context.Context, d Dataset, r Request) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	if err := d.Validate(); err != nil {
		return Result{}, err
	}
	asOf, _ := ParseDate(r.AsOf)
	start := asOf.AddDate(0, 0, -r.LookbackDays).Format(DateLayout)
	historyEnd := r.AsOf
	if d.Source.HistoryStart > start {
		start = d.Source.HistoryStart
	}
	if d.Source.HistoryEnd != "" {
		last, _ := ParseDate(d.Source.HistoryEnd)
		if last.AddDate(0, 0, 1).Format(DateLayout) < historyEnd {
			historyEnd = last.AddDate(0, 0, 1).Format(DateLayout)
		}
	}
	historyStartTime, _ := ParseDate(start)
	historyEndTime, _ := ParseDate(historyEnd)
	historyDays := max(0, int(historyEndTime.Sub(historyStartTime).Hours()/24))
	out := Result{Parameters: r, HistoryStart: start, HistoryEndExclusive: historyEnd, Orders: []Order{}, Products: []Line{}}
	sales := map[string][]Sale{}
	for _, s := range d.Sales {
		if s.Date >= start && s.Date < historyEnd {
			sales[s.ProductID] = append(sales[s.ProductID], s)
		}
	}
	stock := map[string]Stock{}
	for _, s := range d.Stock {
		stock[s.ProductID] = s
	}
	shipments := map[string][]Shipment{}
	for _, s := range d.Shipments {
		shipments[s.ProductID] = append(shipments[s.ProductID], s)
	}
	suppliers := map[string]Supplier{}
	for _, s := range d.Suppliers {
		suppliers[s.ID] = s
	}
	monthly := map[string][]Monthly{}
	for _, m := range d.Monthly {
		monthly[m.ProductID] = append(monthly[m.ProductID], m)
	}
	seasonal := map[string][12]float64{}
	for _, supplier := range d.Suppliers {
		var f [12]float64
		for i := range f {
			f[i] = 1
		}
		seasonal[supplier.ID] = f
	}
	for _, s := range d.Seasonality {
		f := seasonal[s.SupplierID]
		f[s.Month-1] = s.Factor
		seasonal[s.SupplierID] = f
	}
	orders := map[string]*Order{}
	products := append([]Product(nil), d.Products...)
	sort.Slice(products, func(i, j int) bool { return products[i].ID < products[j].ID })
	for _, p := range products {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		supplier := suppliers[p.SupplierID]
		coverage := supplier.LeadTimeDays + r.ReviewPeriodDays + r.SafetyStockDays
		end := asOf.AddDate(0, 0, coverage).Format(DateLayout)
		line := Line{ProductID: p.ID, SKU: p.SKU, Name: p.Name, SupplierID: p.SupplierID, CoverageDays: coverage, CoverageEnd: end,
			AvailableStock: stock[p.ID].OnHand - stock[p.ID].Reserved, Adjustments: []Adjustment{}, Warnings: []string{}}
		line.InternalCode, line.Unit = p.InternalCode, p.Unit
		line.SeasonalFactor = 1
		block := func(reason string) { line.Blocked = true; line.Warnings = append(line.Warnings, reason) }
		for _, reason := range p.ReviewReasons {
			block(reason)
		}
		if supplier.LeadTimeUnconfirmed {
			block("lead_time_unconfirmed")
		}
		if stock[p.ID].Unverified {
			block("stock_unverified")
		}
		if stamp := stock[p.ID].AsOf; stamp != "" && (stamp < asOf.AddDate(0, 0, -1).Format(DateLayout) || stamp > r.AsOf) {
			block("stock_snapshot_outdated")
		}
		if historyDays == 0 {
			block("history_unavailable")
		}
		if historyDays < r.LookbackDays {
			line.Warnings = append(line.Warnings, "incomplete_sales_window")
		}
		days := sales[p.ID]
		sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
		positive := []float64{}
		for _, s := range days {
			if s.Quantity > 0 && !s.ExcludeFromDemand {
				positive = append(positive, float64(s.Quantity))
			}
		}
		baseline, threshold := 0.0, math.Inf(1)
		if len(positive) >= 4 {
			baseline = median(positive)
			deviations := make([]float64, len(positive))
			for i, v := range positive {
				deviations[i] = math.Abs(v - baseline)
			}
			threshold = math.Max(3*baseline, baseline+3*1.4826*median(deviations))
		} else if len(d.Monthly) == 0 {
			line.Warnings = append(line.Warnings, "insufficient_positive_days_for_spike_detection")
		}
		var raw, adjusted, deseasonalized float64
		for _, s := range days {
			quantity := float64(s.Quantity)
			raw += quantity
			reason := ""
			if quantity < 0 {
				quantity, reason = 0, "net_returns"
			} else if s.ExcludeFromDemand {
				quantity, reason = 0, "manual_exclusion"
			} else if quantity > threshold {
				quantity, reason = baseline, "sales_spike"
			}
			adjusted += quantity
			factor := 1.0
			if len(supplier.Seasonality) > 0 {
				day, _ := ParseDate(s.Date)
				factor = supplier.Seasonality[int(day.Month())-1]
			}
			deseasonalized += quantity / factor
			if reason != "" {
				line.Adjustments = append(line.Adjustments, Adjustment{s.Date, s.Quantity, quantity, reason})
			}
		}
		denominator := float64(max(1, historyDays))
		line.RawDemand, line.DailyDemand = raw/denominator, adjusted/denominator
		line.ForecastDailyDemand = line.DailyDemand
		if len(supplier.Seasonality) > 0 {
			factorSum := 0.0
			for day := 0; day < coverage; day++ {
				factorSum += supplier.Seasonality[int(asOf.AddDate(0, 0, day).Month())-1]
			}
			line.ForecastDailyDemand = deseasonalized / denominator * factorSum / float64(coverage)
			if line.DailyDemand > 0 {
				line.SeasonalFactor = line.ForecastDailyDemand / line.DailyDemand
			}
		}
		if len(d.Monthly) > 0 {
			// Monthly totals are authoritative; daily adjustments must not be
			// reported again alongside the monthly audit.
			line.Adjustments = []Adjustment{}
			monthlyDemand(monthly[p.ID], seasonal[p.SupplierID], asOf, r, &line)
			line.ForecastDailyDemand = line.DailyDemand
			line.SeasonalFactor = line.SeasonalityFactor
			if p.Notes != "" {
				line.Warnings = append(line.Warnings, p.Notes)
			}
			if p.StockDate != "" && p.StockDate != r.AsOf {
				line.Warnings = append(line.Warnings, "Остаток датирован "+p.StockDate+"; выбранная дата не восстанавливает движение склада.")
			}
		} else {
			line.ForecastDemand = line.ForecastDailyDemand * float64(coverage-r.SafetyStockDays)
			line.SafetyStock = line.ForecastDailyDemand * float64(r.SafetyStockDays)
			line.TrendFactor = 1
			line.SeasonalityFactor = line.SeasonalFactor
		}
		var arrivingDuringLead float64
		arrival := asOf.AddDate(0, 0, supplier.LeadTimeDays).Format(DateLayout)
		for _, s := range shipments[p.ID] {
			if s.ExpectedDate < r.AsOf {
				line.OverdueQuantity += s.Quantity
			} else if s.ExpectedDate <= end {
				line.IncomingQuantity += s.Quantity
				if s.ExpectedDate <= arrival {
					arrivingDuringLead += s.Quantity
				}
			}
		}
		if line.OverdueQuantity > 0 {
			line.Warnings = append(line.Warnings, "overdue_shipments_excluded")
		}
		if float64(line.AvailableStock+arrivingDuringLead) < line.ForecastDailyDemand*float64(supplier.LeadTimeDays) {
			line.Warnings = append(line.Warnings, "insufficient_supply_during_lead_time")
		}
		// Multiply before dividing to avoid rounding an exact integer upward due
		// to floating-point error in the displayed daily average.
		line.TargetStock = math.Ceil(adjusted * float64(coverage) / denominator)
		if len(supplier.Seasonality) > 0 {
			line.TargetStock = math.Ceil(line.ForecastDailyDemand*float64(coverage) - 1e-9)
		}
		if len(d.Monthly) > 0 {
			line.TargetStock = math.Ceil(line.ForecastDemand + line.SafetyStock)
		}
		if line.DailyDemand == 0 {
			line.Warnings = append(line.Warnings, "no_regular_demand")
		}
		line.NetRequirement = max(float64(0), line.TargetStock-line.AvailableStock-line.IncomingQuantity)
		if line.NetRequirement > 0 {
			quantity := max(line.NetRequirement, p.MinOrderQuantity)
			line.OrderQuantity = math.Ceil(quantity/p.PackSize-1e-9) * p.PackSize
			line.OrderQuantity = math.Round(line.OrderQuantity*1e6) / 1e6
			line.SuggestedQuantity = line.OrderQuantity
			if line.Blocked {
				line.OrderQuantity = 0
			}
		}
		line.Explanation = fmt.Sprintf("Рекомендовано: %g ед. Прогноз спроса: %.1f; страховой запас: %.1f; доступный остаток: %g; учтено в пути: %g. Минимум: %g; округление до кратности %g. Сезонный коэффициент горизонта: %.3f; тренд: %.3f.", line.OrderQuantity, line.ForecastDemand, line.SafetyStock, line.AvailableStock, line.IncomingQuantity, p.MinOrderQuantity, p.PackSize, line.SeasonalityFactor, line.TrendFactor)
		if line.Audit != nil {
			a := line.Audit
			line.Explanation = fmt.Sprintf("Продажи за %d дней полных месяцев: %.2f → после всплесков %.2f → после компенсации отсутствия %.2f. Базовый спрос без сезонности: %.4f ед./день; после тренда: %.4f. ", a.HistoryDays, a.RawTotal, a.AfterSpikesTotal, a.AfterStockoutsTotal, a.BaseDailyDemand, a.TrendDailyDemand) + line.Explanation
		}
		spikes, stockouts := 0, 0
		for _, a := range line.Adjustments {
			if a.Reason == "sales_spike" {
				spikes++
			}
			if a.Reason == "stockout_compensation" {
				stockouts++
			}
		}
		if spikes > 0 {
			line.Explanation += fmt.Sprintf(" Всплески скорректированы: %d.", spikes)
		}
		if stockouts > 0 {
			line.Explanation += fmt.Sprintf(" Возможное отсутствие товара: восстановлен спрос за %d мес. по медиане нормальных периодов.", stockouts)
		}
		if p.Notes != "" {
			line.Explanation += " " + p.Notes
		}
		if line.OrderQuantity > 0 {
			order := orders[supplier.ID]
			if order == nil {
				order = &Order{SupplierID: supplier.ID, SupplierName: supplier.Name, ExpectedDate: arrival, Lines: []Line{}}
				orders[supplier.ID] = order
			}
			order.Lines = append(order.Lines, line)
			order.TotalQuantity += line.OrderQuantity
		}
		out.Products = append(out.Products, line)
	}
	for _, order := range orders {
		out.Orders = append(out.Orders, *order)
	}
	sort.Slice(out.Orders, func(i, j int) bool { return out.Orders[i].SupplierID < out.Orders[j].SupplierID })
	return out, nil
}

// DefaultRequest is useful to clients choosing today's UTC planning date.
func DefaultRequest(now time.Time) Request {
	return Request{AsOf: now.UTC().Format(DateLayout), LookbackDays: 90, ReviewPeriodDays: 14, SafetyStockDays: 7}
}
