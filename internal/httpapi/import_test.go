package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/electrokomplekt/replenishment/internal/planning"
)

type uploadPart struct {
	field, filename, value string
}

func uploadRequest(t *testing.T, parts []uploadPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		var destination io.Writer
		var err error
		if part.filename == "" {
			destination, err = writer.CreateFormField(part.field)
		} else {
			destination, err = writer.CreateFormFile(part.field, part.filename)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(destination, part.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/import/xlsx", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Authorization", "Bearer secret")
	return r
}

func TestExcelUploadErrorsDoNotModifyData(t *testing.T) {
	h := testAPI(t, "secret")
	headers := map[string]string{"Authorization": "Bearer secret", "If-Match": `"0"`}
	if w := request(h, "PUT", "/api/v1/dataset", sample, headers); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := request(h, "GET", "/api/v1/dataset", "", headers).Body.String()
	dates := []uploadPart{{field: "as_of", value: "2026-09-22"}, {field: "history_start", value: "2025-01-01"}}
	for _, tc := range []struct {
		name   string
		parts  []uploadPart
		status int
	}{
		{"missing files", dates, 400},
		{"missing dates", []uploadPart{{"files", "broken.xlsx", "not a zip"}}, 400},
		{"JSON cannot be mixed", append(append([]uploadPart{}, dates...), uploadPart{"files", "data.json", sample}), 400},
		{"unexpected file field", append(append([]uploadPart{}, dates...), uploadPart{"other", "broken.xlsx", "not a zip"}), 400},
		{"duplicate date", append(append([]uploadPart{}, dates...), dates[0]), 400},
		{"unknown field", []uploadPart{{field: "typo", value: "2025-01-01"}}, 400},
		{"long field", []uploadPart{{field: "as_of", value: strings.Repeat("x", 1000)}}, 400},
		{"invalid workbook", append(append([]uploadPart{}, dates...), uploadPart{"files", "broken.xlsx", "not a zip"}), 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, uploadRequest(t, tc.parts))
			if w.Code != tc.status || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	r := uploadRequest(t, dates)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized import: %d", w.Code)
	}
	w = request(h, "POST", "/api/v1/import/xlsx", "{}", headers)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type: %d", w.Code)
	}
	parts := append([]uploadPart{}, dates...)
	for i := 0; i < 13; i++ {
		parts = append(parts, uploadPart{"files", "extra.xlsx", ""})
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, uploadRequest(t, parts))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "too_many_files") {
		t.Fatalf("file count limit: %d %s", w.Code, w.Body.String())
	}
	after := request(h, "GET", "/api/v1/dataset", "", headers)
	if after.Body.String() != before || after.Header().Get("ETag") != `"1"` {
		t.Fatal("failed upload changed stored snapshot")
	}
}

func TestExcelUploadBodyLimitAndBusy(t *testing.T) {
	h := testAPI(t, "secret")
	r := uploadRequest(t, []uploadPart{{"files", "large.xlsx", strings.Repeat("x", maxBodyBytes)}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit: %d %s", w.Code, w.Body.String())
	}
	a := &API{importSlots: make(chan struct{}, 1)}
	a.importSlots <- struct{}{}
	w = httptest.NewRecorder()
	a.importXLSX(w, uploadRequest(t, nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("concurrent import limit: %d", w.Code)
	}
}

func TestExcelPreviewAndConfirmedImport(t *testing.T) {
	h := testAPI(t, "secret")
	files, err := filepath.Glob("../workbook/testdata/iek/*.xlsx")
	if err != nil || len(files) != 6 {
		t.Fatalf("six Excel fixtures required: %v %v", files, err)
	}
	parts := []uploadPart{{field: "as_of", value: "2026-09-22"}, {field: "history_start", value: "2025-01-01"}}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, uploadPart{"files", filepath.Base(file), string(data)})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, uploadRequest(t, parts))
	if w.Code != 200 {
		t.Fatalf("Excel preview: %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		Data planning.Dataset `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if err := preview.Data.Validate(); err != nil || len(preview.Data.Products) == 0 || len(preview.Data.Sales) == 0 {
		t.Fatalf("invalid converted dataset: %v", err)
	}
	headers := map[string]string{"Authorization": "Bearer secret"}
	current := request(h, "GET", "/api/v1/dataset", "", headers)
	if current.Header().Get("ETag") != `"0"` {
		t.Fatal("preview persisted data before confirmation")
	}
	// An intervening save must still require the operator to review the new
	// revision. Excel previews have no special path around compare-and-swap.
	headers["If-Match"] = `"0"`
	if w := request(h, "PUT", "/api/v1/dataset", sample, headers); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body, err := json.Marshal(preview.Data)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "PUT", "/api/v1/dataset", string(body), headers); w.Code != 412 {
		t.Fatalf("stale Excel confirmation accepted: %d", w.Code)
	}
	headers["If-Match"] = `"1"`
	w = request(h, "PUT", "/api/v1/dataset", string(body), headers)
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("confirm Excel import: %d %s", w.Code, w.Body.String())
	}
}
