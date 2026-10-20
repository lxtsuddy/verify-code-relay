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
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
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

// 同一個 key 出現多次時，內容必須與狀態碼一致：只要有一個不是 connected，就回報那個狀態。
func TestDuplicateKeyReportsUnhealthyStatus(t *testing.T) {
	for _, sources := range [][]Source{
		{src{"a/INBOX", "connected"}, src{"a/INBOX", "disconnected"}},
		{src{"a/INBOX", "disconnected"}, src{"a/INBOX", "connected"}},
	} {
		code, body := get(t, Handler(sources))
		if code != 503 || body["mailboxes"]["a/INBOX"] != "disconnected" {
			t.Errorf("%v: code=%d body=%v", sources, code, body)
		}
	}
}

// 沒有任何信箱時目前回傳 200（探針之後會隨 Web 管理介面重新設計，先鎖定現況）。
func TestNoSourcesIsHealthy(t *testing.T) {
	code, body := get(t, Handler(nil))
	if code != 200 || body["mailboxes"] == nil || len(body["mailboxes"]) != 0 {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("code=%d", rec.Code)
	}
}
