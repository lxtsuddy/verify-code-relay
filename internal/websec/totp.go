package websec

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 20 random bytes encoded as unpadded base32.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic("websec: crypto/rand failed: " + err.Error())
	}
	return b32.EncodeToString(b)
}

// TOTPStep returns the RFC 6238 time step (30 s) containing t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// TOTPCode returns the 6-digit HMAC-SHA1 code for the given step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return "", errors.New("websec: TOTP 金鑰格式不正確")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1000000), nil
}

// VerifyTOTP checks code against steps now-1..now+1 and returns the matching step.
func VerifyTOTP(secret, code string, now time.Time) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return 0, false
		}
	}
	cur := TOTPStep(now)
	var matched int64
	found := false
	for d := int64(-1); d <= 1; d++ {
		want, err := TOTPCode(secret, cur+d)
		if err != nil {
			return 0, false
		}
		// no early exit: every step is compared regardless of earlier matches
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && !found {
			matched, found = cur+d, true
		}
	}
	return matched, found
}

// TOTPURI builds the otpauth:// provisioning URI for authenticator apps.
func TOTPURI(secret, account, issuer string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	// Encode() turns spaces into '+', which some authenticator apps keep literally.
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + query
}
