package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAssets(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	for _, tc := range []struct{ path, contentType, contains string }{
		{"/", "text/html", "Пополнение склада"},
		{"/assets/app.js", "text/javascript", "getRecommendations"},
		{"/assets/styles.css", "text/css", "@media"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("asset: status=%d type=%s", w.Code, w.Header().Get("Content-Type"))
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("missing CSP")
			}
		})
	}
	for _, path := range []string{"/assets/", "/assets/secret.json", "/unknown", "/web.go"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || IsPublic(path) {
			t.Fatalf("unexpected public path %s", path)
		}
	}
}
