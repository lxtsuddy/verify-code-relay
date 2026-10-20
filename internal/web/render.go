package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Page is the data every template receives.
type Page struct {
	Title  string
	Nav    string // current nav item: dashboard|mailboxes|telegram|rules|logs|settings
	CSRF   string
	Flash  *Flash
	Errors FieldErrors // field path → message; key "" holds form-level errors
	Data   any
}

// Flash is a one-shot message shown on the next page.
type Flash struct{ Kind, Text string } // Kind: success|error|info

// FieldErrors maps a field path (e.g. "mailboxes.gmail.host") to its message.
type FieldErrors map[string]string

// Templates: layout.html and every file whose name starts with "_" hold shared
// definitions and are parsed into every page; each other file is one page, executed by
// its file name without ".html" (render(w, r, status, "login", p)). See layout.html for
// the conventions page files follow.
func (s *Server) parsePages() map[string]*template.Template {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		panic(err)
	}
	shared := []string{"templates/layout.html"}
	var pages []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() || !strings.HasSuffix(name, ".html") || name == "layout.html":
		case strings.HasPrefix(name, "_"):
			shared = append(shared, "templates/"+name)
		default:
			pages = append(pages, name)
		}
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		files := append(append([]string(nil), shared...), "templates/"+p)
		t := template.Must(template.New(p).Funcs(s.funcs()).ParseFS(templateFS, files...))
		out[strings.TrimSuffix(p, ".html")] = t
	}
	return out
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		// asset returns the URL of a static file with a cache-busting version.
		"asset": func(name string) string { return "/static/" + name + "?v=" + s.assetV },
		// dict builds a map for passing several values to a partial: dict "k1" v1 "k2" v2.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, errors.New("dict: 參數必須成對")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: 第 %d 個鍵不是字串", i/2+1)
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		// chars splits s into its characters, e.g. to draw a code in boxes.
		"chars": func(s string) []string { return strings.Split(s, "") },
		// datetime formats t for display, e.g. 2026-10-20 14:05.
		"datetime": func(t time.Time) string { return t.Format("2006-01-02 15:04") },
		// fieldErr returns the message for path ("" when there is none).
		"fieldErr": func(fe FieldErrors, path string) string { return fe[path] },
	}
}

// assetVersion hashes every static file so a new build never serves stale CSS or JS.
func assetVersion() string {
	h := sha256.New()
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// render executes page with p (adding the session's CSRF token and any pending flash).
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, p Page) {
	t, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("找不到頁面範本 %q", page))
		return
	}
	if p.CSRF == "" {
		if si := sessionFrom(r.Context()); si != nil {
			p.CSRF = si.CSRF
		}
	}
	if p.Flash == nil {
		p.Flash = s.takeFlash(w, r)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, page+".html", p); err != nil {
		s.serverError(w, r, fmt.Errorf("產生頁面 %s：%w", page, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// redirect answers 303 See Other to `to`, carrying f (may be nil) to the next page.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string, f *Flash) {
	if f != nil {
		s.setFlash(w, *f)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// json writes v as a JSON response.
func (s *Server) json(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.o.Log.Error("JSON 編碼失敗", "err", err)
		status, b = http.StatusInternalServerError, []byte(`{"ok":false,"error":"內部錯誤"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b)
}

// wantsJSON reports whether the caller is script code expecting a JSON answer.
func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

type errorData struct {
	Status  int
	Heading string
	Message string
}

// fail answers an error: JSON {"ok":false,"error":msg} for script callers, an error page
// otherwise. msg is shown to the user and must not contain secrets.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if wantsJSON(r) {
		s.json(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	heading := map[int]string{
		http.StatusUnauthorized:          "需要登入",
		http.StatusForbidden:             "無法完成這個要求",
		http.StatusNotFound:              "找不到這個頁面",
		http.StatusRequestEntityTooLarge: "送出的資料太大",
	}[status]
	if heading == "" {
		heading = "發生錯誤"
	}
	s.render(w, r, status, "error", Page{Title: heading, Data: errorData{Status: status, Heading: heading, Message: msg}})
}

// serverError logs err and answers 500 without revealing details.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.o.Log.Error("Web 請求失敗", "method", r.Method, "path", r.URL.Path, "err", err)
	msg := "伺服器內部錯誤，詳情請看服務日誌"
	if wantsJSON(r) {
		s.json(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": msg})
		return
	}
	// a plain page: rendering the error template could fail the same way
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(w, msg)
}

// notFound is the catch-all route.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "這個網址不存在，可能輸入有誤或頁面已移除。")
}
