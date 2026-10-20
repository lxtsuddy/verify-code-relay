// Package health serves the liveness, readiness and per-watcher status probes.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Source is satisfied by an adapter around *watcher.Watcher (see internal/runtime).
type Source interface {
	Key() string
	StatusString() string
}

// readyTimeout bounds one readiness check so a stuck database cannot hang the probe.
const readyTimeout = 3 * time.Second

// Handler serves /livez (always 200), /readyz (200 when ready(ctx) returns nil, else 503)
// and /healthz (per-watcher status; 200 when every source is connected or there are none).
// sources is called on every /healthz request, so it may change while serving.
// A nil ready is always ready.
func Handler(sources func() []Source, ready func(ctx context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		plain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
			defer cancel()
			// The error is not echoed: the probe may be reachable without authentication.
			if err := ready(ctx); err != nil {
				plain(w, http.StatusServiceUnavailable, "not ready")
				return
			}
		}
		plain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		statuses := map[string]string{}
		healthy := true
		for _, s := range sources() {
			st := s.StatusString()
			if st != "connected" {
				healthy = false
			}
			// 同一個 key 重複時保留不健康的狀態，讓內容與 503 一致
			if prev, ok := statuses[s.Key()]; !ok || prev == "connected" {
				statuses[s.Key()] = st
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(map[string]any{"mailboxes": statuses})
	})
	return mux
}

func plain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	w.Write([]byte(body + "\n"))
}
