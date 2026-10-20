//go:build unix

package main

import (
	"syscall"
	"testing"
)

// keepUmask sets a known umask (022) for the test and restores the original one
// afterwards: run and login call restrictUmask, which changes it for the whole
// process. Tests using it must not call t.Parallel.
func keepUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

// umaskNow reports the process umask (briefly setting it to 0 to read it).
func umaskNow() int {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return m
}

func TestRestrictUmask(t *testing.T) {
	keepUmask(t)
	restrictUmask()
	if got := syscall.Umask(0o077); got != 0o077 {
		t.Fatalf("umask = %#o, want 077", got)
	}
}
