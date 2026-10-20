package websec

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

var rfcSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))

func TestTOTPRFC6238(t *testing.T) {
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		got, err := TOTPCode(rfcSecret, TOTPStep(time.Unix(unix, 0)))
		if err != nil || got != want {
			t.Errorf("t=%d: %q %v, want %q", unix, got, err, want)
		}
	}
}

func TestVerifyTOTPWindow(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := TOTPStep(now)
	for d, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		code, _ := TOTPCode(rfcSecret, step+d)
		got, match := VerifyTOTP(rfcSecret, code, now)
		if match != ok || (ok && got != step+d) {
			t.Errorf("offset %d: step=%d ok=%v", d, got, match)
		}
	}
	if _, ok := VerifyTOTP(rfcSecret, "12345", now); ok {
		t.Error("5-digit code accepted")
	}
	if _, ok := VerifyTOTP(rfcSecret, "abcdef", now); ok {
		t.Error("non-digit code accepted")
	}
	if _, ok := VerifyTOTP("!!!not base32", "123456", now); ok {
		t.Error("bad secret accepted")
	}
}

func TestNewTOTPSecretAndURI(t *testing.T) {
	s := NewTOTPSecret()
	if len(s) != 32 || strings.Contains(s, "=") {
		t.Fatalf("secret = %q", s)
	}
	if s == NewTOTPSecret() {
		t.Error("secret not random")
	}
	u := TOTPURI(s, "admin", "verify-code-relay")
	if !strings.HasPrefix(u, "otpauth://totp/verify-code-relay:admin?") || !strings.Contains(u, "secret="+s) {
		t.Errorf("uri = %q", u)
	}
	for _, p := range []string{"issuer=verify-code-relay", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(u, p) {
			t.Errorf("uri %q missing %s", u, p)
		}
	}
	png, err := QRPNG(u)
	if err != nil || len(png) < 100 || string(png[1:4]) != "PNG" {
		t.Errorf("qr: %v len=%d", err, len(png))
	}
}
