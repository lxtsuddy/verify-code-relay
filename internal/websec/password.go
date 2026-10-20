// Package websec holds the security primitives used by the web admin:
// password hashing, TOTP, QR codes, login rate limiting, client IP
// resolution and random tokens.
package websec

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// MinPasswordLen is the minimum accepted admin password length.
const MinPasswordLen = 12

const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB (OWASP minimum for argon2id)
	argonThreads = 1
	argonKeyLen  = 32

	// Upper bounds applied when parsing a stored hash, so a corrupted value
	// cannot make VerifyPassword allocate unbounded memory or burn CPU.
	maxArgonMemory  = 256 * 1024 // KiB
	maxArgonTime    = 10
	maxArgonThreads = 8
	minSaltLen      = 8
	minKeyLen       = 16
	maxFieldLen     = 1024
)

// hashSem serialises hashing so concurrent logins cannot multiply memory use.
var hashSem = make(chan struct{}, 1)

var errBadHash = errors.New("websec: 密碼雜湊格式不正確")

func derive(pw string, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	hashSem <- struct{}{}
	defer func() { <-hashSem }()
	return argon2.IDKey([]byte(pw), salt, t, m, p, keyLen)
}

// HashPassword returns an argon2id PHC string ($argon2id$v=19$m=19456,t=2,p=1$salt$hash).
// Hashing is serialised (one at a time) to bound memory use.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := derive(pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches; malformed hashes return an error.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if n, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || n != 1 || version != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 {
		return false, errBadHash
	}
	if m == 0 || m > maxArgonMemory || t == 0 || t > maxArgonTime || p == 0 || p > maxArgonThreads {
		return false, errBadHash
	}
	if len(parts[4]) > maxFieldLen || len(parts[5]) > maxFieldLen {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < minKeyLen {
		return false, errBadHash
	}
	got := derive(pw, salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
