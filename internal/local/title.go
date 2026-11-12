package local

import "strings"

// MaxTitle is the longest title the service keeps, for a todo and for a
// checklist step, in UTF-16 code units as JavaScript counts them.
const MaxTitle = 500

// StoredTitle is a todo or step title as the service stores it: trimmed,
// then cut to MaxTitle UTF-16 code units. A title edited here has to come
// back from the service the way it went up, or the next pull sees a change
// nobody made. The service trims what it is sent before it cuts, so a cut
// that leaves a space last would come back one shorter: the end is trimmed
// again here, which makes the result a title the service keeps as it is. A
// character that would be cut in half is dropped whole, so the result is
// valid text.
func StoredTitle(s string) string {
	s = strings.TrimFunc(s, isJSSpace)
	units := 0
	for i, r := range s {
		n := 1
		if r >= 0x10000 {
			n = 2
		}
		if units+n > MaxTitle {
			return strings.TrimRightFunc(s[:i], isJSSpace)
		}
		units += n
	}
	return s
}

// isJSSpace is what JavaScript's trim removes. It differs from
// unicode.IsSpace at U+0085, which trim keeps, and U+FEFF, which it removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// TextUnits counts lengths the way the service's JavaScript schemas do.
func TextUnits(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}
