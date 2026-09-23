package httpapi

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/electrokomplekt/replenishment/internal/planning"
	"github.com/electrokomplekt/replenishment/internal/storage"
	"github.com/electrokomplekt/replenishment/internal/webui"
)

const maxBodyBytes = 8 << 20

type API struct {
	store  *storage.Store
	logger *slog.Logger
	apiKey string
	origin string
}

func New(store *storage.Store, logger *slog.Logger, apiKey, allowedOrigin string) http.Handler {
	a := &API{store: store, logger: logger, apiKey: apiKey, origin: allowedOrigin}
	mux := http.NewServeMux()
	webui.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ready"}) })
	mux.HandleFunc("GET /api/v1/dataset", a.getDataset)
	mux.HandleFunc("PUT /api/v1/dataset", a.putDataset)
	mux.HandleFunc("POST /api/v1/recommendations", a.recommend)
	mux.HandleFunc("POST /api/v1/recommendations.csv", a.recommend)
	return a.middleware(mux)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		problem(w, 415, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	err := decoder.Decode(&raw)
	if err == nil {
		var extra any
		if e := decoder.Decode(&extra); !errors.Is(e, io.EOF) {
			if e == nil {
				err = errors.New("body must contain exactly one JSON value")
			} else {
				err = e
			}
		}
	}
	if err == nil {
		if len(raw) == 0 || raw[0] != '{' {
			err = errors.New("body must be a JSON object")
		} else {
			object := json.NewDecoder(bytes.NewReader(raw))
			object.DisallowUnknownFields()
			err = object.Decode(target)
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, 413, "body_too_large", "request body exceeds 8 MiB")
		} else {
			problem(w, 400, "invalid_json", err.Error())
		}
		return false
	}
	return true
}

func etag(revision uint64) string { return fmt.Sprintf("\"%d\"", revision) }

func (a *API) getDataset(w http.ResponseWriter, _ *http.Request) {
	snapshot := a.store.Read()
	w.Header().Set("ETag", etag(snapshot.Revision))
	writeJSON(w, 200, snapshot)
}

func (a *API) putDataset(w http.ResponseWriter, r *http.Request) {
	match := r.Header.Get("If-Match")
	if match == "" {
		problem(w, 428, "revision_required", "send the ETag from GET /api/v1/dataset in If-Match")
		return
	}
	if len(match) < 3 || match[0] != '"' || match[len(match)-1] != '"' {
		problem(w, 400, "invalid_revision", "If-Match must be a quoted revision number")
		return
	}
	revision, err := strconv.ParseUint(match[1:len(match)-1], 10, 64)
	if err != nil {
		problem(w, 400, "invalid_revision", "If-Match must be a quoted revision number")
		return
	}
	var data planning.Dataset
	if !decode(w, r, &data) {
		return
	}
	if err := data.Validate(); err != nil {
		problem(w, 422, "invalid_dataset", err.Error())
		return
	}
	snapshot, err := a.store.Replace(r.Context(), data, revision)
	if errors.Is(err, storage.ErrConflict) {
		problem(w, 412, "revision_conflict", "dataset changed; fetch the latest snapshot before retrying")
		return
	}
	if err != nil {
		a.logger.Error("save dataset", "error", err)
		problem(w, 500, "storage_error", "could not persist dataset")
		return
	}
	w.Header().Set("ETag", etag(snapshot.Revision))
	writeJSON(w, 200, snapshot)
}

func (a *API) recommend(w http.ResponseWriter, r *http.Request) {
	params := planning.DefaultRequest(time.Now())
	if !decode(w, r, &params) {
		return
	}
	if err := params.Validate(); err != nil {
		problem(w, 422, "invalid_parameters", err.Error())
		return
	}
	snapshot := a.store.Read()
	result, err := planning.Calculate(r.Context(), snapshot.Data, params)
	if err != nil {
		a.logger.Error("calculate recommendations", "error", err)
		problem(w, 500, "calculation_error", "could not calculate recommendations")
		return
	}
	w.Header().Set("X-Dataset-Revision", strconv.FormatUint(snapshot.Revision, 10))
	if strings.HasSuffix(r.URL.Path, ".csv") {
		var body bytes.Buffer
		writer := csv.NewWriter(&body)
		_ = writer.Write([]string{"supplier_id", "supplier_name", "product_id", "sku", "product_name", "quantity", "expected_date", "daily_demand", "available_stock", "incoming_quantity"})
		for _, order := range result.Orders {
			for _, line := range order.Lines {
				_ = writer.Write([]string{safeCell(order.SupplierID), safeCell(order.SupplierName), safeCell(line.ProductID), safeCell(line.SKU), safeCell(line.Name),
					strconv.FormatInt(line.OrderQuantity, 10), order.ExpectedDate, strconv.FormatFloat(line.DailyDemand, 'f', 6, 64), strconv.FormatInt(line.AvailableStock, 10), strconv.FormatInt(line.IncomingQuantity, 10)})
			}
		}
		writer.Flush()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="supplier-orders.csv"`)
		_, _ = w.Write(body.Bytes())
		return
	}
	writeJSON(w, 200, struct {
		Revision uint64 `json:"dataset_revision"`
		planning.Result
	}{snapshot.Revision, result})
}

// Neutralize spreadsheet formulas in user-supplied CSV cells.
func safeCell(s string) string {
	trimmed := strings.TrimLeftFunc(s, unicode.IsSpace)
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + s
	}
	if strings.HasPrefix(s, "\t") || strings.HasPrefix(s, "\r") || strings.HasPrefix(s, "\n") {
		return "'" + s
	}
	return s
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(body)
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("request panic", "error", recovered)
				if rw.status == 0 {
					problem(rw, 500, "internal_error", "internal server error")
				}
			}
			a.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", time.Since(started).Milliseconds())
		}()
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			rw.Header().Add("Vary", "Origin")
			if origin == a.origin {
				rw.Header().Set("Access-Control-Allow-Origin", origin)
				rw.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
				rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
				rw.Header().Set("Access-Control-Expose-Headers", "ETag, X-Dataset-Revision, Content-Disposition")
			}
			if r.Method == http.MethodOptions {
				if origin != a.origin {
					problem(rw, 403, "origin_not_allowed", "origin is not allowed")
					return
				}
				rw.WriteHeader(204)
				return
			}
		}
		if a.apiKey != "" && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && !webui.IsPublic(r.URL.Path) {
			expected := sha256.Sum256([]byte("Bearer " + a.apiKey))
			actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
			if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
				rw.Header().Set("WWW-Authenticate", "Bearer")
				problem(rw, 401, "unauthorized", "a valid bearer API key is required")
				return
			}
		}
		next.ServeHTTP(rw, r)
	})
}
