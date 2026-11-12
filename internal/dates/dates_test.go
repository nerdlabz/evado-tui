package dates

import (
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

func TestNormalise(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"2026-03-10", "2026-03-10", true},
		{"2026-03-10T15:04", "2026-03-10T15:04", true},
		{"2026-03-10 15:04", "2026-03-10T15:04", true},
		{"2026/03/10", "2026-03-10", true},
		{"2026-03-10T15:04:00Z", "2026-03-10T15:04:00Z", true},
		{"15:04", "15:04", true},
		{"3pm", "15:00", true},
		{"friday", "", false},
		{"next monday 9am", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := Normalise(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Normalise(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFromParse(t *testing.T) {
	loc := time.UTC
	at := time.Date(2026, 3, 13, 17, 0, 0, 0, time.UTC)
	day := "2026-03-13"
	pv := api.ParsePreview{ScheduledDay: &day, DoAt: &at, HasTime: true}
	for _, field := range []string{Day, DoAt, DueAt, Remind} {
		got, dateOnly, ok := FromParse(field, pv, loc)
		want := "2026-03-13T17:00:00Z"
		if field == Day {
			want = "2026-03-13"
		}
		if !ok || dateOnly || got != want {
			t.Errorf("%s: got %q,%v,%v want %q", field, got, dateOnly, ok, want)
		}
	}
	if _, _, ok := FromParse(DueAt, api.ParsePreview{}, loc); ok {
		t.Error("nothing parsed should fill nothing")
	}
}

// Words that name only a day, as the service reads "friday": a do date at
// midnight on the account's clock and no time of day.
func TestFromParseTakesABareDayForEveryField(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 3, 12, 23, 0, 0, 0, time.UTC) // 00:00 on the 13th in Madrid
	day := "2026-03-13"
	for name, pv := range map[string]api.ParsePreview{
		"a do date at midnight": {DoAt: &midnight},
		"a day":                 {ScheduledDay: &day},
	} {
		for _, c := range []struct {
			field, want string
			dateOnly    bool
		}{
			{Day, "2026-03-13", false},
			{DoAt, "2026-03-13T00:00:00+01:00", true},
			{DueAt, "2026-03-13T00:00:00+01:00", true},
			{Remind, "2026-03-13T00:00:00+01:00", true},
		} {
			got, dateOnly, ok := FromParse(c.field, pv, madrid)
			if !ok || got != c.want || dateOnly != c.dateOnly {
				t.Errorf("%s, %s: got %q,%v,%v want %q,%v", name, c.field, got, dateOnly, ok, c.want, c.dateOnly)
			}
		}
	}
}

func TestLabel(t *testing.T) {
	if Label(Day) != "do day" || Label(DueAt) != "deadline" || Label("x") != "x" {
		t.Error("labels are wrong")
	}
}

func TestWire(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		field, in, want string
		dateOnly        bool
	}{
		// The offset follows the date, across Madrid's change on 2026-03-29.
		{DueAt, "2026-03-28T15:00", "2026-03-28T15:00:00+01:00", false},
		{DueAt, "2026-03-30T15:00", "2026-03-30T15:00:00+02:00", false},
		// A value that names its own offset is not read again.
		{DueAt, "2026-03-30T15:00:00Z", "2026-03-30T15:00:00Z", false},
		{Remind, "2026-03-30T15:00:00-05:00", "2026-03-30T15:00:00-05:00", false},
		// The day field is only a day, on Madrid's calendar for an instant.
		{Day, "2026-03-10T15:00:00+02:00", "2026-03-10", false},
		{Day, "2026-03-10T23:30:00Z", "2026-03-11", false},
		{Day, "2026-03-10T23:30", "2026-03-10", false},
		{Day, "2026-03-10", "2026-03-10", false},
		{Day, "15:04", "2026-03-28", false},
		// A bare date has no time of day.
		{DoAt, "2026-03-10", "2026-03-10T00:00:00+01:00", true},
		{DoAt, "15:04", "2026-03-28T15:04:00+01:00", false},
	}
	for _, c := range cases {
		got, dateOnly := Wire(c.field, c.in, madrid, now)
		if got != c.want || dateOnly != c.dateOnly {
			t.Errorf("Wire(%s, %q) = %q,%v want %q,%v", c.field, c.in, got, dateOnly, c.want, c.dateOnly)
		}
	}
}

func TestNeedsZone(t *testing.T) {
	cases := []struct {
		field, in string
		want      bool
	}{
		{DueAt, "2026-03-10T15:00", true},
		{DoAt, "2026-03-10", true},
		{DueAt, "2026-03-10T15:00:00Z", false},
		{Remind, "2026-03-10T15:00:00+01:00", false},
		{Day, "2026-03-10", false},
		{Day, "2026-03-10T15:00", false},
		{Day, "2026-03-10T15:00:00Z", true},
		{Day, "15:04", true},
	}
	for _, c := range cases {
		if got := NeedsZone(c.field, c.in); got != c.want {
			t.Errorf("NeedsZone(%s, %q) = %v want %v", c.field, c.in, got, c.want)
		}
	}
}

func TestParseProblemNamesTheField(t *testing.T) {
	rule := "FREQ=WEEKLY"
	pv := api.ParsePreview{RecurrenceRule: &rule}
	for _, field := range []string{Day, DoAt, DueAt, Remind} {
		err := ParseProblem(field, "friday weekly", pv)
		if err == nil || !strings.Contains(err.Error(), Label(field)) || !strings.Contains(err.Error(), "repeat") {
			t.Errorf("%s: %v, want the field and the repeat named", field, err)
		}
	}
	if err := ParseProblem(DueAt, "friday", api.ParsePreview{}); err != nil {
		t.Errorf("words with no repeat: %v", err)
	}
}

func TestAssignDoAtSetsHasTime(t *testing.T) {
	var p api.TodoPatch
	Assign(&p, DoAt, "2026-03-10T00:00:00Z", true)
	if string(p.HasTime) != "false" {
		t.Errorf("hasTime = %s, want false", p.HasTime)
	}
	Assign(&p, DoAt, "2026-03-10T09:00:00Z", false)
	if string(p.HasTime) != "true" {
		t.Errorf("hasTime = %s, want true", p.HasTime)
	}
	var q api.TodoPatch
	Assign(&q, DueAt, "2026-03-10T09:00:00Z", true)
	if q.HasTime != nil {
		t.Error("only the do time has a hasTime")
	}
}
