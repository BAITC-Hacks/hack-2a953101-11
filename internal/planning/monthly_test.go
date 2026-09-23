package planning

import (
	"context"
	"math"
	"slices"
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
	if l.IncomingQuantity != 30 || math.Mod(l.OrderQuantity, 20) != 0 || l.ForecastDemand < 400 || l.ForecastDemand > 700 || l.Explanation == "" {
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

func TestMonthlyForecastRetainsSupplierChecksAndFractionalStock(t *testing.T) {
	d := fixture()
	d.Sales[0].Quantity = 1000 // Daily spikes must not duplicate the monthly audit.
	d.Monthly = []Monthly{{ProductID: "p", Date: "2026-08-01", Quantity: 310}}
	d.Products[0].PackSize = 0.5
	d.Products[0].MinOrderQuantity = 0
	d.Stock[0].OnHand = 10.25
	d.Stock[0].Reserved = 0
	r := Request{"2026-09-08", 60, 2, 1}
	result, err := Calculate(context.Background(), d, r)
	if err != nil {
		t.Fatal(err)
	}
	l := result.Products[0]
	if l.ForecastDemand != 50 || l.ForecastDailyDemand != 10 || l.SafetyStock != 10 || l.TargetStock != 60 || l.OrderQuantity != 50 || len(l.Adjustments) != 0 {
		t.Fatalf("monthly forecast or fractional rounding changed: %+v", l)
	}
	d.Suppliers[0].LeadTimeUnconfirmed = true
	result, err = Calculate(context.Background(), d, r)
	if err != nil {
		t.Fatal(err)
	}
	l = result.Products[0]
	if !l.Blocked || l.OrderQuantity != 0 || l.SuggestedQuantity != 50 || len(result.Orders) != 0 || !slices.Contains(l.Warnings, "lead_time_unconfirmed") {
		t.Fatalf("monthly calculation lost the supplier check: %+v", l)
	}
}
