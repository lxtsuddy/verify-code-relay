package mailparse

import (
	"runtime"
	"strings"
	"testing"
)

// allocated returns the bytes f allocates on the heap.
func allocated(f func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// rawMessage returns a message with a single body part of the given type.
func rawMessage(ctype, body string) []byte {
	return []byte("From: a@example.com\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: " + ctype +
		"; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + body)
}

// A huge body on a single line must not cost many times its size: the watcher
// refetches a message that crashed it, so running out of memory loops forever.
// Before normalisation streamed, a per-rune slice cost ~150x; the HTML parser alone
// costs ~12x, and the position map 8x more.
func TestParseLargeSingleLineMemory(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector defeats sync.Pool, so regexp allocates per match")
	}
	line := strings.Repeat("lorem ipsum dolor ", (1<<20)/18)
	for _, c := range []struct {
		name, ctype, body string
		factor            uint64
	}{
		{"plain", "text/plain", line, 8},
		{"html", "text/html", "<p>" + line + "</p>", 24},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := rawMessage(c.ctype, c.body)
			n := allocated(func() {
				if _, err := Parse(raw); err != nil {
					t.Fatal(err)
				}
			})
			t.Logf("allocated %d bytes for a %d byte body (%.1fx)", n, len(c.body), float64(n)/float64(len(c.body)))
			if n > c.factor*uint64(len(c.body)) {
				t.Fatalf("Parse allocated %d bytes for a %d byte body, want ≤ %dx", n, len(c.body), c.factor)
			}
		})
	}
}
