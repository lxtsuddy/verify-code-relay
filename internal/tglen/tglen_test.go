package tglen

import "testing"

func TestLen(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"驗證碼", 3},
		{"😀", 2},               // outside the BMP: a surrogate pair
		{"⚠️", 2},              // U+26A0 U+FE0F, both in the BMP
		{"👨‍👩‍👧", 8},           // 3 astral emoji + 2 ZWJ
		{"a\xffb", 3},          // an invalid byte counts as one U+FFFD
		{"&amp;😀長", 5 + 2 + 1}, // counted as-is, entities are not decoded
	} {
		if got := Len(tc.s); got != tc.want {
			t.Errorf("Len(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abc", 3, "abc"},
		{"abc", 2, "ab"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"a😀b", 3, "a😀"},
		{"a😀b", 2, "a"}, // never splits a surrogate pair
		{"a😀b", 1, "a"},
		{"長長長", 2, "長長"},
	} {
		got := Cut(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
		if Len(got) > max(tc.n, 0) {
			t.Errorf("Cut(%q, %d) is %d units", tc.s, tc.n, Len(got))
		}
	}
}
