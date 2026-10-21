//go:build race

package mailparse

// raceEnabled scales timing bounds and skips allocation bounds: the race detector slows
// allocation-heavy code several times and makes sync.Pool (and so regexp) drop reuse.
const raceEnabled = true
