// Package tglen measures text the way Telegram limits it: in UTF-16 code units.
//
// Text is measured as given; HTML tags and entities are not stripped or decoded, so
// measuring an HTML message over-estimates what Telegram counts after parsing.
package tglen

// Max is Telegram's sendMessage text limit, in UTF-16 code units.
const Max = 4096

// runeLen is the number of UTF-16 code units r encodes to. Invalid UTF-8 decodes to
// U+FFFD, which is one unit.
func runeLen(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// Len returns the length of s in UTF-16 code units.
func Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeLen(r)
	}
	return n
}

// Cut returns the longest prefix of s that is at most n UTF-16 code units long. It cuts
// between runes, so a surrogate pair is never split.
func Cut(s string, n int) string {
	used := 0
	for i, r := range s {
		used += runeLen(r)
		if used > n {
			return s[:i]
		}
	}
	return s
}
