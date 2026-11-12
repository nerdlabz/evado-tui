package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return ts
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello w…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"héllo wörld", 7, "héllo …"},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.w); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}

func TestPad(t *testing.T) {
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 3); got != "ab…" {
		t.Errorf("pad should truncate, got %q", got)
	}
}

func TestFormatDay(t *testing.T) {
	loc := time.UTC
	now := mustTime(t, "2026-03-10T12:00:00Z") // a Tuesday
	tests := []struct {
		day  string
		want string
	}{
		{"2026-03-10", "Today"},
		{"2026-03-11", "Tomorrow"},
		{"2026-03-09", "Yesterday"},
		{"2026-03-14", "Sat 14 Mar"},
		{"2027-01-05", "5 Jan 2027"},
	}
	for _, tc := range tests {
		d := mustTime(t, tc.day+"T00:00:00Z")
		if got := formatDay(&d, loc, now); got != tc.want {
			t.Errorf("formatDay(%s) = %q, want %q", tc.day, got, tc.want)
		}
	}
	if got := formatDay(nil, loc, now); got != "" {
		t.Errorf("nil day = %q", got)
	}
}

func TestFormatDayUsesAccountLocation(t *testing.T) {
	// 23:30 UTC is already the next day in Tokyo, so "Today" must be
	// computed in the account's zone, not the machine's.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := mustTime(t, "2026-03-10T14:30:00Z") // 23:30 on the 10th in Tokyo
	next := mustTime(t, "2026-03-10T16:00:00Z")
	if got := formatDay(&next, tokyo, now); got != "Tomorrow" {
		t.Errorf("formatDay in Tokyo = %q, want Tomorrow", got)
	}
}

func TestRelative(t *testing.T) {
	now := mustTime(t, "2026-03-10T12:00:00Z")
	ago := func(d time.Duration) *time.Time {
		tm := now.Add(-d)
		return &tm
	}
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{2 * time.Hour, "2 hours ago"},
		{25 * time.Hour, "1 day ago"},
		{3 * 24 * time.Hour, "3 days ago"},
	}
	for _, tc := range tests {
		if got := relative(ago(tc.d), now); got != tc.want {
			t.Errorf("relative(-%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := relative(nil, now); got != "never" {
		t.Errorf("relative(nil) = %q", got)
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("the quick brown fox jumps", 10)
	want := []string{"the quick", "brown fox", "jumps"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapText = %q, want %q", got, want)
	}
	if got := wrapText("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("empty wrap = %q", got)
	}
	if got := wrapText("a\n\nb", 10); len(got) != 3 {
		t.Errorf("blank lines should be kept: %q", got)
	}
}

// A word wider than the line, such as a pasted link in a todo's notes, is
// broken across lines instead of running past the edge of the pane.
func TestWrapTextBreaksAWordWiderThanTheLine(t *testing.T) {
	got := wrapText("see https://example.com/a/long/path ok", 10)
	want := []string{"see", "https://ex", "ample.com/", "a/long/pat", "h ok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapText = %q, want %q", got, want)
	}
	for _, line := range wrapText("日本語のとても長い単語です", 7) {
		if w := visibleWidth(line); w > 7 {
			t.Errorf("line %q is %d cells wide, want at most 7", line, w)
		}
	}
}

func TestPadToKeepsStyledWidth(t *testing.T) {
	// lipgloss.Width counts display cells, so padding a styled string must
	// not count the escape sequences.
	styled := styOK.Render("ok")
	if got := padTo(styled, 6); got != styled+"    " {
		t.Errorf("padTo = %q", got)
	}
}

func TestDayKeyCrossesYears(t *testing.T) {
	a := mustTime(t, "2026-12-31T00:00:00Z")
	b := mustTime(t, "2027-01-01T00:00:00Z")
	if got := dayKey(b) - dayKey(a); got != 1 {
		t.Errorf("dayKey difference across new year = %d, want 1", got)
	}
}

func TestMaskToken(t *testing.T) {
	got := maskToken("evt_abcdefghijklmnop")
	if strings.Contains(got, "ghijkl") {
		t.Errorf("maskToken leaked the middle: %q", got)
	}
	if !strings.HasPrefix(got, "evt_ab") || !strings.HasSuffix(got, "mnop") {
		t.Errorf("maskToken = %q, want the ends kept", got)
	}
	if got := maskToken("short"); got != "*****" {
		t.Errorf("maskToken(short) = %q", got)
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("0123456789"); got != "01234567" {
		t.Errorf("shortID = %q", got)
	}
}

// A link pasted into a todo's notes is shown whole in the detail pane, its
// end on the following lines rather than cut off at the pane's edge.
func TestDetailShowsAllOfALongLinkInNotes(t *testing.T) {
	h := newHarness(t, sampleAPI())
	link := "https://example.com/" + strings.Repeat("segment/", 14) + "the-end"
	todo := api.Todo{ID: "t9", ListID: "l1", Title: "Read this", Notes: &link}
	const w = 60
	var shown []string
	inNotes := false
	for _, line := range strings.Split(stripANSI(h.model.todos.todoCard(todo, nil, true, w, 20)), "\n") {
		if visibleWidth(line) > w {
			t.Errorf("line %q is wider than the pane's %d cells", line, w)
		}
		switch text := strings.TrimSpace(line); {
		case strings.EqualFold(text, "notes"):
			inNotes = true
		case inNotes && text == "":
			inNotes = false
		case inNotes:
			shown = append(shown, text)
		}
	}
	if got := strings.Join(shown, ""); got != link {
		t.Errorf("notes show %q, want the whole link %q", got, link)
	}
}
