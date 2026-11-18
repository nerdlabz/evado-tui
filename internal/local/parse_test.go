package local

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	// Wednesday 30 September 2026, mid-morning.
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, loc)
	cases := []struct {
		in, title string
		at        string // "" for no date
		hasTime   bool
		rule      string
	}{
		{"Buy milk", "Buy milk", "", false, ""},
		{"Pay the cable bill Friday at 5pm", "Pay the cable bill", "2026-10-02 17:00", true, ""},
		{"Pay the cable bill on friday", "Pay the cable bill", "2026-10-02 00:00", false, ""},
		{"Call Sam tomorrow", "Call Sam", "2026-10-01 00:00", false, ""},
		{"call sam tomorrow 9am", "call sam", "2026-10-01 09:00", true, ""},
		{"Water the plants today", "Water the plants", "2026-09-30 00:00", false, ""},
		{"standup at 9:30am", "standup", "2026-10-01 09:30", true, ""}, // already past today
		{"lunch at noon", "lunch", "2026-09-30 12:00", true, ""},
		{"ship it 17:30", "ship it", "2026-09-30 17:30", true, ""},
		{"Renew passport in 2 weeks", "Renew passport", "2026-10-14 00:00", false, ""},
		{"Dentist oct 3", "Dentist", "2026-10-03 00:00", false, ""},
		{"Dentist October 3rd at 2pm", "Dentist", "2026-10-03 14:00", true, ""},
		{"Taxes 15 april", "Taxes", "2027-04-15 00:00", false, ""},
		{"Launch 2026-11-05", "Launch", "2026-11-05 00:00", false, ""},
		{"Review plan next week", "Review plan", "2026-10-07 00:00", false, ""},
		{"Gym wednesday", "Gym", "2026-09-30 00:00", false, ""}, // today is Wednesday: today, as the service reads it
		{"Call mum the day after tomorrow", "Call mum", "2026-10-02 00:00", false, ""},
		{"Gym friday after next", "Gym", "2026-10-09 00:00", false, ""},
		// A date word inside a longer phrase this reader does not work out
		// is not read on its own, which would give the wrong day.
		{"Review 2 weeks before friday", "Review 2 weeks before friday", "", false, ""},
		{"Party the day after friday", "Party the day after friday", "", false, ""},
		// Days or weeks from a weekday count from the coming one.
		{"Review 2 weeks from friday", "Review", "2026-10-16 00:00", false, ""},
		{"Report end of next week", "Report end of next week", "", false, ""},
		{"Sprint the week after next", "Sprint", "2026-10-14 00:00", false, ""},
		{"Expense dinner from last tuesday", "Expense dinner from last tuesday", "", false, ""},
		{"Call mum tomorrow evening", "Call mum", "2026-10-01 00:00", false, ""},
		{"Call mum tonight at 8pm", "Call mum", "2026-09-30 20:00", true, ""},
		{"Clean this weekend", "Clean", "2026-10-03 00:00", false, ""},
		{"Call two days from now", "Call", "2026-10-02 00:00", false, ""},
		{"Rent first of october", "Rent", "2026-10-01 00:00", false, ""},
		{"Rent twenty first of october", "Rent", "2026-10-21 00:00", false, ""},
		{"Wake up at 5", "Wake up", "2026-09-30 17:00", true, ""}, // never the small hours
		{"Pay 5,000 at 5,000 rate", "Pay 5,000 at 5,000 rate", "", false, ""},
		{"Stand up in an hour", "Stand up", "2026-09-30 11:00", true, ""},
		{"Submit 10/15", "Submit", "2026-10-15 00:00", false, ""},
		{"Submit 31/4", "Submit 31/4", "", false, ""},
		{"Launch 2026-11-05T14:30", "Launch", "2026-11-05 14:30", true, ""},
		{"Due by sept 30", "Due", "2026-09-30 00:00", false, ""},
		{"Gym next friday", "Gym", "2026-10-09 00:00", false, ""},
		{"Take out bins every wednesday", "Take out bins", "2026-09-30 00:00", false, "1:week:3"}, // today is Wednesday, and it is before 18:00
		{"Take out bins every other thursdays", "Take out bins", "2026-10-01 00:00", false, "2:week:4"},
		{"Water plants every four days", "Water plants", "2026-10-04 00:00", false, "4:day"},
		{"Pay rent every month", "Pay rent", "2026-10-30 00:00", false, "1:month"},
		{"Stretch daily", "Stretch", "2026-10-01 00:00", false, "1:day"},
		{"Water plants every other week", "Water plants", "2026-10-14 00:00", false, "2:week"},
		{"Standup every monday at 9am", "Standup", "2026-10-05 09:00", true, "1:week:1"},
		{"Read chapter 5", "Read chapter 5", "", false, ""},
		{"Friday", "Friday", "2026-10-02 00:00", false, ""},
	}
	for _, c := range cases {
		got := Parse(c.in, loc, now)
		at := ""
		if got.DoAt != nil {
			at = got.DoAt.In(loc).Format("2006-01-02 15:04")
		}
		if got.Title != c.title || at != c.at || got.HasTime != c.hasTime || got.Recurrence != c.rule {
			t.Errorf("Parse(%q) = %q at %q time=%v rule=%q; want %q at %q time=%v rule=%q",
				c.in, got.Title, at, got.HasTime, got.Recurrence, c.title, c.at, c.hasTime, c.rule)
		}
	}
}

