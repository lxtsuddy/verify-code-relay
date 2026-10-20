package web

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
)

func TestFieldErrors(t *testing.T) {
	fe := fieldErrors(errors.New("mailboxes.a.host: 不可為空\ntemplates.code: template: x: bad\n奇怪的錯誤"))
	if fe["mailboxes.a.host"] != "不可為空" || fe["templates.code"] != "template: x: bad" || fe[""] != "奇怪的錯誤" {
		t.Fatalf("fe = %v", fe)
	}
}

func TestFieldErrorsEdgeCases(t *testing.T) {
	fe := fieldErrors(errors.New("rules[0].code_regex: 正規表示式錯誤：x\nrules[0]: 至少需要 from 或 subject\n解析設定：第 3 行: 壞掉\n其一\n其二\n\nrules[0].code_regex: 第二個"))
	if fe["rules[0].code_regex"] != "正規表示式錯誤：x\n第二個" {
		t.Errorf("same path twice = %q", fe["rules[0].code_regex"])
	}
	if fe["rules[0]"] != "至少需要 from 或 subject" {
		t.Errorf("rules[0] = %q", fe["rules[0]"])
	}
	if fe[""] != "解析設定：第 3 行: 壞掉\n其一\n其二" {
		t.Errorf("unmapped = %q", fe[""])
	}
	if fieldErrors(nil) != nil {
		t.Error("nil error must give nil")
	}
	var fe2 FieldErrors
	if !errors.As(fmt.Errorf("wrapped: %w", FieldErrors{"admin": "x"}), &fe2) || fe2["admin"] != "x" {
		t.Error("FieldErrors must work with errors.As")
	}
	if got := (FieldErrors{"b": "2", "": "0", "a": "1"}).Error(); got != "0\na: 1\nb: 2" {
		t.Errorf("Error() = %q", got)
	}
}

func TestFieldErrorsOnly(t *testing.T) {
	fe := FieldErrors{"admin": "找不到", "mailboxes.x.host": "不可為空", "": "頂端"}
	got := fe.only("admin", "log_level")
	if got["admin"] != "找不到" || got[""] != "頂端\nmailboxes.x.host: 不可為空" || len(got) != 2 {
		t.Fatalf("only = %v", got)
	}
}

func TestParseDuration(t *testing.T) {
	if d, err := parseDuration(""); err != nil || d.Set {
		t.Errorf("empty = %+v %v", d, err)
	}
	if d, err := parseDuration(" 1h30m "); err != nil || !d.Set || d.Duration != 90*time.Minute {
		t.Errorf("1h30m = %+v %v", d, err)
	}
	if d, err := parseDuration("0s"); err != nil || !d.Set || d.Duration != 0 {
		t.Errorf("0s = %+v %v (Validate rejects it, not the parser)", d, err)
	}
	for _, bad := range []string{"10", "0", "+0", "0.5", "abc", "5 分鐘"} {
		if _, err := parseDuration(bad); err == nil || !strings.Contains(err.Error(), "請輸入例如 10m、30s、1h") {
			t.Errorf("parseDuration(%q) err = %v", bad, err)
		}
	}
	if durationValue(config.Duration{}) != "" {
		t.Error("unset duration must be empty")
	}
}

func TestMutate(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	_, before, _ := runtime.LoadConfig(ctx, e.st)
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "203.0.113.9:5555"

	// fn rejects: its FieldErrors come back unchanged and nothing is stored
	fe, err := e.srv.mutate(r, "x", func(c *config.Config) error { return FieldErrors{"admin": "不行"} })
	if err != nil || fe["admin"] != "不行" {
		t.Fatalf("fn reject = %v %v", fe, err)
	}
	// fn fails otherwise: a plain error
	if fe, err := e.srv.mutate(r, "x", func(c *config.Config) error { return errors.New("boom") }); err == nil || fe != nil {
		t.Fatalf("fn error = %v %v", fe, err)
	}
	// validation failure
	fe, err = e.srv.mutate(r, "x", func(c *config.Config) error { c.Admin = "nobody"; return nil })
	if err != nil || !strings.Contains(fe["admin"], "找不到目標") {
		t.Fatalf("invalid = %v %v", fe, err)
	}
	if _, id, _ := runtime.LoadConfig(ctx, e.st); id != before {
		t.Fatal("a rejected change stored a version")
	}

	// success: stored with meta and applied
	fe, err = e.srv.mutate(r, "修改日誌等級", func(c *config.Config) error { c.LogLevel = "debug"; return nil })
	if err != nil || fe != nil {
		t.Fatalf("ok = %v %v", fe, err)
	}
	v, _, _ := e.st.LatestConfigVersion(ctx)
	if v.ID != before+1 || v.Source != "web" || v.IP != "203.0.113.9" || v.Summary != "修改日誌等級" || !v.CreatedAt.Equal(e.now) {
		t.Errorf("version = %+v", v)
	}
	if cur := e.rt.Current(); cur == nil || cur.Config.LogLevel != "debug" {
		t.Error("change not applied")
	}
}

func TestMutateSerialised(t *testing.T) {
	e := newEnv(t, nil)
	r := httptest.NewRequest("POST", "/x", nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// read-modify-write: without serialisation two calls read the same list and one append is lost
			e.srv.mutate(r, "add", func(c *config.Config) error {
				c.Recognize.ExtraCodeKeywords = append(c.Recognize.ExtraCodeKeywords, fmt.Sprintf("k%d", len(c.Recognize.ExtraCodeKeywords)))
				return nil
			})
		}()
	}
	wg.Wait()
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if n := len(raw.Recognize.ExtraCodeKeywords); n != 8 {
		t.Fatalf("keywords = %d, want 8 (lost updates)", n)
	}
}

func TestDurationValue(t *testing.T) {
	for d, want := range map[time.Duration]string{
		24 * time.Hour: "24h", 5 * time.Minute: "5m", 90 * time.Minute: "1h30m", 30 * time.Second: "30s",
		0: "0s", time.Hour + 5*time.Second: "1h0m5s", 10*time.Minute + 30*time.Second: "10m30s", 1500 * time.Millisecond: "1.5s",
	} {
		got := durationValue(config.Duration{Duration: d, Set: true})
		if got != want {
			t.Errorf("durationValue(%v) = %q, want %q", d, got, want)
		}
		if back, err := parseDuration(got); err != nil || back.Duration != d {
			t.Errorf("%q does not parse back to %v: %v %v", got, d, back, err)
		}
	}
}
