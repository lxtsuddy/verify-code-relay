//go:build race

package recognize

// raceEnabled scales timing bounds: the race detector slows allocation-heavy code several times.
const raceEnabled = true
