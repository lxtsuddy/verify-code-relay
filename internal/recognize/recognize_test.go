package recognize

import (
	"testing"

	"verify-code-relay/internal/mailparse"
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
