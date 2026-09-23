package planning

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// monthlyDemand uses only completed months; partial September exports never
// become a full month's zero/low demand. Document spikes only reduce (never add
// to) the authoritative monthly quantities.
func monthlyDemand(rows []Monthly, factors [12]float64, asOf time.Time, r Request, line *Line) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date < rows[j].Date })
	start := asOf.AddDate(0, 0, -r.LookbackDays)
	line.Audit = &DemandAudit{History: []HistoryPoint{}}
	type observation struct {
		m               Monthly
		rate, raw, days float64
		month           time.Time
	}
	obs := []observation{}
	for _, m := range rows {
		t, _ := ParseDate(m.Date)
		end := t.AddDate(0, 1, 0)
		if end.After(asOf) || !end.After(start) {
			continue
		}
		days := end.Sub(t).Hours() / 24
		factor := factors[int(t.Month())-1]
		if factor <= 0 {
			factor = 1
		}
		q := math.Max(0, m.Quantity-m.SpikeExcess)
		if q < m.Quantity {
			line.Adjustments = append(line.Adjustments, Adjustment{m.Date, m.Quantity, q, "sales_spike"})
		}
		obs = append(obs, observation{m, q / days / factor, m.Quantity / days, days, t})
	}
	if len(obs) == 0 {
		line.DailyDemand = 0
		line.RawDemand = 0
		line.Warnings = append(line.Warnings, "Нет полных месяцев продаж в выбранном окне.")
		return
	}
	vals := []float64{}
	for _, o := range obs {
		if o.rate > 0 {
			vals = append(vals, o.rate)
		}
	}
	baseline, threshold := 0., math.Inf(1)
	if len(vals) > 0 {
		baseline = median(append([]float64{}, vals...))
	}
	if len(vals) >= 4 {
		dev := []float64{}
		for _, v := range vals {
			dev = append(dev, math.Abs(v-baseline))
		}
		threshold = math.Max(3*baseline, baseline+3*1.4826*median(dev))
	} else {
		line.Warnings = append(line.Warnings, "Мало ненулевых месяцев для уверенного выявления месячных всплесков.")
	}
	normals := []float64{}
	for _, o := range obs {
		if o.rate > 0 && o.rate <= threshold && o.m.Stock != nil && *o.m.Stock > 0 {
			normals = append(normals, o.rate)
		}
	}
	fallback := 0.
	if len(normals) > 0 {
		fallback = median(normals)
	}
	raw, total, days := 0., 0., 0.
	for i := range obs {
		o := &obs[i]
		if o.rate > threshold {
			o.rate = baseline
			line.Adjustments = append(line.Adjustments, Adjustment{o.m.Date, o.m.Quantity, baseline * o.days * factors[int(o.month.Month())-1], "sales_spike"})
		}
		factor := factors[int(o.month.Month())-1]
		point := HistoryPoint{Date: o.m.Date, Raw: o.m.Quantity, AfterSpikes: o.rate * o.days * factor, Stock: o.m.Stock, Seasonality: factor}
		if o.m.Stock != nil && *o.m.Stock == 0 && fallback > 0 && o.rate <= fallback*0.2 {
			o.rate = fallback
			line.Adjustments = append(line.Adjustments, Adjustment{o.m.Date, o.m.Quantity, fallback * o.days * factors[int(o.month.Month())-1], "stockout_compensation"})
		}
		point.AfterStockouts = o.rate * o.days * factor
		line.Audit.History = append(line.Audit.History, point)
		line.Audit.RawTotal += point.Raw
		line.Audit.AfterSpikesTotal += point.AfterSpikes
		line.Audit.AfterStockoutsTotal += point.AfterStockouts
		total += o.rate * o.days
		raw += o.raw * o.days
		days += o.days
	}
	trend := 1.
	if len(obs) >= 6 {
		recent, prior := []float64{}, []float64{}
		for _, o := range obs[len(obs)-3:] {
			recent = append(recent, o.rate)
		}
		for _, o := range obs[len(obs)-6 : len(obs)-3] {
			prior = append(prior, o.rate)
		}
		a, b := median(recent), median(prior)
		if b > 0 {
			trend = math.Max(.75, math.Min(1.25, a/b))
		}
	}
	line.Audit.HistoryDays = int(days)
	line.Audit.BaseDailyDemand = total / days
	rate := total / days * trend
	line.Audit.TrendDailyDemand = rate
	weighted := 0.
	for day := 0; day < line.CoverageDays; day++ {
		f := factors[int(asOf.AddDate(0, 0, day).Month())-1]
		weighted += rate * f
		if day < line.CoverageDays-r.SafetyStockDays {
			line.ForecastDemand += rate * f
		} else {
			line.SafetyStock += rate * f
		}
	}
	line.RawDemand = raw / days
	line.DailyDemand = weighted / float64(line.CoverageDays)
	line.TrendFactor = trend
	line.SeasonalityFactor = 1.
	if rate > 0 {
		line.SeasonalityFactor = line.DailyDemand / rate
	}
	line.Warnings = append(line.Warnings, fmt.Sprintf("Использовано полных месяцев: %d. Пустые ячейки сводных таблиц приняты за 0; отсутствие строки остатка не считается подтверждённым дефицитом.", len(obs)))
}
