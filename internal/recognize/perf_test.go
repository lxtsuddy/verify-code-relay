package recognize

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

// A hostile or merely huge message must not stall a watcher: the code finder has
// to stay roughly linear in the number of candidates.
func TestRecognizeLargeInputIsFast(t *testing.T) {
	var oneLine strings.Builder
	oneLine.WriteString("Your code is below\n")
	for i := 0; i < 32000; i++ {
		fmt.Fprintf(&oneLine, "%06d ", 100000+i)
	}
	var otpLines strings.Builder
	for i := 0; i < 32000; i++ {
		fmt.Fprintf(&otpLines, "OTP %06d\n", 100000+i)
	}
	chain, err := New(nil, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	limit := 500 * time.Millisecond
	if raceEnabled {
		limit *= 4
	}
	for name, text := range map[string]string{"one line": oneLine.String(), "otp lines": otpLines.String()} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			chain.Recognize(&mailparse.Message{Subject: "Security notice", Text: text})
			if d := time.Since(start); d > limit {
				t.Fatalf("Recognize took %s on %d bytes, want < %s", d, len(text), limit)
			}
		})
	}
}
