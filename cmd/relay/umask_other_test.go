//go:build !unix

package main

import "testing"

func keepUmask(*testing.T) {}

// umaskNow: no umask outside unix; report the value run would have set.
func umaskNow() int { return 0o077 }
