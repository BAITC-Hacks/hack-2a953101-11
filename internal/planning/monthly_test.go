package planning

import (
	"context"
	"math"
	"testing"
)

func TestMonthlySeasonStockoutSpikeAndRounding(t *testing.T) {
	d := fixture()
	d.Suppliers[0].LeadTimeDays = 14
	d.Products[0].PackSize = 20
	zero, stock := 0., 100.
	for i, date := range []string{"2026-01-01", "2026-02-01", "2026-03-01", "2026-04-01", "2026-05-01", "2026-06-01", "2026-07-01", "2026-08-01"} {
		m := Monthly{ProductID: "p", Date: date, Quantity: 300, Stock: &stock}
		if i == 3 {
			m.Quantity = 3000
		}
		if i == 5 {
			m.Quantity = 0
			m.Stock = &zero
		}
		d.Monthly = append(d.Monthly, m)
	}
	// Partial month must never affect regular demand.
	d.Monthly = append(d.Monthly, Monthly{ProductID: "p", Date: "2026-09-01", Quantity: 999999, Stock: &stock})
	for m := 1; m <= 12; m++ {
		factor := 1.
		if m == 9 || m == 10 {
			factor = 2
		}
		d.Seasonality = append(d.Seasonality, Seasonality{"supplier", m, factor})
	}
	d.Shipments = []Shipment{{ID: "in", ProductID: "p", Quantity: 30, ExpectedDate: "2026-09-25"}}
	r := Request{"2026-09-23", 365, 14, 7}
	result, err := Calculate(context.Background(), d, r)
	if err != nil {
		t.Fatal(err)
	}
	l := result.Products[0]
	if l.IncomingQuantity != 30 || l.OrderQuantity%20 != 0 || l.ForecastDemand < 400 || l.ForecastDemand > 700 || l.Explanation == "" {
		t.Fatalf("bad line: %+v", l)
	}
	found := map[string]bool{}
	for _, a := range l.Adjustments {
		found[a.Reason] = true
	}
	if !found["sales_spike"] || !found["stockout_compensation"] {
		t.Fatalf("missing adjustments: %+v", l)
	}
	if math.Abs(float64(l.TargetStock)-(l.ForecastDemand+l.SafetyStock)) > 1 {
		t.Fatal("forecast does not reconcile")
	}
	d.Shipments = []Shipment{}
	other, err := Calculate(context.Background(), d, r)
	if err != nil {
		t.Fatal(err)
	}
	if other.Products[0].NetRequirement-l.NetRequirement != 30 {
		t.Fatal("transit not subtracted")
	}
}
