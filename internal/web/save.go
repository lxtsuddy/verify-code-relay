package web

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/websec"
)

// mutate loads the latest raw config, applies fn, validates, stores it as a new version
// (source "web", client IP, summary) and applies it. Calls are serialised. A validation
// failure returns the problems as FieldErrors and stores nothing; fn may itself return
// FieldErrors (as error) to reject the change.
func (s *Server) mutate(r *http.Request, summary string, fn func(raw *config.Config) error) (FieldErrors, error) {
	return s.mutateAs(r, "web", summary, fn)
}

// mutateAs is mutate with another version source (e.g. "restore").
func (s *Server) mutateAs(r *http.Request, source, summary string, fn func(raw *config.Config) error) (FieldErrors, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	ctx := r.Context()
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		return nil, err
	}
	if err := fn(raw); err != nil {
		var fe FieldErrors
		if errors.As(err, &fe) {
			return fe, nil
		}
		return nil, err
	}
	// validate first so that only Build's problems ("path: message" lines) become field
	// errors; SaveConfig builds again, which is cheap, and its errors are then internal
	if _, err := runtime.Build(raw); err != nil {
		return fieldErrors(err), nil
	}
	ip := websec.ClientIP(r, s.o.TrustedProxies)
	b, id, err := runtime.SaveConfig(ctx, s.o.Store, raw, runtime.Meta{Source: source, IP: ip, Summary: summary}, s.now())
	if err != nil {
		return nil, err
	}
	s.o.Runtime.Apply(b)
	s.o.Log.Info("設定已儲存並套用", "version", id, "source", source, "summary", summary, "ip", ip)
	return nil, nil
}

// pathRe matches a config path such as "mailboxes.gmail.folders[0]" or "admin".
var pathRe = regexp.MustCompile(`^[a-z0-9_-]+(\[\d+\])?(\.[A-Za-z0-9_-]+(\[\d+\])?)*$`)

// fieldErrors turns "path: message" lines into FieldErrors; lines without a path go under "".
// Several messages for one path are joined with newlines.
func fieldErrors(err error) FieldErrors {
	if err == nil {
		return nil
	}
	fe := FieldErrors{}
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path, msg := "", line
		if p, m, ok := strings.Cut(line, ": "); ok && pathRe.MatchString(p) {
			path, msg = p, m
		}
		fe.add(path, msg)
	}
	return fe
}

func (fe FieldErrors) add(path, msg string) {
	if old, ok := fe[path]; ok && old != "" {
		msg = old + "\n" + msg
	}
	fe[path] = msg
}

// Error lists the problems, form-level ones first, then "path: message" by path.
func (fe FieldErrors) Error() string {
	keys := make([]string, 0, len(fe))
	for k := range fe {
		keys = append(keys, k)
	}
	sort.Strings(keys) // "" sorts first
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			lines = append(lines, fe[k])
		} else {
			lines = append(lines, k+": "+fe[k])
		}
	}
	return strings.Join(lines, "\n")
}

// only keeps the errors of the given fields and moves every other one, as
// "path: message", into "" so the form shows it at the top instead of losing it.
func (fe FieldErrors) only(fields ...string) FieldErrors {
	if fe == nil {
		return nil
	}
	known := map[string]bool{}
	for _, f := range fields {
		known[f] = true
	}
	out := FieldErrors{}
	rest := FieldErrors{}
	for k, v := range fe {
		if known[k] {
			out[k] = v
		} else {
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		out[""] = rest.Error()
	}
	return out
}

// parseDuration reads an optional duration field: "" = unset, otherwise time.ParseDuration
// (a bare number is rejected with "請輸入例如 10m、30s、1h").
func parseDuration(v string) (config.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return config.Duration{}, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return config.Duration{}, fmt.Errorf("無法解讀 %q：請輸入例如 10m、30s、1h", v)
	}
	return config.Duration{Duration: d, Set: true}, nil
}

// durationValue is the form value of d: "" when unset, else d.Duration.String().
func durationValue(d config.Duration) string {
	if !d.Set {
		return ""
	}
	return d.Duration.String()
}
