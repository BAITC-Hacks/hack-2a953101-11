package realdata

import (
	"context"
	"github.com/electrokomplekt/replenishment/internal/planning"
	"math"
	"testing"
)

func TestSuppliedExcelSnapshot(t *testing.T) {
	d, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Products) < 3000 || len(d.Monthly) < 90000 || len(d.Seasonality) != 24 || len(d.Shipments) == 0 {
		t.Fatalf("incomplete import: products %d monthly %d season %d shipments %d", len(d.Products), len(d.Monthly), len(d.Seasonality), len(d.Shipments))
	}
	var cable *planning.Product
	for i := range d.Products {
		if d.Products[i].ID == "iek:200400085_" {
			cable = &d.Products[i]
		}
	}
	if cable == nil || cable.SKU != "LC1-C5E04-311" {
		t.Fatalf("lost exact 1C/article join: %+v", cable)
	}
	foundStock := false
	for _, s := range d.Stock {
		if s.ProductID == "iek:200400085_" {
			foundStock = true
			if s.OnHand != 9084 {
				t.Fatalf("wrong September stock: %g", s.OnHand)
			}
		}
	}
	if !foundStock {
		t.Fatal("missing cable stock")
	}
	result, err := planning.Calculate(context.Background(), d, planning.Request{AsOf: "2026-09-23", LookbackDays: 365, ReviewPeriodDays: 14, SafetyStockDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Orders) != 2 {
		t.Fatalf("expected both suppliers, got %d", len(result.Orders))
	}
	spikes, stockouts := 0, 0
	for _, l := range result.Products {
		if l.Explanation == "" {
			t.Fatal("missing explanation")
		}
		for _, a := range l.Adjustments {
			if a.Reason == "sales_spike" {
				spikes++
			}
			if a.Reason == "stockout_compensation" {
				stockouts++
			}
		}
	}
	if spikes == 0 || stockouts == 0 {
		t.Fatalf("adjustments missing: %d %d", spikes, stockouts)
	}
	t.Logf("products=%d monthly=%d shipments=%d spikes=%d stockout_months=%d", len(d.Products), len(d.Monthly), len(d.Shipments), spikes, stockouts)
}

// This is the real SKU used in the live demonstration, not a synthetic fixture.
func TestLiveDemoSKUTrace(t *testing.T) {
	d, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	result, err := planning.Calculate(context.Background(), d, planning.Request{AsOf: "2026-09-23", LookbackDays: 365, ReviewPeriodDays: 14, SafetyStockDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range result.Products {
		if l.ProductID != "iek:010400929_" {
			continue
		}
		if l.SKU != "IVR21-1-25" || l.Audit == nil || len(l.Audit.History) != 12 || l.AvailableStock != 0 || l.IncomingQuantity != 5 || l.OrderQuantity != 2 {
			t.Fatalf("demo scenario changed: %+v", l)
		}
		a := l.Audit
		if !(a.AfterSpikesTotal < a.RawTotal && a.AfterStockoutsTotal > a.AfterSpikesTotal) {
			t.Fatalf("missing visible corrections: %+v", a)
		}
		raw, spikes, stockouts := 0., 0., 0.
		for _, h := range a.History {
			raw += h.Raw
			spikes += h.AfterSpikes
			stockouts += h.AfterStockouts
		}
		if math.Abs(raw-a.RawTotal) > 1e-8 || math.Abs(spikes-a.AfterSpikesTotal) > 1e-8 || math.Abs(stockouts-a.AfterStockoutsTotal) > 1e-8 {
			t.Fatal("history does not reconcile with totals")
		}
		if math.Ceil(l.ForecastDemand+l.SafetyStock) != l.TargetStock {
			t.Fatal("forecast does not reconcile")
		}
		t.Logf("raw %.4f -> filtered %.4f -> compensated %.4f; base %.6f trend %.6f season %.6f forecast %.6f safety %.6f target %g stock %g transit %g order %g", a.RawTotal, a.AfterSpikesTotal, a.AfterStockoutsTotal, a.BaseDailyDemand, l.TrendFactor, l.SeasonalityFactor, l.ForecastDemand, l.SafetyStock, l.TargetStock, l.AvailableStock, l.IncomingQuantity, l.OrderQuantity)
		return
	}
	t.Fatal("real demo SKU missing")
}
