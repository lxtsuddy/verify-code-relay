package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type src struct{ key, status string }

func (s src) Key() string          { return s.key }
func (s src) StatusString() string { return s.status }

func get(t *testing.T, h http.Handler) (int, map[string]map[string]string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var body map[string]map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestHealthy(t *testing.T) {
	code, body := get(t, Handler([]Source{src{"a/INBOX", "connected"}, src{"b/INBOX", "connected"}}))
	if code != 200 || body["mailboxes"]["b/INBOX"] != "connected" {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestUnhealthy(t *testing.T) {
	code, body := get(t, Handler([]Source{src{"a/INBOX", "connected"}, src{"o/INBOX", "needs_login"}}))
	if code != 503 || body["mailboxes"]["o/INBOX"] != "needs_login" {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("code=%d", rec.Code)
	}
}
