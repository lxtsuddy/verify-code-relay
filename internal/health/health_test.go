package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type src struct{ key, status string }

func (s src) Key() string          { return s.key }
func (s src) StatusString() string { return s.status }

// handler serves a fixed list of sources and is always ready.
func handler(srcs []Source) http.Handler {
	return Handler(func() []Source { return srcs }, func(context.Context) error { return nil })
}

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
	code, body := get(t, handler([]Source{src{"a/INBOX", "connected"}, src{"b/INBOX", "connected"}}))
	if code != 200 || body["mailboxes"]["b/INBOX"] != "connected" {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestUnhealthy(t *testing.T) {
	code, body := get(t, handler([]Source{src{"a/INBOX", "connected"}, src{"o/INBOX", "needs_login"}}))
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
		code, body := get(t, handler(sources))
		if code != 503 || body["mailboxes"]["a/INBOX"] != "disconnected" {
			t.Errorf("%v: code=%d body=%v", sources, code, body)
		}
	}
}

// 沒有任何信箱時目前回傳 200（探針之後會隨 Web 管理介面重新設計，先鎖定現況）。
func TestNoSourcesIsHealthy(t *testing.T) {
	code, body := get(t, handler(nil))
	if code != 200 || body["mailboxes"] == nil || len(body["mailboxes"]) != 0 {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestLivezReadyz(t *testing.T) {
	ready := errors.New("db down")
	h := Handler(func() []Source { return nil }, func(context.Context) error { return ready })
	for path, want := range map[string]int{"/livez": 200, "/readyz": 503} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
	}
	ready = nil
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Errorf("/readyz = %d after recovery", rec.Code)
	}
}

// /healthz must reflect the sources current at request time (they change on every Apply).
func TestHealthzReadsSourcesPerRequest(t *testing.T) {
	srcs := []Source{src{"a/INBOX", "connecting"}}
	h := Handler(func() []Source { return srcs }, nil)
	if code, _ := get(t, h); code != 503 {
		t.Fatalf("code = %d, want 503", code)
	}
	srcs = []Source{src{"a/INBOX", "connected"}}
	if code, _ := get(t, h); code != 200 {
		t.Fatalf("code = %d after sources changed, want 200", code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Errorf("/readyz with nil ready = %d, want 200", rec.Code)
	}
}
