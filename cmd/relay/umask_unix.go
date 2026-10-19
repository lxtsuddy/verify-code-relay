//go:build unix

package main

import "syscall"

// restrictUmask makes SQLite's -wal/-shm side files private like the main db file.
func restrictUmask() { syscall.Umask(0o077) }
