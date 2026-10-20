package render

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
)

// compileFast compiles src as the global code template, failing the test if that takes
// 100 ms or more (a refused template must be refused before it runs).
func compileFast(t *testing.T, src string) error {
	t.Helper()
	start := time.Now()
	_, err := New(&config.Config{Templates: config.Templates{Code: src}})
	if d := time.Since(start); d >= 100*time.Millisecond {
		t.Errorf("%.60q took %v", src, d)
	}
	return err
}

func TestCompileRejectsUnboundedTemplates(t *testing.T) {
	doubling := func(n int, unit, step string) string {
		var b strings.Builder
		b.WriteString(unit)
		for range n {
			b.WriteString(step)
		}
		return b.String()
	}
	var chain strings.Builder
	chain.WriteString(`{{define "t0"}}{{end}}`)
	for i := 1; i <= 22; i++ {
		fmt.Fprintf(&chain, `{{define "t%d"}}{{template "t%d"}}{{template "t%d"}}{{end}}`, i, i-1, i-1)
	}
	chain.WriteString(`{{template "t22"}}{{.Code}}`)

	for _, c := range []struct{ src, want string }{
		// loops, template calls and definitions
		{`{{range 100000000}}x{{end}}`, "range"},
		{`{{range .Code}}{{.}}{{end}}`, "range"},
		{`{{if .Code}}{{.Code}}{{else}}{{with .URL}}{{range 3}}{{end}}{{end}}{{end}}`, "range"},
		{`{{with .URL}}{{else}}{{range 3}}{{end}}{{end}}`, "range"},
		{`{{define "a"}}x{{end}}{{.Code}}`, "define"},
		{`{{block "b" .}}{{.Code}}{{end}}`, "define"},
		{`{{template "x"}}`, "template"},
		{chain.String(), "define"},
		// variables: the reviewer's doubling, and any other use
		{doubling(34, `{{$a := "x"}}`, `{{$a = print $a $a}}`) + `{{.Code}}`, "變數"},
		{`{{$}}`, "變數"},
		{`{{with $x := .Code}}{{$x}}{{end}}`, "變數"},
		{`{{if $x := .Code}}{{end}}{{.Code}}`, "變數"},
		// with on a computed value doubles dot at every nesting level
		{strings.Repeat(`{{with print . .}}`, 30) + strings.Repeat(`{{end}}`, 30) + `{{.Code}}`, "with"},
		{`{{with .Subject | html}}{{.}}{{end}}{{.Code}}`, "with"},
		{`{{with .Date.Format "15:04"}}{{.}}{{end}}{{.Code}}`, "with"},
		{`{{with .Code}}{{else with print .URL .URL}}{{end}}`, "with"},
		// long pipelines (the reviewer's js doubling) and sub-pipelines
		{`{{if "\\" ` + strings.Repeat("| js ", 26) + `}}{{end}}{{.Code}}`, "管線"},
		{`{{.Subject | html | js}}`, "管線"},
		{`{{print (print .Subject .Subject) .Code}}`, "括號"},
		{`{{(.Date).Year}}`, "括號"},
		// functions
		{`{{call .Code}}`, "call"},
		{`{{print html}}`, "開頭"},
		{`{{print ` + strings.Repeat(".Subject ", maxArgs) + `}}`, "參數"},
		{`{{nil}}`, "參數"},
		// printf formats
		{`{{printf "%0999999999d" 1}}`, "printf"},
		{`{{printf "%9999999!" 1}}`, "printf"},
		{`{{printf "%9999999中" 1}}`, "printf"},
		{`{{printf "%.9999f" 1.0}}`, "printf"},
		{`{{printf "%*d" 999999999 1}}`, "printf"},
		{`{{printf "%.*f" 999 1.0}}`, "printf"},
		{`{{printf "%[1]s" .Code}}`, "printf"},
		{`{{printf "%-1000s" .Code}}`, "printf"},
		{`{{printf .Subject 1}}`, "printf"},
		{`{{.Subject | printf}}`, "printf"},
	} {
		err := compileFast(t, c.src)
		if err == nil || !strings.HasPrefix(err.Error(), "templates.code: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.80q: err = %v", c.src, err)
		}
	}
}

func TestCompileAllowsStraightLineTemplates(t *testing.T) {
	r, err := New(&config.Config{Templates: config.Templates{Code: `{{with .Subject}}[{{.}}]{{else}}-{{end}}` +
		`{{if eq .Service "GitHub"}}gh{{else if ne .Rule ""}}{{.Rule}}{{end}} {{.Date.Format "15:04"}} ` +
		`{{printf "%-10s|%3d|%05.1f|%%|%q" .Service 7 3.14159 .Code}} {{.Subject | html}} {{len .Code}} ` +
		`{{with .Code}}{{slice . 0 2}} {{index . 0}}{{end}} {{and .Code .URL}}{{or .URL "none"}} {{not .URL}} {{println .Code}}` +
		`{{/* comment */}}{{.Code | urlquery}} {{js .Subject}} {{print .Code .Service}}`}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("", Data{Code: "123456", Service: "GitHub", Subject: "a<b", Rule: "x", Date: time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	want := `[a&lt;b]gh 15:04 GitHub    |  7|003.1|%|&#34;123456&#34; a&lt;b 6 12 49 none true 123456` + "\n" +
		`123456 a\u003Cb 123456GitHub`
	if out.Text != want {
		t.Fatalf("got  %q\nwant %q", out.Text, want)
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

func TestBuiltinAndExampleTemplatesPassTheGuard(t *testing.T) {
	b := Builtin()
	for _, src := range []string{b.Code, b.Link, b.Both,
		// config.example.yaml
		"💼 <b>{{.Service}}</b>：<code>{{.Code}}</code>\n📧 {{.Mailbox}}{{with .Subject}} · {{.}}{{end}}",
	} {
		if _, err := compile("x", src); err != nil {
			t.Fatal(err)
		}
	}
}
