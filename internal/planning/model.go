package planning

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const DateLayout = "2006-01-02"
const MaxQuantity float64 = 1_000_000_000

type Supplier struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	LeadTimeDays        int       `json:"lead_time_days"`
	LeadTimeUnconfirmed bool      `json:"lead_time_unconfirmed,omitempty"`
	Seasonality         []float64 `json:"seasonality,omitempty"`
}

type Product struct {
	ID               string   `json:"id"`
	SKU              string   `json:"sku"`
	Name             string   `json:"name"`
	SupplierID       string   `json:"supplier_id"`
	PackSize         float64  `json:"pack_size"`
	MinOrderQuantity float64  `json:"min_order_quantity"`
	InternalCode     string   `json:"internal_code,omitempty"`
	Unit             string   `json:"unit,omitempty"`
	ReviewReasons    []string `json:"review_reasons,omitempty"`
}

type Sale struct {
	ProductID         string  `json:"product_id"`
	Date              string  `json:"date"`
	Quantity          float64 `json:"quantity"`
	ExcludeFromDemand bool    `json:"exclude_from_demand"`
}

type Stock struct {
	ProductID  string  `json:"product_id"`
	OnHand     float64 `json:"on_hand"`
	Reserved   float64 `json:"reserved"`
	AsOf       string  `json:"as_of,omitempty"`
	Unverified bool    `json:"unverified,omitempty"`
}

type Shipment struct {
	ID           string  `json:"id"`
	ProductID    string  `json:"product_id"`
	Quantity     float64 `json:"quantity"`
	ExpectedDate string  `json:"expected_date"`
}

// Dataset is a complete snapshot for one warehouse. Shipments contain only open,
// unreceived quantities; receipt must update stock and shipments in one import.
type Dataset struct {
	Suppliers []Supplier `json:"suppliers"`
	Products  []Product  `json:"products"`
	Sales     []Sale     `json:"sales"`
	Stock     []Stock    `json:"stock"`
	Shipments []Shipment `json:"shipments"`
	Source    SourceInfo `json:"source,omitempty"`
}

type SourceInfo struct {
	Label        string   `json:"label,omitempty"`
	AsOf         string   `json:"as_of,omitempty"`
	HistoryStart string   `json:"history_start,omitempty"`
	HistoryEnd   string   `json:"history_end,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

func finiteQuantity(q float64) bool {
	return !math.IsNaN(q) && !math.IsInf(q, 0) && math.Abs(q) <= MaxQuantity
}

func EmptyDataset() Dataset {
	return Dataset{Suppliers: []Supplier{}, Products: []Product{}, Sales: []Sale{}, Stock: []Stock{}, Shipments: []Shipment{}}
}

func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil || t.Format(DateLayout) != s {
		return time.Time{}, fmt.Errorf("invalid date %q; expected YYYY-MM-DD", s)
	}
	return t, nil
}

func validText(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 2048 }

func (d Dataset) Validate() error {
	for _, date := range []string{d.Source.AsOf, d.Source.HistoryStart, d.Source.HistoryEnd} {
		if date != "" {
			if _, err := ParseDate(date); err != nil {
				return fmt.Errorf("source: %w", err)
			}
		}
	}
	if (d.Source.HistoryStart == "") != (d.Source.HistoryEnd == "") || d.Source.HistoryStart > d.Source.HistoryEnd {
		return fmt.Errorf("source history_start and history_end must define a valid interval")
	}
	if d.Suppliers == nil || d.Products == nil || d.Sales == nil || d.Stock == nil || d.Shipments == nil {
		return fmt.Errorf("suppliers, products, sales, stock, and shipments must all be arrays (use [] for empty collections)")
	}
	suppliers := make(map[string]bool, len(d.Suppliers))
	for _, s := range d.Suppliers {
		if !validText(s.ID) || !validText(s.Name) || suppliers[s.ID] || s.LeadTimeDays < 0 || s.LeadTimeDays > 365 {
			return fmt.Errorf("invalid or duplicate supplier %q (lead_time_days must be 0..365)", s.ID)
		}
		suppliers[s.ID] = true
		if len(s.Seasonality) > 0 {
			if len(s.Seasonality) != 12 {
				return fmt.Errorf("supplier %q requires exactly 12 seasonal factors", s.ID)
			}
			for _, factor := range s.Seasonality {
				if !finiteQuantity(factor) || factor <= 0 || factor > 10 {
					return fmt.Errorf("supplier %q requires 12 positive seasonal factors <= 10", s.ID)
				}
			}
		}
	}
	products := map[string]bool{}
	for _, p := range d.Products {
		if !validText(p.ID) || !validText(p.Name) || !validText(p.SKU) || products[p.ID] || !suppliers[p.SupplierID] {
			return fmt.Errorf("invalid or duplicate product %q, SKU, or supplier reference", p.ID)
		}
		if !finiteQuantity(p.PackSize) || !finiteQuantity(p.MinOrderQuantity) || p.PackSize <= 0 || p.MinOrderQuantity < 0 {
			return fmt.Errorf("product %q requires pack_size 0..%g and min_order_quantity 0..%g", p.ID, MaxQuantity, MaxQuantity)
		}
		products[p.ID] = true
	}
	stock := map[string]bool{}
	for _, s := range d.Stock {
		if !products[s.ProductID] || stock[s.ProductID] || !finiteQuantity(s.OnHand) || !finiteQuantity(s.Reserved) || s.OnHand < 0 || s.Reserved < 0 || s.Reserved > s.OnHand {
			return fmt.Errorf("invalid or duplicate stock for %q; reserved must be between zero and on_hand", s.ProductID)
		}
		stock[s.ProductID] = true
		if s.AsOf != "" {
			if _, err := ParseDate(s.AsOf); err != nil {
				return fmt.Errorf("stock: %w", err)
			}
		}
	}
	for _, p := range d.Products {
		if !stock[p.ID] {
			return fmt.Errorf("missing stock for product %q", p.ID)
		}
	}
	type saleKey struct{ product, date string }
	sales := map[saleKey]bool{}
	for _, s := range d.Sales {
		key := saleKey{s.ProductID, s.Date}
		if !products[s.ProductID] || sales[key] || !finiteQuantity(s.Quantity) {
			return fmt.Errorf("invalid or duplicate daily sale for %q on %q", s.ProductID, s.Date)
		}
		if _, err := ParseDate(s.Date); err != nil {
			return fmt.Errorf("sale: %w", err)
		}
		sales[key] = true
	}
	shipments := map[string]bool{}
	for _, s := range d.Shipments {
		if !validText(s.ID) || shipments[s.ID] || !products[s.ProductID] || !finiteQuantity(s.Quantity) || s.Quantity <= 0 {
			return fmt.Errorf("invalid or duplicate shipment %q", s.ID)
		}
		if _, err := ParseDate(s.ExpectedDate); err != nil {
			return fmt.Errorf("shipment: %w", err)
		}
		shipments[s.ID] = true
	}
	return nil
}
