package mailparse

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
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
// Before normalisation streamed, a per-rune slice cost ~150x; now plain costs ~4x and
// HTML ~18x (the parser ~12x, the int32 position map 4x).
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
		{"html", "text/html", "<p>" + line + "</p>", 22},
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

// Thousands of links on one line must not make the text before each link (textBefore)
// quadratic: it is computed once per distinct URL, over a bounded window.
func TestParseManyLinksIsFast(t *testing.T) {
	var urls, anchors, same strings.Builder
	for i := 0; urls.Len() < 1<<20; i++ {
		fmt.Fprintf(&urls, "https://example.com/p/%d ", i)
	}
	for i := 0; anchors.Len() < 1<<20; i++ {
		fmt.Fprintf(&anchors, `<a href="https://example.com/p/%d">x</a>`, i)
	}
	for same.Len() < 1<<20 {
		same.WriteString(`<a href="https://example.com/p">x</a> `)
	}
	limit := 500 * time.Millisecond
	if raceEnabled {
		// The race detector slows parsing down 4× and more, unevenly on a shared CI
		// runner. The bound is only there to catch a quadratic blow-up on 1 MiB of links,
		// which would be slower by far more than this.
		limit *= 10
	}
	for _, c := range []struct{ name, ctype, body string }{
		{"bare urls", "text/plain", urls.String()},
		{"anchors", "text/html", anchors.String()},
		{"one anchor repeated", "text/html", same.String()},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := rawMessage(c.ctype, c.body)
			start := time.Now()
			m, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > limit {
				t.Fatalf("Parse took %s on %d bytes, want < %s", d, len(c.body), limit)
			}
			if len(m.Links) > maxLinks {
				t.Fatalf("kept %d links, want ≤ %d", len(m.Links), maxLinks)
			}
		})
	}
}
