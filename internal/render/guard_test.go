package render

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
)

func TestCompileRejectsLoopsAndTemplateCalls(t *testing.T) {
	for _, src := range []string{
		`{{range 100000000}}x{{end}}`,
		`{{range $i, $c := .Code}}{{$c}}{{end}}`,
		`{{define "a"}}x{{end}}{{.Code}}`,
		`{{block "b" .}}{{.Code}}{{end}}`,
		`{{template "x"}}`,
		`{{if .Code}}{{range 3}}{{end}}{{else}}{{with .URL}}{{range 3}}{{end}}{{end}}{{end}}`,
		`{{with .URL}}{{else}}{{range 3}}{{end}}{{end}}`,
	} {
		_, err := New(&config.Config{Templates: config.Templates{Code: src}})
		if err == nil || !strings.Contains(err.Error(), "templates.code: 範本不支援") {
			t.Errorf("%q: err = %v", src, err)
		}
	}
}

// TestCompileRejectsTemplateDoubling: a 1.3 KB chain of templates calling the previous one
// twice would run 2^22 times; it must be refused before any execution.
func TestCompileRejectsTemplateDoubling(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{{define "t0"}}{{end}}`)
	for i := 1; i <= 22; i++ {
		fmt.Fprintf(&b, `{{define "t%d"}}{{template "t%d"}}{{template "t%d"}}{{end}}`, i, i-1, i-1)
	}
	b.WriteString(`{{template "t22"}}{{.Code}}`)
	start := time.Now()
	_, err := New(&config.Config{Templates: config.Templates{Code: b.String()}})
	if err == nil || !strings.Contains(err.Error(), "範本不支援") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestPrintfGuard(t *testing.T) {
	for _, src := range []string{
		`{{printf "%0999999999d" 1}}`,
		`{{printf "%.9999f" 1.0}}`,
		`{{printf "%*d" 999999999 1}}`,
		`{{printf "%.*f" 999999999 1.0}}`,
		`{{printf "%-1000s" .Code}}`,
	} {
		_, err := New(&config.Config{Templates: config.Templates{Code: src}})
		if err == nil || !strings.Contains(err.Error(), "printf") {
			t.Errorf("%q: err = %v", src, err)
		}
	}
	// ordinary formats keep working
	r, err := New(&config.Config{Templates: config.Templates{Code: `{{printf "%s|%6s|%-3s|%05.1f|%%|%q|%[1]s" .Code "a" "b" 3.14159 .Service}}`}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("", Data{Code: "12", Service: "S"})
	if err != nil || out.Text != `12|     a|b  |003.1|%|&#34;S&#34;|12` {
		t.Fatalf("= %q, %v", out.Text, err)
	}
	// a format taken from the mail is checked when the message is rendered
	r, err = New(&config.Config{Templates: config.Templates{Code: `{{printf .Subject 1}} {{.Code}}`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Render("", Data{Code: "1", Subject: "%0999999999d"}); err == nil || !strings.Contains(err.Error(), "printf") {
		t.Fatalf("subject format: %v", err)
	}
}

func TestOutputCap(t *testing.T) {
	r, err := New(&config.Config{Templates: config.Templates{Code: `{{.Subject}}{{.Subject}}{{.Code}}`}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("", Data{Code: "1", Subject: strings.Repeat("x", maxOutput)})
	if err != nil {
		t.Fatal(err)
	}
	checkFits(t, out.Text)
	var w capWriter
	n, err := w.Write(make([]byte, maxOutput+1))
	if n != maxOutput || err != errOutputCap || w.b.Len() != maxOutput {
		t.Fatalf("capWriter = %d %v %d", n, err, w.b.Len())
	}
}

func TestBuiltinTemplatesPassTheGuard(t *testing.T) {
	b := Builtin()
	for _, src := range []string{b.Code, b.Link, b.Both} {
		if _, err := compile("x", src); err != nil {
			t.Fatal(err)
		}
	}
}
