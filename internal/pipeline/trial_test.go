package pipeline

import (
	"strings"
	"testing"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/recognize"
	"github.com/lxtsuddy/verify-code-relay/internal/render"
)

func trialDeps(t *testing.T, rules []config.Rule) (*recognize.Chain, *render.Renderer) {
	t.Helper()
	rec, err := recognize.New(rules, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rnd, err := render.New(config.Empty().Defaulted())
	if err != nil {
		t.Fatal(err)
	}
	return rec, rnd
}

const trialMail = "From: Google <noreply@google.com>\r\nTo: bar@outlook.com\r\nSubject: Use 123456 to set up recovery email\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nUse this code to finish:\r\n123456\r\n"

func TestRunTrialCode(t *testing.T) {
	rec, rnd := trialDeps(t, nil)
	tr, err := RunTrial([]byte(trialMail), rec, rnd, Mailbox{DisplayName: "（測試）"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Kind != "code" || tr.Result.Code != "123456" || tr.Output == nil || !strings.Contains(tr.Output.Text, "（測試）") {
		t.Fatalf("trial = %+v output = %+v", tr, tr.Output)
	}
	if len(tr.Trace.Steps) == 0 || tr.Msg.Subject != "Use 123456 to set up recovery email" {
		t.Fatalf("trace %q subject %q", tr.Trace.Steps, tr.Msg.Subject)
	}
}

func TestRunTrialIgnoredAndNone(t *testing.T) {
	rec, rnd := trialDeps(t, []config.Rule{{Name: "g", From: "*@google.com", Ignore: true}})
	tr, err := RunTrial([]byte(trialMail), rec, rnd, Mailbox{})
	if err != nil || tr.Kind != "ignored" || tr.Output != nil || tr.Result.Rule != "custom:g" {
		t.Fatalf("ignored: %+v %v", tr, err)
	}
	rec, rnd = trialDeps(t, nil)
	tr, err = RunTrial([]byte("From: a@b.c\r\nSubject: hi\r\n\r\nhello\r\n"), rec, rnd, Mailbox{})
	if err != nil || tr.Kind != "none" || tr.Output != nil || tr.Result != nil {
		t.Fatalf("none: %+v %v", tr, err)
	}
}

func TestRunTrialParseError(t *testing.T) {
	rec, rnd := trialDeps(t, nil)
	if tr, err := RunTrial([]byte("Content-Transfer-Encoding: bogus\r\n\r\nx"), rec, rnd, Mailbox{}); err == nil || tr != nil {
		t.Fatalf("want a parse error, got %+v %v", tr, err)
	}
}

// The link button carries the mail's own link text, and the text shows the host, not the URL.
func TestRunTrialLinkButtonText(t *testing.T) {
	rec, rnd := trialDeps(t, nil)
	mail := "From: Example <no-reply@example.com>\r\nSubject: Confirm your email\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<p>Please confirm your email address.</p><a href="https://example.com/verify?token=Zx8_kq2LmN0pQr7sTuVwXy">Confirm email</a>` + "\r\n"
	tr, err := RunTrial([]byte(mail), rec, rnd, Mailbox{DisplayName: "（測試）"})
	if err != nil || tr.Kind != "link" || tr.Output == nil {
		t.Fatalf("trial = %+v, %v", tr, err)
	}
	if !strings.Contains(tr.Output.ReplyMarkup, `"text":"🔗 Confirm email"`) {
		t.Errorf("markup = %s", tr.Output.ReplyMarkup)
	}
	if strings.Contains(tr.Output.Text, "token=") || !strings.Contains(tr.Output.Text, "🌐 example.com") {
		t.Errorf("text = %q", tr.Output.Text)
	}
}
