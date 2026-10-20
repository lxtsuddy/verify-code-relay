package recognize

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type span struct{ start, end int } // rune offsets, end exclusive

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func isASCIILetter(r rune) bool { return r < utf8.RuneSelf && (r|0x20) >= 'a' && (r|0x20) <= 'z' }

// findKeywords returns every occurrence of every keyword in lower. Keywords that
// start/end with an ASCII letter must not touch another ASCII letter (so "otp"
// does not match "hotpot").
func findKeywords(lower []rune, keywords [][]rune) []span {
	var out []span
	for _, kw := range keywords {
		if len(kw) == 0 {
			continue
		}
		for i := 0; i+len(kw) <= len(lower); i++ {
			if keywordAt(lower, kw, i) {
				out = append(out, span{i, i + len(kw)})
			}
		}
	}
	return out
}

// keywordAt reports whether kw occurs in lower at i, respecting the ASCII-letter
// boundary rule of findKeywords.
func keywordAt(lower, kw []rune, i int) bool {
	if !runesEqual(lower[i:i+len(kw)], kw) {
		return false
	}
	if isASCIILetter(kw[0]) && i > 0 && isASCIILetter(lower[i-1]) {
		return false
	}
	end := i + len(kw)
	return !(isASCIILetter(kw[len(kw)-1]) && end < len(lower) && isASCIILetter(lower[end]))
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toRuneKeywords(words []string) [][]rune {
	out := make([][]rune, 0, len(words))
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			out = append(out, []rune(w))
		}
	}
	return out
}

// byteToRune maps every byte offset of s (0..len(s)) to its rune offset.
func byteToRune(s string) []int {
	m := make([]int, len(s)+1)
	r := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:]) // invalid bytes: size 1, same as []rune(s)
		for j := 0; j < size; j++ {
			m[i+j] = r
		}
		i += size
		r++
	}
	m[len(s)] = r
	return m
}

func regexpSpans(re *regexp.Regexp, s string, b2r []int) []span {
	var out []span
	for _, m := range re.FindAllStringIndex(s, -1) {
		out = append(out, span{b2r[m[0]], b2r[m[1]]})
	}
	return out
}
