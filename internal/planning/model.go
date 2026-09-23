package planning

import (
	"fmt"
	"strings"
	"time"
)

const DateLayout = "2006-01-02"
const MaxQuantity int64 = 1_000_000_000

type Supplier struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	LeadTimeDays int    `json:"lead_time_days"`
}

type Product struct {
	ID               string `json:"id"`
	SKU              string `json:"sku"`
	Name             string `json:"name"`
	SupplierID       string `json:"supplier_id"`
	PackSize         int64  `json:"pack_size"`
	MinOrderQuantity int64  `json:"min_order_quantity"`
}

type Sale struct {
	ProductID         string `json:"product_id"`
	Date              string `json:"date"`
	Quantity          int64  `json:"quantity"`
	ExcludeFromDemand bool   `json:"exclude_from_demand"`
}

type Stock struct {
	ProductID string `json:"product_id"`
	OnHand    int64  `json:"on_hand"`
	Reserved  int64  `json:"reserved"`
}

type Shipment struct {
	ID           string `json:"id"`
	ProductID    string `json:"product_id"`
	Quantity     int64  `json:"quantity"`
	ExpectedDate string `json:"expected_date"`
}

// Dataset is a complete snapshot for one warehouse. Shipments contain only open,
// unreceived quantities; receipt must update stock and shipments in one import.
type Dataset struct {
	Suppliers []Supplier `json:"suppliers"`
	Products  []Product  `json:"products"`
	Sales     []Sale     `json:"sales"`
	Stock     []Stock    `json:"stock"`
	Shipments []Shipment `json:"shipments"`
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

func validText(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 200 }

func (d Dataset) Validate() error {
	if d.Suppliers == nil || d.Products == nil || d.Sales == nil || d.Stock == nil || d.Shipments == nil {
		return fmt.Errorf("suppliers, products, sales, stock, and shipments must all be arrays (use [] for empty collections)")
	}
	suppliers := make(map[string]bool, len(d.Suppliers))
	for _, s := range d.Suppliers {
		if !validText(s.ID) || !validText(s.Name) || suppliers[s.ID] || s.LeadTimeDays < 0 || s.LeadTimeDays > 365 {
			return fmt.Errorf("invalid or duplicate supplier %q (lead_time_days must be 0..365)", s.ID)
		}
		suppliers[s.ID] = true
	}
	products, skus := map[string]bool{}, map[string]bool{}
	for _, p := range d.Products {
		if !validText(p.ID) || !validText(p.Name) || !validText(p.SKU) || products[p.ID] || skus[p.SKU] || !suppliers[p.SupplierID] {
			return fmt.Errorf("invalid or duplicate product %q, SKU, or supplier reference", p.ID)
		}
		if p.PackSize < 1 || p.PackSize > MaxQuantity || p.MinOrderQuantity < 0 || p.MinOrderQuantity > MaxQuantity {
			return fmt.Errorf("product %q requires pack_size 1..%d and min_order_quantity 0..%d", p.ID, MaxQuantity, MaxQuantity)
		}
		products[p.ID], skus[p.SKU] = true, true
	}
	stock := map[string]bool{}
	for _, s := range d.Stock {
		if !products[s.ProductID] || stock[s.ProductID] || s.OnHand < 0 || s.OnHand > MaxQuantity || s.Reserved < 0 || s.Reserved > s.OnHand {
			return fmt.Errorf("invalid or duplicate stock for %q; reserved must be between zero and on_hand", s.ProductID)
		}
		stock[s.ProductID] = true
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
		if !products[s.ProductID] || sales[key] || s.Quantity < 0 || s.Quantity > MaxQuantity {
			return fmt.Errorf("invalid or duplicate daily sale for %q on %q", s.ProductID, s.Date)
		}
		if _, err := ParseDate(s.Date); err != nil {
			return fmt.Errorf("sale: %w", err)
		}
		sales[key] = true
	}
	shipments := map[string]bool{}
	for _, s := range d.Shipments {
		if !validText(s.ID) || shipments[s.ID] || !products[s.ProductID] || s.Quantity < 1 || s.Quantity > MaxQuantity {
			return fmt.Errorf("invalid or duplicate shipment %q", s.ID)
		}
		if _, err := ParseDate(s.ExpectedDate); err != nil {
			return fmt.Errorf("shipment: %w", err)
		}
		shipments[s.ID] = true
	}
	return nil
}
