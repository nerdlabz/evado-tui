package local

import (
	"context"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// A time that has already passed today stays on today when the words said
// today, and moves to tomorrow when they named no day. Either reading can be
// asked for instead.
func TestParseATimeThatHasPassedToday(t *testing.T) {
	loc := time.UTC
	at := func(hour int) time.Time { return time.Date(2026, 10, 1, hour, 0, 0, 0, loc) }
	for _, c := range []struct {
		in     string
		now    time.Time
		choice string
		when   string
		passed string
	}{
		{"Call Steven today at 3pm", at(16), "", "2026-10-01 15:00", Kept},
		{"Call Steven today at 3pm", at(16), Moved, "2026-10-02 15:00", Moved},
		{"Call Steven today at 3pm", at(14), "", "2026-10-01 15:00", ""},
		{"Call Steven today at 3pm", at(14), Moved, "2026-10-01 15:00", ""},
		{"Call Steven at 3pm", at(16), "", "2026-10-02 15:00", Moved},
		{"Call Steven at 3pm", at(16), Kept, "2026-10-01 15:00", Kept},
		{"Call Steven at 3pm", at(14), Kept, "2026-10-01 15:00", ""},
		{"Dinner tonight at 8", at(21), "", "2026-10-01 20:00", Kept},
		{"Call Sam this afternoon at 2", at(16), "", "2026-10-01 14:00", Kept},
		{"Call at 8 tonight", at(22), "", "2026-10-01 20:00", Kept},
		// A time typed without am/pm says which half it took as well.
		{"Call today at 9", at(22), "", "2026-10-01 09:00", Kept},
		{"Call at 9", at(22), "", "2026-10-02 09:00", Moved},
		// Tomorrow and a weekday are days of their own, never kept or moved,
		// and a weekday that is today goes to next week.
		{"Call tomorrow at 3pm", at(16), Kept, "2026-10-02 15:00", ""},
		{"Call thursday at 3pm", at(16), "", "2026-10-08 15:00", ""},
		{"Call thursday at 3pm", at(16), Kept, "2026-10-08 15:00", ""},
		// A repeat's first one is always ahead.
		{"Stretch daily at 3pm", at(16), "", "2026-10-02 15:00", ""},
		{"Stretch daily at 3pm", at(16), Kept, "2026-10-02 15:00", ""},
		// Midnight is the one coming.
		{"Call at midnight", at(16), "", "2026-10-02 00:00", ""},
		{"Call today at midnight", at(16), Kept, "2026-10-02 00:00", ""},
		// A day with no time has nothing to pass.
		{"Call today", at(16), Moved, "2026-10-01 00:00", ""},
	} {
		got := ParseWith(c.in, loc, c.now, Options{TimePassed: c.choice})
		when := ""
		if got.DoAt != nil {
			when = got.DoAt.In(loc).Format("2006-01-02 15:04")
		}
		if when != c.when || got.TimePassed != c.passed {
			t.Errorf("%q at %s asking %q: %s passed %q; want %s passed %q",
				c.in, c.now.Format("15:04"), c.choice, when, got.TimePassed, c.when, c.passed)
		}
	}
}

// A date written in numbers reads month first, or day first for an account
// that reads them so. A number that cannot be a month reads the other way
// in either, and words read the same in both.
func TestParseNumericDateOrder(t *testing.T) {
	now := time.Date(2026, 10, 1, 5, 53, 0, 0, time.UTC)
	for _, c := range []struct{ in, order, day string }{
		{"Dentist 3/4", "", "2027-03-04"},
		{"Dentist 3/4", "mdy", "2027-03-04"},
		{"Dentist 3/4", "dmy", "2027-04-03"},
		{"Pay rent 13/12", "mdy", "2026-12-13"},
		{"Pay rent 13/12", "dmy", "2026-12-13"},
		{"Submit on 10/15", "dmy", "2026-10-15"},
		{"Meeting 4.3.2027", "mdy", "2027-04-03"},
		{"Meeting 4.3.2027", "dmy", "2027-03-04"},
		{"Meeting 4-3-2027", "dmy", "2027-03-04"},
		{"Submit taxes April 14", "dmy", "2027-04-14"},
		{"Launch 2026-11-05", "dmy", "2026-11-05"},
		// Dots with no year are not a date: this is a version, or a time.
		{"Ship 4.3", "dmy", ""},
	} {
		got := ParseWith(c.in, time.UTC, now, Options{DateOrder: c.order})
		day := ""
		if got.DoAt != nil {
			day = got.DoAt.Format("2006-01-02")
		}
		if day != c.day {
			t.Errorf("%q read %s: %q; want %q", c.in, c.order, day, c.day)
		}
	}
	if got := Parse("Call at 3.30pm", time.UTC, now); got.DoAt == nil || got.DoAt.Format("15:04") != "15:30" || got.Title != "Call" {
		t.Errorf("Call at 3.30pm = %+v; want 15:30", got)
	}
}

// The local backend answers /parse with what became of a passed time, takes
// the other reading when asked, and refuses a choice it does not know.
func TestPreviewSaysWhenATimeHasPassed(t *testing.T) {
	b := open(t, "", "")
	loc := b.loc()
	b.now = func() time.Time { return time.Date(2031, 3, 3, 16, 0, 0, 0, loc) }
	c := b.Client()
	ctx := context.Background()

	p, err := c.Parse(ctx, "Call Steven today at 3pm")
	if err != nil || p.Passed() != api.TimeKept || p.DoAt == nil || !p.DoAt.Equal(time.Date(2031, 3, 3, 15, 0, 0, 0, loc)) {
		t.Errorf("today at 3pm at 4 PM = %+v, %v; want kept on today", p, err)
	}
	p, err = c.ParseChoosing(ctx, "Call Steven today at 3pm", nil, api.TimeMoved)
	if err != nil || p.Passed() != api.TimeMoved || p.DoAt == nil || !p.DoAt.Equal(time.Date(2031, 3, 4, 15, 0, 0, 0, loc)) {
		t.Errorf("asking for moved = %+v, %v; want tomorrow", p, err)
	}
	p, err = c.Parse(ctx, "Call Steven tomorrow at 3pm")
	if err != nil || p.TimePassed != nil {
		t.Errorf("tomorrow at 3pm = %+v, %v; want no passed time", p, err)
	}
	if _, err := c.ParseChoosing(ctx, "Call Steven today at 3pm", nil, "later"); api.CodeOf(err) != "invalid_request" {
		t.Errorf("an unknown choice: %v; want invalid_request", err)
	}
}

// A todo kept on today at a time that has passed gets no reminder, since one
// would go off at once. Moved to tomorrow it is reminded as usual, and a
// reminder the request gives is kept either way.
func TestATodoKeptOnTodayAfterItsTimeHasNoReminder(t *testing.T) {
	b := open(t, "", "")
	loc := b.loc()
	b.now = func() time.Time { return time.Date(2031, 3, 3, 16, 0, 0, 0, loc) }
	c := b.Client()
	ctx := context.Background()
	three := time.Date(2031, 3, 3, 15, 0, 0, 0, loc)
	add := func(n api.NewTodo) api.Todo {
		t.Helper()
		made, err := c.CreateTodos(ctx, n)
		if err != nil || len(made) != 1 {
			t.Fatalf("create %+v: %v, %v", n, made, err)
		}
		return made[0]
	}

	kept := add(api.NewTodo{Title: "Call Steven today at 3pm"})
	if kept.DoAt == nil || !kept.DoAt.Equal(three) || kept.RemindAt != nil {
		t.Errorf("kept on today: doAt %v remindAt %v; want 3 PM today and no reminder", kept.DoAt, kept.RemindAt)
	}
	moved := add(api.NewTodo{Title: "Call Steven at 3pm"})
	if next := three.AddDate(0, 0, 1); moved.DoAt == nil || !moved.DoAt.Equal(next) || moved.RemindAt == nil || !moved.RemindAt.Equal(next) {
		t.Errorf("moved to tomorrow: doAt %v remindAt %v; want both 3 PM tomorrow", moved.DoAt, moved.RemindAt)
	}
	asked := add(api.NewTodo{Title: "Call Steven at 3pm", TimePassed: api.TimeKept})
	if asked.DoAt == nil || !asked.DoAt.Equal(three) || asked.RemindAt != nil {
		t.Errorf("asked to keep: doAt %v remindAt %v; want 3 PM today and no reminder", asked.DoAt, asked.RemindAt)
	}
	other := add(api.NewTodo{Title: "Call Steven today at 3pm", TimePassed: api.TimeMoved})
	if other.DoAt == nil || !other.DoAt.Equal(three.AddDate(0, 0, 1)) || other.RemindAt == nil {
		t.Errorf("asked to move: doAt %v remindAt %v; want 3 PM tomorrow with a reminder", other.DoAt, other.RemindAt)
	}
	at := time.Date(2031, 3, 3, 18, 0, 0, 0, loc).Format(time.RFC3339)
	own := add(api.NewTodo{Title: "Call Steven today at 3pm", RemindAt: &at})
	if own.RemindAt == nil || own.RemindAt.Format(time.RFC3339) != at {
		t.Errorf("with its own reminder: remindAt %v; want %s", own.RemindAt, at)
	}
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Call Steven today at 3pm", TimePassed: "later"}); api.CodeOf(err) != "invalid_request" {
		t.Errorf("an unknown choice: %v; want invalid_request", err)
	}
}

// An account that reads dates day first has them read that way here too.
func TestCaptureReadsNumericDatesAsTheAccountDoes(t *testing.T) {
	b := open(t, "", "")
	loc := b.loc()
	b.now = func() time.Time { return time.Date(2031, 3, 3, 16, 0, 0, 0, loc) }
	b.st.User.DateOrder = "dmy"
	p, err := b.Client().Parse(context.Background(), "Dentist 5/6")
	if err != nil || p.DoAt == nil || p.DoAt.In(loc).Format("2006-01-02") != "2031-06-05" {
		t.Errorf("5/6 day first = %+v, %v; want 5 June", p, err)
	}
}
