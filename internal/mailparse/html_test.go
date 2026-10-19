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
