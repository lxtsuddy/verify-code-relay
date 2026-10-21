package mailparse

import (
	"reflect"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	src := `<html><head><title>T</title><style>p{color:red}</style></head><body>
<p>Hello&nbsp;there</p><div>Your code:<br><b>123456</b></div>
<script>var x = 999999;</script>
<div style="display: none">hidden 777777</div>
<img src="https://t.example.com/pixel.gif">
<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>
<a href="https://example.com/verify?t=abc">Verify   email</a>
<a href="mailto:x@y.z">mail</a>
</body></html>`
	text, links := HTMLToText(src)
	for _, bad := range []string{"999999", "777777", "color:red"} {
		if strings.Contains(text, bad) {
			t.Errorf("text contains %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{"Hello there", "Your code:\n123456", "Verify email"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	wantLinks := []Link{{URL: "https://example.com/verify?t=abc", Text: "Verify email"}}
	if !reflect.DeepEqual(links, wantLinks) {
		t.Errorf("links = %+v", links)
	}
}

func TestExtractURLs(t *testing.T) {
	got := ExtractURLs("go to https://a.com/x?y=1. or (https://b.com/z), 或 https://c.com/中文，好")
	want := []string{"https://a.com/x?y=1", "https://b.com/z", "https://c.com/中文"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestHTMLSourceNewlinesCollapsed(t *testing.T) {
	text, _ := HTMLToText("<p>Your code is\n123456 ok</p>")
	if !strings.Contains(text, "Your code is 123456 ok") {
		t.Errorf("text = %q", text)
	}
}

func TestHTMLPreKeepsLineBreaks(t *testing.T) {
	text, _ := HTMLToText("<pre>line one\nline two</pre>")
	if !strings.Contains(text, "line one\nline two") {
		t.Errorf("text = %q", text)
	}
}

func TestHTMLPreKeepsIndentation(t *testing.T) {
	text, _ := HTMLToText("<p>Intro   text</p>before<pre>  code:\n    123456\n\tend  </pre>after   x")
	want := "Intro text\nbefore\n  code:\n    123456\n\tend\nafter x"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestHTMLPreAtStartKeepsIndentation(t *testing.T) {
	text, _ := HTMLToText("<pre>    indented\n<b>  bold</b></pre>")
	if text != "    indented\n  bold" {
		t.Errorf("text = %q", text)
	}
}

func TestExtractURLsFullWidthPunctuation(t *testing.T) {
	got := ExtractURLs("網址：https://a.com/x：請點擊 https://b.com/y！ https://c.com/z？ https://d.com/w；" +
		"【https://e.com/v】 https://f.com/u　下一句")
	want := []string{"https://a.com/x", "https://b.com/y", "https://c.com/z", "https://d.com/w", "https://e.com/v", "https://f.com/u"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// normalizeLinesRef is normalizeLines as it was before it tracked positions: the
// position-tracking version must produce exactly the same text.
func normalizeLinesRef(s string, verbatim []span) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := true
	off := 0
	for _, l := range lines {
		start, end := off, off+len(l)
		off = end + 1
		for len(verbatim) > 0 && verbatim[0].end <= start {
			verbatim = verbatim[1:]
		}
		if len(verbatim) > 0 && verbatim[0].start < end {
			l = strings.TrimRight(l, " \t\r\f\v")
		} else {
			l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		}
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func TestNormalizeLinesMapMatchesReference(t *testing.T) {
	cases := []struct {
		s        string
		verbatim []span
	}{
		{"", nil},
		{"  a   b  \n\n\n c\t\td \n", nil},
		{"　全形空白　\n nbsp ", nil},
		{"a\u0085\nb ", nil},
		{"bad \xff byte\n\xef\xbf\xbd ok", nil},
		{"x\n  keep   inner  \ny", []span{{2, 20}}},
		{"\n\n\nlead\n\n\ntrail\n\n\n", nil},
		{"中文  字\t\t間\n第二行", nil},
	}
	for _, c := range cases {
		got, pos := normalizeLinesMap(c.s, c.verbatim)
		if want := normalizeLinesRef(c.s, c.verbatim); got != want {
			t.Errorf("normalizeLinesMap(%q) = %q, want %q", c.s, got, want)
		}
		if len(pos) != len(c.s)+1 {
			t.Fatalf("len(pos) = %d, want %d", len(pos), len(c.s)+1)
		}
		for i := 1; i < len(pos); i++ {
			if pos[i] < pos[i-1] || pos[i] > len(got) {
				t.Fatalf("pos not monotonic within [0,%d]: %v", len(got), pos)
			}
		}
	}
}

func TestRenderHTMLEmphasis(t *testing.T) {
	cases := []struct {
		src, text string
		want      []Span
	}{
		{`<p>Your code is <b>123456</b></p>`, "Your code is 123456", []Span{{13, 19}}},
		{`<p>您的驗證碼為 <strong>482913</strong>。</p>`, "您的驗證碼為 482913。", []Span{{7, 13}}},
		{`<p>a    b <b>  777777  </b> c</p>`, "a b 777777 c", []Span{{4, 10}}},
		{`<div style="font-size: 28px">654321</div>`, "654321", []Span{{0, 6}}},
		{`<table><tr><td style="color:#333;letter-spacing:6px">ABCDEF</td></tr></table>`, "ABCDEF", []Span{{0, 6}}},
		{`<span style="font-size:12px">111111</span>`, "111111", nil},
		{`<span style="font-size:1.5em">2</span><span style="font-size:16pt">3</span>`, "23", []Span{{0, 1}, {1, 2}}},
		{`<p>x</p><h2>
  Code
</h2>`, "x\n\nCode", []Span{{3, 7}}},
		{`<b>   </b>tail`, "tail", nil},
	}
	for _, c := range cases {
		text, _, emph := renderHTML(c.src)
		if text != c.text {
			t.Errorf("renderHTML(%q) text = %q, want %q", c.src, text, c.text)
		}
		if !reflect.DeepEqual(emph, c.want) {
			t.Errorf("renderHTML(%q) emphasis = %v, want %v", c.src, emph, c.want)
		}
	}
}
