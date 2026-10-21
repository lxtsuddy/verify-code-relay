package recognize

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lxtsuddy/verify-code-relay/internal/mailparse"
)

type fakeStage struct {
	res    *Result
	ignore bool
	called *int
}

func (f fakeStage) Run(*mailparse.Message, *Trace) (*Result, bool) {
	*f.called++
	return f.res, f.ignore
}

func TestChainOrderAndService(t *testing.T) {
	var a, b int
	c := NewChain(fakeStage{called: &a}, fakeStage{res: &Result{Code: "1"}, called: &b})
	res, ignore, tr := c.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "noreply@accounts.google.com"}})
	if ignore || res == nil || res.Service != "google" || a != 1 || b != 1 || tr == nil {
		t.Fatalf("res=%+v ignore=%v a=%d b=%d", res, ignore, a, b)
	}
}

func TestChainDoesNotModifyStageResult(t *testing.T) {
	var a int
	shared := &Result{Code: "1"}
	c := NewChain(fakeStage{res: shared, called: &a})
	res, _, _ := c.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "noreply@accounts.google.com"}})
	if res == nil || res.Service != "google" {
		t.Fatalf("res = %+v", res)
	}
	if res == shared || shared.Service != "" {
		t.Fatalf("stage result was modified: %+v", shared)
	}
}

func TestChainIgnoreStops(t *testing.T) {
	var a, b int
	c := NewChain(fakeStage{res: &Result{Rule: "custom:x"}, ignore: true, called: &a}, fakeStage{res: &Result{Code: "1"}, called: &b})
	res, ignore, _ := c.Recognize(&mailparse.Message{})
	if !ignore || res.Rule != "custom:x" || b != 0 {
		t.Fatalf("res=%+v ignore=%v b=%d", res, ignore, b)
	}
}

func TestServiceName(t *testing.T) {
	cases := map[mailparse.Address]string{
		{Name: "GitHub", Addr: "noreply@github.com"}: "GitHub",
		{Addr: "no-reply@accounts.google.com"}:       "google",
		{Addr: "service@mail.shop.example.com.tw"}:   "example",
		{Addr: "a@bbc.co.uk"}:                        "bbc",
		{Addr: "weird"}:                              "weird",
	}
	for in, want := range cases {
		if got := ServiceName(in); got != want {
			t.Errorf("ServiceName(%+v) = %q, want %q", in, got, want)
		}
	}
}

// textStage finds a result when the message text contains want, recording each text it saw.
type textStage struct {
	want string
	seen *[]string
}

func (s textStage) Run(msg *mailparse.Message, _ *Trace) (*Result, bool) {
	*s.seen = append(*s.seen, msg.Text)
	if strings.Contains(msg.Text, s.want) {
		return &Result{Code: "1"}, false
	}
	return nil, false
}

func TestChainSecondRoundUsesAltText(t *testing.T) {
	var seen []string
	c := NewChain(textStage{want: "PLAIN", seen: &seen})
	msg := &mailparse.Message{Text: "html", AltText: "PLAIN", Emphasis: []mailparse.Span{{Start: 0, End: 4}}}
	res, _, tr := c.Recognize(msg)
	if res == nil || !reflect.DeepEqual(seen, []string{"html", "PLAIN"}) {
		t.Fatalf("res=%+v seen=%q", res, seen)
	}
	steps := strings.Join(tr.Steps, "\n")
	if !strings.Contains(steps, "第一輪：HTML 內文") || !strings.Contains(steps, "第二輪：純文字內文") {
		t.Fatalf("trace = %q", tr.Steps)
	}
	if msg.Text != "html" || msg.AltText != "PLAIN" || len(msg.Emphasis) != 1 {
		t.Fatalf("message was modified: %+v", msg)
	}
}

func TestChainNoSecondRound(t *testing.T) {
	var seen []string
	c := NewChain(textStage{want: "html", seen: &seen})
	if res, _, _ := c.Recognize(&mailparse.Message{Text: "html", AltText: "PLAIN"}); res == nil || len(seen) != 1 {
		t.Fatalf("first-round result: res=%+v seen=%q", res, seen)
	}
	seen = nil
	res, _, tr := c.Recognize(&mailparse.Message{Text: "nothing"})
	if res != nil || len(seen) != 1 || strings.Contains(strings.Join(tr.Steps, "\n"), "第一輪") {
		t.Fatalf("no AltText: res=%+v seen=%q trace=%q", res, seen, tr.Steps)
	}
}

func TestChainSecondRoundSeesNoEmphasis(t *testing.T) {
	var got []mailparse.Span
	stage := stageFunc(func(msg *mailparse.Message) { got = msg.Emphasis })
	NewChain(stage).Recognize(&mailparse.Message{Text: "a", AltText: "b", Emphasis: []mailparse.Span{{Start: 0, End: 1}}})
	if got != nil {
		t.Fatalf("second round emphasis = %v", got)
	}
}

// stageFunc calls f with every message it is run on and finds nothing.
type stageFunc func(msg *mailparse.Message)

func (f stageFunc) Run(msg *mailparse.Message, _ *Trace) (*Result, bool) {
	f(msg)
	return nil, false
}
