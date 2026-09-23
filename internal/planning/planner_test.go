package planning

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func fixture() Dataset {
	d := EmptyDataset()
	d.Suppliers = []Supplier{{ID: "supplier", Name: "Supplier", LeadTimeDays: 3}}
	d.Products = []Product{{ID: "p", SKU: "SKU", Name: "Cable", SupplierID: "supplier", PackSize: 6, MinOrderQuantity: 10}}
	d.Stock = []Stock{{ProductID: "p", OnHand: 12, Reserved: 2}}
	for _, date := range []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-05", "2026-09-06", "2026-09-07"} {
		d.Sales = append(d.Sales, Sale{ProductID: "p", Date: date, Quantity: 10})
	}
	return d
}

func TestCalculation(t *testing.T) {
	request := Request{AsOf: "2026-09-08", LookbackDays: 7, ReviewPeriodDays: 2, SafetyStockDays: 1}
	tests := []struct {
		name        string
		mutate      func(*Dataset)
		demand      float64
		quantity    int64
		adjustments int
	}{
		{"regular demand", func(d *Dataset) {}, 10, 54, 0},
		{"one-off spike replaced by baseline", func(d *Dataset) { d.Sales[3].Quantity = 1000 }, 10, 54, 1},
		{"covered by stock", func(d *Dataset) { d.Stock[0].OnHand = 100 }, 10, 0, 0},
		{"incoming covers stock", func(d *Dataset) { d.Shipments = []Shipment{{"s", "p", 50, "2026-09-12"}} }, 10, 0, 0},
		{"late incoming excluded", func(d *Dataset) { d.Shipments = []Shipment{{"s", "p", 50, "2026-09-15"}} }, 10, 54, 0},
		{"overdue incoming excluded", func(d *Dataset) { d.Shipments = []Shipment{{"s", "p", 50, "2026-09-07"}} }, 10, 54, 0},
		{"minimum rounded to pack", func(d *Dataset) { d.Stock[0].OnHand = 61 }, 10, 12, 0},
		{"no history no order", func(d *Dataset) { d.Sales = []Sale{} }, 0, 0, 0},
		{"zero days included", func(d *Dataset) { d.Sales = d.Sales[:1] }, 10.0 / 7, 0, 0},
		{"sparse demand not discarded", func(d *Dataset) { d.Sales = d.Sales[:1]; d.Sales[0].Quantity = 70; d.Stock[0].OnHand = 2 }, 10, 60, 0},
		{"manual exclusion", func(d *Dataset) { d.Sales[0].ExcludeFromDemand = true }, 60.0 / 7, 42, 1},
		{"current day excluded", func(d *Dataset) { d.Sales = append(d.Sales, Sale{ProductID: "p", Date: "2026-09-08", Quantity: 1000}) }, 10, 54, 0},
		{"old and future sales excluded", func(d *Dataset) {
			d.Sales = append(d.Sales, Sale{ProductID: "p", Date: "2026-08-31", Quantity: 1000}, Sale{ProductID: "p", Date: "2026-09-09", Quantity: 1000})
		}, 10, 54, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture()
			tc.mutate(&d)
			result, err := Calculate(context.Background(), d, request)
			if err != nil {
				t.Fatal(err)
			}
			line := result.Products[0]
			if math.Abs(line.DailyDemand-tc.demand) > 1e-9 || line.OrderQuantity != tc.quantity || len(line.Adjustments) != tc.adjustments {
				t.Fatalf("got %+v; want demand %v, quantity %d, adjustments %d", line, tc.demand, tc.quantity, tc.adjustments)
			}
			if tc.quantity == 0 && len(result.Orders) != 0 {
				t.Fatal("zero-quantity order generated")
			}
			if tc.quantity > 0 && (len(result.Orders) != 1 || result.Orders[0].TotalQuantity != tc.quantity || result.Orders[0].ExpectedDate != "2026-09-11") {
				t.Fatalf("unexpected orders: %+v", result.Orders)
			}
		})
	}
}

func TestShipmentsAndWarnings(t *testing.T) {
	d := fixture()
	d.Shipments = []Shipment{{"today", "p", 2, "2026-09-08"}, {"boundary", "p", 3, "2026-09-14"}, {"late", "p", 100, "2026-09-15"}, {"overdue", "p", 7, "2026-09-07"}}
	r, err := Calculate(context.Background(), d, Request{"2026-09-08", 7, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	l := r.Products[0]
	if l.IncomingQuantity != 5 || l.OverdueQuantity != 7 || l.OrderQuantity != 48 {
		t.Fatalf("wrong shipment treatment: %+v", l)
	}
	if !reflect.DeepEqual(l.Warnings, []string{"overdue_shipments_excluded", "insufficient_supply_during_lead_time"}) {
		t.Fatalf("warnings: %v", l.Warnings)
	}
}

func TestSupplierGroupingAndDeterminism(t *testing.T) {
	d := fixture()
	d.Products = append(d.Products, Product{"a", "SKU2", "Switch", "supplier", 1, 0})
	d.Stock = append(d.Stock, Stock{ProductID: "a"})
	d.Sales = append(d.Sales, Sale{ProductID: "a", Date: "2026-09-01", Quantity: 7})
	r, err := Calculate(context.Background(), d, Request{"2026-09-08", 7, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || len(r.Orders[0].Lines) != 2 || r.Orders[0].Lines[0].ProductID != "a" || r.Orders[0].TotalQuantity != 60 {
		t.Fatalf("grouping: %+v", r.Orders)
	}
	if d.Products[0].ID != "p" {
		t.Fatal("calculation mutated input")
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dataset)
	}{
		{"missing collection", func(d *Dataset) { d.Sales = nil }},
		{"unknown supplier", func(d *Dataset) { d.Products[0].SupplierID = "unknown" }},
		{"missing stock", func(d *Dataset) { d.Stock = []Stock{} }},
		{"duplicate product", func(d *Dataset) { d.Products = append(d.Products, d.Products[0]) }},
		{"duplicate day", func(d *Dataset) { d.Sales = append(d.Sales, d.Sales[0]) }},
		{"negative sale", func(d *Dataset) { d.Sales[0].Quantity = -1 }},
		{"bad date", func(d *Dataset) { d.Sales[0].Date = "2026-02-30" }},
		{"unrecognized product", func(d *Dataset) { d.Sales[0].ProductID = "missing" }},
		{"invalid pack", func(d *Dataset) { d.Products[0].PackSize = 0 }},
		{"invalid reserved", func(d *Dataset) { d.Stock[0].Reserved = 100 }},
		{"overflow quantity", func(d *Dataset) { d.Sales[0].Quantity = MaxQuantity + 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture()
			tc.mutate(&d)
			if d.Validate() == nil {
				t.Fatal("accepted invalid data")
			}
		})
	}
	for _, r := range []Request{{"bad", 7, 2, 0}, {"2026-09-08", 0, 2, 0}, {"2026-09-08", 7, 0, 0}, {"2026-09-08", 7, 2, -1}} {
		if r.Validate() == nil {
			t.Fatalf("accepted invalid request %+v", r)
		}
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Calculate(ctx, fixture(), Request{"2026-09-08", 7, 2, 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
