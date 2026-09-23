package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/electrokomplekt/replenishment/internal/storage"
)

const sample = `{"suppliers":[{"id":"s","name":"Supplier","lead_time_days":3}],"products":[{"id":"p","sku":"=1+1","name":"Cable","supplier_id":"s","pack_size":6,"min_order_quantity":10}],"sales":[{"product_id":"p","date":"2026-09-01","quantity":70}],"stock":[{"product_id":"p","on_hand":0,"reserved":0}],"shipments":[]}`

func testAPI(t *testing.T, key string) http.Handler {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), key, "http://localhost:3000")
}

func request(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestImportRecommendExport(t *testing.T) {
	h := testAPI(t, "secret")
	headers := map[string]string{"Authorization": "Bearer secret"}
	w := request(h, "GET", "/api/v1/dataset", "", headers)
	if w.Code != 200 || w.Header().Get("ETag") != `"0"` {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	headers["If-Match"] = w.Header().Get("ETag")
	w = request(h, "PUT", "/api/v1/dataset", sample, headers)
	if w.Code != 200 || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	params := `{"as_of":"2026-09-08","lookback_days":7,"review_period_days":2,"safety_stock_days":1}`
	w = request(h, "POST", "/api/v1/recommendations", params, headers)
	if w.Code != 200 {
		t.Fatalf("recommend: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Revision int `json:"dataset_revision"`
		Orders   []struct {
			Total int `json:"total_quantity"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 || len(result.Orders) != 1 || result.Orders[0].Total != 60 {
		t.Fatalf("result: %+v", result)
	}
	w = request(h, "POST", "/api/v1/recommendations.csv", params, headers)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][3] != "'=1+1" || rows[1][5] != "60" {
		t.Fatalf("CSV: %v", rows)
	}
	w = request(h, "PUT", "/api/v1/dataset", sample, headers)
	if w.Code != 412 {
		t.Fatalf("stale update accepted: %d", w.Code)
	}
}

func TestErrors(t *testing.T) {
	h := testAPI(t, "secret")
	tests := []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"auth", "GET", "/api/v1/dataset", "", nil, 401},
		{"public health", "GET", "/healthz", "", nil, 200},
		{"public ready", "GET", "/readyz", "", nil, 200},
		{"missing precondition", "PUT", "/api/v1/dataset", sample, map[string]string{"Authorization": "Bearer secret"}, 428},
		{"bad precondition", "PUT", "/api/v1/dataset", sample, map[string]string{"Authorization": "Bearer secret", "If-Match": "0"}, 400},
		{"missing collections", "PUT", "/api/v1/dataset", `{}`, map[string]string{"Authorization": "Bearer secret", "If-Match": `"0"`}, 422},
		{"unknown field", "POST", "/api/v1/recommendations", `{"typo":1}`, map[string]string{"Authorization": "Bearer secret"}, 400},
		{"malformed", "POST", "/api/v1/recommendations", `{`, map[string]string{"Authorization": "Bearer secret"}, 400},
		{"null body", "POST", "/api/v1/recommendations", `null`, map[string]string{"Authorization": "Bearer secret"}, 400},
		{"trailing document", "POST", "/api/v1/recommendations", `{} {}`, map[string]string{"Authorization": "Bearer secret"}, 400},
		{"invalid date", "POST", "/api/v1/recommendations", `{"as_of":"2026-02-30"}`, map[string]string{"Authorization": "Bearer secret"}, 422},
		{"zero window", "POST", "/api/v1/recommendations", `{"lookback_days":0}`, map[string]string{"Authorization": "Bearer secret"}, 422},
		{"content type", "POST", "/api/v1/recommendations", `{}`, map[string]string{"Authorization": "Bearer secret", "Content-Type": "text/plain"}, 415},
		{"defaults", "POST", "/api/v1/recommendations", `{}`, map[string]string{"Authorization": "Bearer secret"}, 200},
		{"oversized", "POST", "/api/v1/recommendations", `{"as_of":"` + strings.Repeat("x", maxBodyBytes) + `"}`, map[string]string{"Authorization": "Bearer secret"}, 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, tc.method, tc.path, tc.body, tc.headers)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if !json.Valid(w.Body.Bytes()) {
				t.Fatal("response is not JSON")
			}
		})
	}
}

func TestCORS(t *testing.T) {
	h := testAPI(t, "secret")
	w := request(h, "OPTIONS", "/api/v1/dataset", "", map[string]string{"Origin": "http://localhost:3000", "Access-Control-Request-Method": "PUT"})
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	w = request(h, "OPTIONS", "/api/v1/dataset", "", map[string]string{"Origin": "http://untrusted.example"})
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("untrusted origin accepted")
	}
}

func TestDashboardPublicWithProtectedAPI(t *testing.T) {
	h := testAPI(t, "secret")
	for _, path := range []string{"/", "/assets/app.js", "/assets/styles.css"} {
		w := request(h, "GET", path, "", nil)
		if w.Code != 200 {
			t.Fatalf("public frontend %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/dataset", "/api/v1/recommendations", "/assets/../api/v1/dataset"} {
		w := request(h, "GET", path, "", nil)
		if w.Code != 401 {
			t.Fatalf("API auth bypass %s: %d", path, w.Code)
		}
	}
}

func TestCSVFormulaEscaping(t *testing.T) {
	for _, input := range []string{"=SUM(A1:A2)", "+1", "-1", "@cmd", "\tname", "\rname", "  =1", "\n=1"} {
		if !strings.HasPrefix(safeCell(input), "'") {
			t.Errorf("unescaped %q", input)
		}
	}
	if safeCell("SKU-1") != "SKU-1" {
		t.Fatal("normal SKU altered")
	}
}

func TestDocumentedExample(t *testing.T) {
	h := testAPI(t, "")
	dataset, err := os.ReadFile("../../examples/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	params, err := os.ReadFile("../../examples/recommendation.json")
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "PUT", "/api/v1/dataset", string(dataset), map[string]string{"If-Match": `"0"`})
	if w.Code != 200 {
		t.Fatalf("import example: %d %s", w.Code, w.Body.String())
	}
	w = request(h, "POST", "/api/v1/recommendations", string(params), nil)
	if w.Code != 200 {
		t.Fatalf("calculate example: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Products []struct {
			ID       string  `json:"product_id"`
			Quantity int64   `json:"order_quantity"`
			Demand   float64 `json:"daily_demand"`
		} `json:"products"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	expected := map[string]int64{"cable": 220, "breaker": 0, "lamp": 100}
	if len(result.Products) != len(expected) {
		t.Fatalf("products: %+v", result.Products)
	}
	for _, p := range result.Products {
		if p.Quantity != expected[p.ID] {
			t.Errorf("%s quantity=%d, want %d", p.ID, p.Quantity, expected[p.ID])
		}
		if p.ID == "cable" && p.Demand != 20 {
			t.Errorf("cable demand=%v, want 20", p.Demand)
		}
	}
}
