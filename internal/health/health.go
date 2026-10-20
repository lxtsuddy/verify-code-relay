// Package health serves /healthz with per-watcher status.
package health

import (
	"encoding/json"
	"net/http"
)

// Source is satisfied by an adapter around *watcher.Watcher (see cmd/relay/run.go).
type Source interface {
	Key() string
	StatusString() string
}

func Handler(sources []Source) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		statuses := map[string]string{}
		healthy := true
		for _, s := range sources {
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
