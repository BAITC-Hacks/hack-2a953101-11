package planning

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

func TestSupplierSeasonalityAcrossMonths(t *testing.T) {
	d := fixture()
	d.Suppliers[0].Seasonality = []float64{1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1}
	for i := range d.Sales {
		d.Sales[i].Date = []string{"2026-08-23", "2026-08-24", "2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28", "2026-08-29"}[i]
	}
	r, err := Calculate(context.Background(), d, Request{"2026-08-30", 7, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	l := r.Products[0]
	if l.DailyDemand != 10 || math.Abs(l.ForecastDemand-100.0/6) > 1e-9 || l.TargetStock != 100 || l.OrderQuantity != 90 {
		t.Fatalf("seasonality: %+v", l)
	}
	// A constant seasonal coefficient must cancel between history and horizon.
	for i := range d.Suppliers[0].Seasonality {
		d.Suppliers[0].Seasonality[i] = 2
	}
	r, err = Calculate(context.Background(), d, Request{"2026-08-30", 7, 2, 1})
	if err != nil || r.Products[0].OrderQuantity != 54 {
		t.Fatalf("constant factor inflated demand: %v %+v", err, r)
	}
}

func TestUnverifiedSupplierDataCannotBecomeOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Dataset)
	}{
		{"lead time", func(d *Dataset) { d.Suppliers[0].LeadTimeUnconfirmed = true }},
		{"stock absent", func(d *Dataset) { d.Stock[0].Unverified = true }},
		{"opening balance", func(d *Dataset) { d.Stock[0].AsOf = "2026-09-01" }},
		{"future stock", func(d *Dataset) { d.Stock[0].AsOf = "2026-09-09" }},
		{"MOQ", func(d *Dataset) { d.Products[0].ReviewReasons = []string{"missing_order_rules"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture()
			tc.change(&d)
			r, err := Calculate(context.Background(), d, Request{"2026-09-08", 7, 2, 1})
			if err != nil {
				t.Fatal(err)
			}
			if !r.Products[0].Blocked || r.Products[0].OrderQuantity != 0 || r.Products[0].SuggestedQuantity != 54 || len(r.Orders) != 0 {
				t.Fatalf("unsafe order: %+v", r)
			}
		})
	}
}

func TestReturnsAndFractionalQuantities(t *testing.T) {
	d := fixture()
	d.Sales[0].Quantity = -5
	r, err := Calculate(context.Background(), d, Request{"2026-09-08", 7, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	l := r.Products[0]
	if l.RawDemand != 55.0/7 || l.DailyDemand != 60.0/7 || len(l.Adjustments) != 1 || l.Adjustments[0].Reason != "net_returns" {
		t.Fatalf("returns: %+v", l)
	}
	d = fixture()
	d.Stock[0].OnHand = 12.4
	d.Stock[0].Reserved = 2.1
	d.Products[0].PackSize = .1
	r, err = Calculate(context.Background(), d, Request{"2026-09-08", 7, 2, 1})
	if err != nil || math.Abs(r.Products[0].OrderQuantity-49.7) > 1e-9 {
		t.Fatalf("fractional quantities lost: %v %+v", err, r)
	}
}

func TestSourceCoverageDoesNotInventZeroSales(t *testing.T) {
	d := fixture()
	d.Source = SourceInfo{HistoryStart: "2026-09-01", HistoryEnd: "2026-09-07"}
	r, err := Calculate(context.Background(), d, Request{"2026-09-15", 14, 2, 1})
	if err != nil || r.Products[0].DailyDemand != 10 || r.HistoryEndExclusive != "2026-09-08" {
		t.Fatalf("incomplete history treated as zero: %v %+v", err, r)
	}
	r, err = Calculate(context.Background(), d, Request{"2026-12-15", 7, 2, 1})
	if err != nil || !r.Products[0].Blocked || len(r.Orders) != 0 {
		t.Fatalf("unavailable history: %v %+v", err, r)
	}
}

func TestActualSupplierImport(t *testing.T) {
	path := os.Getenv("SUPPLIER_IMPORT_FILE")
	if path == "" {
		t.Skip("set SUPPLIER_IMPORT_FILE to verify the supplied workbooks after conversion")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d Dataset
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(d.Products) != 3909 || len(d.Sales) != 140922 || len(d.Shipments) != 313 {
		t.Fatalf("unexpected source counts: %d %d %d", len(d.Products), len(d.Sales), len(d.Shipments))
	}
	for _, s := range d.Stock {
		if s.ProductID == "iek:130200015_" && s.OnHand != 43.2 {
			t.Fatal("fractional stock lost")
		}
	}
	for i := range d.Suppliers {
		d.Suppliers[i].LeadTimeDays = 14
		d.Suppliers[i].LeadTimeUnconfirmed = false
	}
	r, err := Calculate(context.Background(), d, Request{"2026-09-23", 90, 14, 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range r.Orders {
		if order.SupplierID == "iek" {
			t.Fatal("historical IEK stock used for a ready order")
		}
	}
	ready, blocked := 0, 0
	for _, line := range r.Products {
		if line.Blocked {
			blocked++
		}
		if strings.HasPrefix(line.ProductID, "systeme:") && line.OrderQuantity > 0 {
			ready++
		}
	}
	if ready == 0 || blocked == 0 {
		t.Fatalf("expected verified SE recommendations and blocked historical data: ready=%d blocked=%d", ready, blocked)
	}
	t.Logf("verified %d products, %d daily sales, %d incoming lines; %d SE lines ready after explicit test lead times; %d need review", len(d.Products), len(d.Sales), len(d.Shipments), ready, blocked)
}
