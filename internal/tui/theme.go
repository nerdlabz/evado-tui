// Package tui contains the terminal user interface for Evado.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// truncate shortens a styled line to w display cells, adding an ellipsis
// when it has to cut. ANSI escape sequences are copied through without
// counting towards the width, so a truncated line never loses the middle
// of a colour code and never gains width from the codes themselves.
func truncate(s string, w int) string {
	if w <= 0 {
		return zoneMarks(s)
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…" + zoneMarks(s)
	}
	var b strings.Builder
	used := 0
	for i := 0; i < len(s); {
		n, esc := escapeLen(s[i:])
		if esc {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		size := decodeRune(s[i:])
		rw := runeCells(s[i : i+size])
		if used+rw > w-1 {
			b.WriteString("…" + zoneMarks(s[i:]))
			return b.String()
		}
		b.WriteString(s[i : i+size])
		used += rw
		i += size
	}
	return b.String() + "…"
}

// escapeLen reports the byte length of the ANSI escape sequence at the
// start of s, or 0 when s does not start with one.
func escapeLen(s string) (int, bool) {
	if len(s) < 2 || s[0] != 0x1b {
		return 0, false
	}
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then a final byte
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1, true
			}
		}
		return len(s), true
	case ']': // OSC: terminated by BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1, true
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, true
			}
		}
		return len(s), true
	default:
		return 2, true
	}
}

// visibleWidth is the display width of s, ignoring escape sequences.
func visibleWidth(s string) int { return lipgloss.Width(s) }

// decodeRune returns the byte length of the first rune in s, never zero.
func decodeRune(s string) int {
	if s == "" {
		return 0
	}
	_, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return 1
	}
	return size
}

// runeCells is the terminal width of a single rune or string.
func runeCells(s string) int { return lipgloss.Width(s) }

// padLines pads every line of a block to w display cells, so a box built
// around styled text keeps a straight edge.
func padLines(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = padTo(l, w)
	}
	return strings.Join(lines, "\n")
}

// pad right-pads s with spaces to exactly w display cells.
func pad(s string, w int) string {
	n := visibleWidth(s)
	if n >= w {
		return truncate(s, w)
	}
	return s + strings.Repeat(" ", w-n)
}

// formatDay renders a calendar day the way a person would say it:
// Today, Tomorrow, Yesterday, Fri 12 Sep, or 12 Sep 2027.
func formatDay(day *time.Time, loc *time.Location, today time.Time) string {
	if day == nil {
		return ""
	}
	d := day.In(loc)
	t := today.In(loc)
	// Compare day-of-year in the account's zone: dy is today, dd is the
	// day being rendered.
	dy, dd := t.YearDay(), d.YearDay()
	switch {
	case d.Year() == t.Year() && dd == dy:
		return "Today"
	case d.Year() == t.Year() && dd == dy+1:
		return "Tomorrow"
	case d.Year() == t.Year() && dd == dy-1:
		return "Yesterday"
	}
	if d.Year() == t.Year() {
		return d.Format("Mon 2 Jan")
	}
	return d.Format("2 Jan 2006")
}

// dayLabel renders a YYYY-MM-DD day string the way a person would say it.
func dayLabel(day *string, loc *time.Location, now time.Time) string {
	if day == nil {
		return ""
	}
	t, err := time.ParseInLocation("2006-01-02", *day, loc)
	if err != nil {
		return *day
	}
	return formatDay(&t, loc, now)
}

func (m *Model) dayLabel(day *string) string { return dayLabel(day, m.loc, m.now) }

// hstack puts right at the end of a line of exactly width cells, giving
// the left-hand text whatever is left. When there is not enough room the
// left side is shortened rather than letting the line overflow.
func hstack(left, right string, width int) string {
	lw, rw := visibleWidth(left), visibleWidth(right)
	if lw+rw+1 <= width {
		return left + strings.Repeat(" ", max(width-lw-rw, 0)) + right
	}
	if rw >= width {
		return truncate(right, width)
	}
	return truncate(left, max(width-rw-1, 0)) + " " + right
}

// dayKey counts whole days from the epoch for a calendar date, ignoring
// the time of day and any DST shift, so subtracting two keys gives the
// number of days between them even across year boundaries.
func dayKey(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// formatClock renders the time of day, or an empty string for a date-only
// todo.
func formatClock(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("15:04")
}

// formatStamp renders a full timestamp for detail panes and tooltips.
func formatStamp(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("Mon 2 Jan 2006, 15:04")
}

// relative renders how long ago t was, for last-used and sync cursors.
func relative(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	}
	return t.Format("2 Jan 2006")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// shortID trims an id to something readable in a list.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// dataDir is where exports and cached state land.
func dataDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv("EVADO_DATA_DIR")); d != "" {
		return d, nil
	}
	if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
		return filepath.Join(d, "evado"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "evado"), nil
}

// toastText is the current status message, for tests and logging.
func (m *Model) toastText() string {
	if m.toast == nil {
		return ""
	}
	return m.toast.text
}