func TestParseBareTimeWithNoDayIsTheSoonestReading(t *testing.T) {
	checkReadings(t, []readCase{
		// 10:00: the next 3 is this afternoon's, and 5 can only be the evening.
		{"", "Meet at 3", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 3:30", "Meet", "2026-09-30 15:30", true, ""},
		{"", "Call at 5", "Call", "2026-09-30 17:00", true, ""},
		{"", "Meet at 1", "Meet", "2026-09-30 13:00", true, ""},
		// 8 this morning has gone, 8 this evening has not. 9 has no evening.
		{"", "Movie at 8", "Movie", "2026-09-30 20:00", true, ""},
		{"", "Standup at 9", "Standup", "2026-10-01 09:00", true, ""},
		{"2026-09-30 08:00", "Standup at 9", "Standup", "2026-09-30 09:00", true, ""},
		// Whichever half comes first.
		{"2026-09-30 21:00", "Wake up at 8", "Wake up", "2026-10-01 08:00", true, ""},
		{"2026-09-30 14:00", "Wake up at 8", "Wake up", "2026-09-30 20:00", true, ""},
		{"2026-09-30 20:00", "Call at 9", "Call", "2026-10-01 09:00", true, ""},
		// Never the small hours: 3 is the afternoon, even late at night.
		{"2026-09-30 21:00", "Meet at 3", "Meet", "2026-10-01 15:00", true, ""},
		{"2026-09-30 23:30", "Meet at 3", "Meet", "2026-10-01 15:00", true, ""},
		{"2026-10-01 02:00", "Meet at 3", "Meet", "2026-10-01 15:00", true, ""},
		{"2026-09-30 05:53", "Wake up at 5", "Wake up", "2026-09-30 17:00", true, ""},
		// 06:00 is the first hour it can be, and 21:00 the first it cannot.
		{"2026-09-30 05:59", "Wake up at 6", "Wake up", "2026-09-30 06:00", true, ""},
		{"2026-09-30 06:00", "Wake up at 6", "Wake up", "2026-09-30 18:00", true, ""},
		{"2026-09-30 20:59", "Call at 8", "Call", "2026-10-01 08:00", true, ""},
		{"2026-09-30 19:59", "Call at 8", "Call", "2026-09-30 20:00", true, ""},
		{"2026-09-30 20:00", "Call at 8:59", "Call", "2026-09-30 20:59", true, ""},
		{"2026-09-30 22:30", "Dentist at 9", "Dentist", "2026-10-01 09:00", true, ""},
		{"2026-09-30 20:59", "Call at 9", "Call", "2026-10-01 09:00", true, ""},
		// A time already past today is tomorrow's.
		{"2026-09-30 15:30", "Meet at 3", "Meet", "2026-10-01 15:00", true, ""},
		// 12 is noon, as typed.
		{"", "Lunch at 12", "Lunch", "2026-09-30 12:00", true, ""},
		{"", "Lunch at 12:30", "Lunch", "2026-09-30 12:30", true, ""},
		{"2026-09-30 13:00", "Lunch at 12", "Lunch", "2026-10-01 12:00", true, ""},
		// The day is the one in the account's zone.
		{"2026-09-30 21:30", "Meet at 3", "Meet", "2026-10-01 15:00", true, ""},
		// Dates in the text that are not times.
		{"", "Pay 5,000 at 5,000 rate", "Pay 5,000 at 5,000 rate", "", false, ""},
		{"", "Meet at 5.30", "Meet at 5.30", "", false, ""},
		{"", "Meet at the 5th", "Meet at the 5th", "", false, ""},
	})
}

func TestParseBareTimeWithADayIsReadByTheHour(t *testing.T) {
	checkReadings(t, []readCase{
		// 7 to 11 is the morning, the rest the afternoon.
		{"", "Wake up tomorrow at 6", "Wake up", "2026-10-01 18:00", true, ""},
		{"", "Wake up tomorrow at 7", "Wake up", "2026-10-01 07:00", true, ""},
		{"", "Wake up tomorrow at 8", "Wake up", "2026-10-01 08:00", true, ""},
		{"", "Wake up tomorrow at 11", "Wake up", "2026-10-01 11:00", true, ""},
		{"", "Dinner tomorrow at 12", "Dinner", "2026-10-01 12:00", true, ""},
		{"", "Dinner tomorrow at 1", "Dinner", "2026-10-01 13:00", true, ""},
		{"", "Meet tomorrow at 5", "Meet", "2026-10-01 17:00", true, ""},
		{"", "Meet at 5 tomorrow", "Meet", "2026-10-01 17:00", true, ""},
		{"", "Meet friday at 9", "Meet", "2026-10-02 09:00", true, ""},
		{"", "Movie friday at 8", "Movie", "2026-10-02 08:00", true, ""},
		{"", "Dinner saturday at 6", "Dinner", "2026-10-03 18:00", true, ""},
		{"", "Dinner saturday at 6:30", "Dinner", "2026-10-03 18:30", true, ""},
		{"", "Pay next tuesday at 9", "Pay", "2026-10-06 09:00", true, ""},
		{"", "Pay rent on the 15th at 3", "Pay rent", "2026-10-15 15:00", true, ""},
		{"", "Pay oct 15 at 3", "Pay", "2026-10-15 15:00", true, ""},
		// The day decides, not how soon the time comes: this is 5 this afternoon.
		{"2026-09-30 21:00", "Call today at 5", "Call", "2026-09-30 17:00", true, ""},
		// A month or a year names no day, so the time is the soonest reading.
		{"", "Call next month at 8", "Call", "2026-10-30 20:00", true, ""},
		{"", "Call in 2 months at 7", "Call", "2026-11-30 19:00", true, ""},
		{"", "Call next year at 7", "Call", "2027-09-30 19:00", true, ""},
		{"", "Call in 3 days at 8", "Call", "2026-10-03 08:00", true, ""},
		{"", "Call next week at 8", "Call", "2026-10-07 08:00", true, ""},
		// A recurrence is the same.
		{"", "Standup every monday at 3", "Standup", "2026-10-05 15:00", true, "1:week:1"},
		{"", "Standup every monday at 9", "Standup", "2026-10-05 09:00", true, "1:week:1"},
		{"", "Stretch daily at 8", "Stretch", "2026-10-01 08:00", true, "1:day"},
		{"", "Stretch daily at 6", "Stretch", "2026-09-30 18:00", true, "1:day"},
	})
}

func TestParseATimeThatSaysWhichHalfIsReadAsTyped(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Meet at 3pm", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 3 pm", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 3am", "Meet", "2026-10-01 03:00", true, ""},
		{"", "Meet at 15:00", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 03:00", "Meet", "2026-10-01 03:00", true, ""},
		{"", "Meet at 3:30am", "Meet", "2026-10-01 03:30", true, ""},
		{"", "Meet at 15", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 07", "Meet", "2026-10-01 07:00", true, ""},
		{"", "Meet at noon", "Meet", "2026-09-30 12:00", true, ""},
		{"", "Meet at midnight", "Meet", "2026-10-01 00:00", true, ""},
		{"", "Meet at 12", "Meet", "2026-09-30 12:00", true, ""},
		{"", "Meet at 3 in the afternoon", "Meet", "2026-09-30 15:00", true, ""},
		{"", "Meet at 8 in the morning", "Meet", "2026-10-01 08:00", true, ""},
		{"", "Meet at 8 in the evening", "Meet", "2026-09-30 20:00", true, ""},
		{"", "Call at 8 tonight", "Call", "2026-09-30 20:00", true, ""},
		{"", "Call at 8 this evening", "Call", "2026-09-30 20:00", true, ""},
		{"", "Call tonight at 8pm", "Call", "2026-09-30 20:00", true, ""},
	})
}

func TestParseWordedTimesWithNoAmOrPm(t *testing.T) {
	// 05:53, as the service's corpus was read.
	const early = "2026-09-30 05:53"
	checkReadings(t, []readCase{
		{early, "Meet at half past 5", "Meet", "2026-09-30 17:30", true, ""},
		{early, "Meet half past 5 tomorrow", "Meet", "2026-10-01 17:30", true, ""},
		{early, "Meet at half past five", "Meet", "2026-09-30 17:30", true, ""},
		{early, "Meet at quarter past 5", "Meet", "2026-09-30 17:15", true, ""},
		{early, "Meet at quarter to 6", "Meet", "2026-09-30 17:45", true, ""},
		{early, "Meet at quarter to 12", "Meet", "2026-09-30 11:45", true, ""},
		{early, "Meet at quarter to 1", "Meet", "2026-09-30 12:45", true, ""},
		{early, "Meet at half past 9", "Meet", "2026-09-30 09:30", true, ""},
		{early, "Meet at half past 12", "Meet", "2026-09-30 12:30", true, ""},
		{early, "Meet at 5 o'clock", "Meet", "2026-09-30 17:00", true, ""},
		{early, "Meet 5 o'clock tomorrow", "Meet", "2026-10-01 17:00", true, ""},
		{early, "Meet at 8 o'clock", "Meet", "2026-09-30 08:00", true, ""},
		{early, "Meet at 8 o'clock tomorrow", "Meet", "2026-10-01 08:00", true, ""},
		{early, "Meet at 12 o'clock", "Meet", "2026-09-30 12:00", true, ""},
		{"2026-09-30 21:00", "Wake up at half past 8", "Wake up", "2026-10-01 08:30", true, ""},
		// No such hour.
		{early, "Meet at half past 13", "Meet at half past 13", "", false, ""},
		{early, "Meet half past 13 tomorrow", "Meet half past 13 tomorrow", "", false, ""},
	})
}

func TestLooksDated(t *testing.T) {
	cases := map[string]string{
		"Plan the march offsite":      "march",
		"Book flights in a fortnight": "in a fortnight",
		"Buy gifts christmas":         "christmas",
		"Pay rent on the 15th":        "15th",
		"Gym friday after next":       "friday",
		"Buy milk":                    "",
		"Fix the may release":         "",
		"Sun cream":                   "",
		"Read chapter 5":              "",
	}
	for in, want := range cases {
		if got, ok := LooksDated(in); got != want || ok != (want != "") {
			t.Errorf("LooksDated(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

func TestNextOccurrence(t *testing.T) {
	loc := time.UTC
	after := time.Date(2026, 9, 30, 10, 0, 0, 0, loc) // a Wednesday
	cases := map[string]string{
		"1:day":    "2026-10-01",
		"3:day":    "2026-10-03",
		"1:week":   "2026-10-07",
		"1:week:5": "2026-10-02",
		"1:week:3": "2026-10-07",
		"1:month":  "2026-10-30",
		"1:year":   "2027-09-30",
	}
	for rule, want := range cases {
		if got := NextOccurrence(rule, loc, after, 9, 0).Format("2006-01-02"); got != want {
			t.Errorf("%s: next = %s, want %s", rule, got, want)
		}
	}
	if got := NextOccurrence("nonsense", loc, after, 9, 0); !got.Equal(after) {
		t.Errorf("an unreadable rule should give the moment back, got %v", got)
	}
	for _, bad := range []string{"", "0:day", "1:fortnight", "1:week:8", "x:day"} {
		if ValidRule(bad) {
			t.Errorf("ValidRule(%q) = true", bad)
		}
	}
}
