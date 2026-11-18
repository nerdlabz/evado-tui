package local

import (
	"testing"
	"time"
)

// readCase is a phrase read as of a moment in Madrid, "" for the usual one:
// Wednesday 30 September 2026, 10:00.
type readCase struct {
	now, in, title string
	at             string // "" for no date
	hasTime        bool
	rule           string
}

func checkReadings(t *testing.T, cases []readCase) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		now := time.Date(2026, 9, 30, 10, 0, 0, 0, loc)
		if c.now != "" {
			if now, err = time.ParseInLocation("2006-01-02 15:04", c.now, loc); err != nil {
				t.Fatal(err)
			}
		}
		got := Parse(c.in, loc, now)
		at := ""
		if got.DoAt != nil {
			at = got.DoAt.In(loc).Format("2006-01-02 15:04")
		}
		if got.Title != c.title || at != c.at || got.HasTime != c.hasTime || got.Recurrence != c.rule {
			t.Errorf("Parse(%q) at %q = %q at %q time=%v rule=%q; want %q at %q time=%v rule=%q",
				c.in, c.now, got.Title, at, got.HasTime, got.Recurrence, c.title, c.at, c.hasTime, c.rule)
		}
	}
}

func TestParseCountsFromTodayOrTomorrow(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Call 3 days from tomorrow", "Call", "2026-10-04 00:00", false, ""},
		{"", "Call two weeks from today", "Call", "2026-10-14 00:00", false, ""},
		{"", "Call a week after tomorrow", "Call", "2026-10-08 00:00", false, ""},
		{"", "Call a week from today at 5pm", "Call", "2026-10-07 17:00", true, ""},
		{"", "2 weeks from today", "2 weeks from today", "2026-10-14 00:00", false, ""},
		// The day is the one in the account's zone, not the day somewhere else.
		{"2026-10-01 23:30", "Call a week from today", "Call", "2026-10-08 00:00", false, ""},
		// A month stops at the end of one that is too short.
		{"2026-01-31 12:00", "Renew a month from today", "Renew", "2026-02-28 00:00", false, ""},
		{"2026-01-31 12:00", "Renew in 1 month", "Renew", "2026-02-28 00:00", false, ""},
		{"2026-03-31 12:00", "Renew next month", "Renew", "2026-04-30 00:00", false, ""},
		{"2028-02-29 12:00", "Renew a year from today", "Renew", "2029-02-28 00:00", false, ""},
		{"2026-11-30 12:00", "Renew a month from tomorrow", "Renew", "2027-01-01 00:00", false, ""},
		// Days or weeks from a weekday count from the coming one, today
		// included, as the weekday alone reads.
		{"", "Review 2 weeks from friday", "Review", "2026-10-16 00:00", false, ""},
		{"", "Review a day from wednesday", "Review", "2026-10-01 00:00", false, ""},
		// Counts from a day this reader does not work out stay in the title.
		{"", "Review 2 months from friday", "Review 2 months from friday", "", false, ""},
		{"", "Review 2 weeks before friday", "Review 2 weeks before friday", "", false, ""},
	})
}

func TestParseAfterNext(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Dentist monday after next", "Dentist", "2026-10-12 00:00", false, ""},
		{"", "Pay friday after next", "Pay", "2026-10-09 00:00", false, ""},
		// On the day itself, next friday is in a week and the one after is a week on.
		{"2026-10-02 10:00", "Pay friday after next", "Pay", "2026-10-16 00:00", false, ""},
		{"2026-10-02 10:00", "Pay next friday", "Pay", "2026-10-09 00:00", false, ""},
		{"2026-10-04 10:00", "Pay monday after next", "Pay", "2026-10-12 00:00", false, ""},
		{"", "Pay the week after next", "Pay", "2026-10-14 00:00", false, ""},
		{"", "Pay week after next", "Pay", "2026-10-14 00:00", false, ""},
		{"", "Call mum day after tomorrow", "Call mum", "2026-10-02 00:00", false, ""},
		// The minutes of a time are not a count: not "45 days after tomorrow".
		{"", "Dentist at 10:45 day after tomorrow", "Dentist", "2026-10-02 10:45", true, ""},
		{"", "Dentist at 5:30 day after tomorrow", "Dentist", "2026-10-02 17:30", true, ""},
		{"", "Dentist at 03:00 day after tomorrow", "Dentist", "2026-10-02 03:00", true, ""},
		{"", "Dentist 10:45 2 weeks from today", "Dentist", "2026-10-14 10:45", true, ""},
	})
}

// A number right after "at" is the hour of a time, not a count of days.
func TestParseHourAfterAtIsNotACount(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Dentist at 10 day after tomorrow", "Dentist", "2026-10-02 10:00", true, ""},
		{"", "Dentist at 2 day after tomorrow", "Dentist", "2026-10-02 14:00", true, ""},
		// The same without "at" is still a count.
		{"", "Dentist 10 days after tomorrow", "Dentist", "2026-10-11 00:00", false, ""},
		{"", "Dentist 2 day after tomorrow", "Dentist", "2026-10-03 00:00", false, ""},
		{"", "Meet at 3 in 2 days", "Meet", "2026-10-02 15:00", true, ""},
		// The rest of the phrase is one this reader does not work out, so the
		// time beside it gets no date, but never a date a count of days out.
		{"", "Dentist at 10 days after tomorrow", "Dentist at 10 days after tomorrow", "", false, ""},
		{"", "Call at 3 weeks from today", "Call at 3 weeks from today", "", false, ""},
		{"", "Call at 2 days from tomorrow", "Call at 2 days from tomorrow", "", false, ""},
		{"", "Call at 5 days ago", "Call at 5 days ago", "", false, ""},
		{"", "Call at 3 weeks from now", "Call at 3 weeks from now", "", false, ""},
		{"", "Call at 3 weeks after today", "Call at 3 weeks after today", "", false, ""},
		{"", "Call at 3 months ago", "Call at 3 months ago", "", false, ""},
		// Beyond the hour, or with a count in front of "at", nothing changes.
		{"", "Call at 5pm 2 weeks from today", "Call", "2026-10-14 17:00", true, ""},
		{"", "Call 2 weeks from today at 5pm", "Call", "2026-10-14 17:00", true, ""},
		{"", "Call 3 days ago at 5pm", "Call 3 days ago at 5pm", "", false, ""},
	})
}

func TestParseEndOfPeriod(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Report end of the month", "Report", "2026-09-30 00:00", false, ""},
		{"", "Report end of month", "Report", "2026-09-30 00:00", false, ""},
		{"", "Report last day of this month", "Report", "2026-09-30 00:00", false, ""},
		{"", "Report end of the year", "Report", "2026-12-31 00:00", false, ""},
		{"", "Report end of next year", "Report", "2027-12-31 00:00", false, ""},
		{"", "Report end of next month", "Report", "2026-10-31 00:00", false, ""},
		{"", "Report end of the next month", "Report", "2026-10-31 00:00", false, ""},
		{"", "Report by the end of next month", "Report", "2026-10-31 00:00", false, ""},
		{"2026-10-01 10:00", "Report by end of next month", "Report", "2026-11-30 00:00", false, ""},
		{"2026-02-10 12:00", "Report end of the month", "Report", "2026-02-28 00:00", false, ""},
		{"2026-12-10 12:00", "Report end of next month", "Report", "2027-01-31 00:00", false, ""},
		{"2027-01-31 12:00", "Report end of next month", "Report", "2027-02-28 00:00", false, ""},
		{"2026-10-01 10:00", "Report end of next month at 5pm", "Report", "2026-11-30 17:00", true, ""},
		// The end of the week is Friday to some and Sunday to others.
		{"", "Report end of the week", "Report end of the week", "", false, ""},
		{"", "Report by end of week", "Report by end of week", "", false, ""},
		{"", "Report end of next week", "Report end of next week", "", false, ""},
		{"", "Report end of friday", "Report end of friday", "", false, ""},
	})
}

func TestParseDayOfTheMonth(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Pay rent on the 15th", "Pay rent", "2026-10-15 00:00", false, ""},
		{"", "Pay rent on the 1st", "Pay rent", "2026-10-01 00:00", false, ""},
		{"", "Pay rent on the 2nd", "Pay rent", "2026-10-02 00:00", false, ""},
		// Today counts; the day after, it is next month's.
		{"2026-10-15 12:00", "Pay rent on the 15th", "Pay rent", "2026-10-15 00:00", false, ""},
		{"2026-10-16 12:00", "Pay rent on the 15th", "Pay rent", "2026-11-15 00:00", false, ""},
		{"2026-12-20 12:00", "Pay rent on the 15th", "Pay rent", "2027-01-15 00:00", false, ""},
		// A month with no such day is skipped.
		{"2026-09-01 12:00", "Pay on the 31st", "Pay", "2026-10-31 00:00", false, ""},
		{"2026-09-30 12:00", "Pay on the 31st", "Pay", "2026-10-31 00:00", false, ""},
		{"2026-02-01 12:00", "Pay on the 30th", "Pay", "2026-03-30 00:00", false, ""},
		{"", "Pay rent on the 15th at 5pm", "Pay rent", "2026-10-15 17:00", true, ""},
		{"", "Pay rent on the 15th every month", "Pay rent", "2026-10-15 00:00", false, "1:month"},
		{"", "Call mom on the 3rd of november", "Call mom", "2026-11-03 00:00", false, ""},
		// An ordinal that ends no sentence is not a day of the month.
		{"", "Meet on the 3rd floor", "Meet on the 3rd floor", "", false, ""},
		{"", "Work on the 1st draft", "Work on the 1st draft", "", false, ""},
		{"", "Pay on the 32nd", "Pay on the 32nd", "", false, ""},
	})
}

func TestParseThePastGetsNoDate(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "yesterday", "yesterday", "", false, ""},
		{"", "Call Bob yesterday", "Call Bob yesterday", "", false, ""},
		{"", "Call Bob yesterday at 5pm", "Call Bob yesterday at 5pm", "", false, ""},
		{"", "Yesterday I paid", "Yesterday I paid", "", false, ""},
		{"", "Pay 3 days ago", "Pay 3 days ago", "", false, ""},
		{"", "Pay last week", "Pay last week", "", false, ""},
		{"", "Pay last friday", "Pay last friday", "", false, ""},
		{"", "Pay last friday at 5pm", "Pay last friday at 5pm", "", false, ""},
		{"", "Call mum the day before yesterday", "Call mum the day before yesterday", "", false, ""},
		// The date is the last one in the line.
		{"", "Call tomorrow, no, yesterday", "Call tomorrow, no, yesterday", "", false, ""},
		{"", "Lunch yesterday, dinner friday", "Lunch yesterday, dinner", "2026-10-02 00:00", false, ""},
		// A recurrence still starts.
		{"", "Pay last friday every month", "Pay last friday", "2026-10-30 00:00", false, "1:month"},
	})
}

func TestParseHalfAPhraseGetsNoDate(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Call a week before friday", "Call a week before friday", "", false, ""},
		{"", "Call a week from", "Call a week from", "", false, ""},
		{"", "Meet half past 13 tomorrow", "Meet half past 13 tomorrow", "", false, ""},
	})
}

func TestParseConnectorsLeftAtTheEnd(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Clean house at the weekend", "Clean house", "2026-10-03 00:00", false, ""},
		{"", "Clean house on the weekend", "Clean house", "2026-10-03 00:00", false, ""},
		{"", "Pay rent due by the end of next month", "Pay rent", "2026-10-31 00:00", false, ""},
		{"", "Pay rent the first of november", "Pay rent", "2026-11-01 00:00", false, ""},
		{"", "Due by sept 30", "Due", "2026-09-30 00:00", false, ""},
		// A date and a time side by side are both cut, whatever the title starts with.
		{"", ", Launch 2026-11-05 14:30", "Launch", "2026-11-05 14:30", true, ""},
		{"", "Launch by friday by 5pm", "Launch", "2026-10-02 17:00", true, ""},
		// An article in the title stays.
		{"", "Meet the team tomorrow", "Meet the team", "2026-10-01 00:00", false, ""},
		{"", "Meet the team on friday at 5pm", "Meet the team", "2026-10-02 17:00", true, ""},
		// "coming" is a verb as often as a date word.
		{"", "Contractor coming tomorrow", "Contractor coming", "2026-10-01 00:00", false, ""},
		{"", "Contractor coming friday", "Contractor coming", "2026-10-02 00:00", false, ""},
		{"", "Contractor coming at 7:30am", "Contractor coming", "2026-10-01 07:30", true, ""},
		{"", "Pay this coming friday", "Pay", "2026-10-02 00:00", false, ""},
		{"", "Pay the coming friday", "Pay", "2026-10-02 00:00", false, ""},
	})
}

func TestParseRecurrenceWithATime(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Standup every monday at 9am", "Standup", "2026-10-05 09:00", true, "1:week:1"},
		// On that weekday it starts today while the time is ahead.
		{"2026-10-05 08:00", "Standup every monday at 9am", "Standup", "2026-10-05 09:00", true, "1:week:1"},
		{"2026-10-05 10:00", "Standup every monday at 9am", "Standup", "2026-10-12 09:00", true, "1:week:1"},
		{"2026-10-05 09:00", "Standup every monday at 9am", "Standup", "2026-10-12 09:00", true, "1:week:1"},
		// Without a weekday the time is read like any other.
		{"2026-09-30 08:00", "Standup every 2 weeks at 9am", "Standup", "2026-09-30 09:00", true, "2:week"},
		{"", "Standup every 2 weeks at 9am", "Standup", "2026-10-01 09:00", true, "2:week"},
		{"", "Standup daily at 9am", "Standup", "2026-10-01 09:00", true, "1:day"},
	})
}

// A phrase this reader does not work out takes the time beside it along:
// "end of the week at 5pm" is not today at 5pm.
func TestParseTimeBesideAPhraseNotWorkedOut(t *testing.T) {
	checkReadings(t, []readCase{
		{"", "Report end of the week at 5pm", "Report end of the week at 5pm", "", false, ""},
		{"", "Report at 5pm end of next week", "Report at 5pm end of next week", "", false, ""},
		{"", "Report end of next week at 5", "Report end of next week at 5", "", false, ""},
		{"", "Review 2 weeks before friday at 5pm", "Review 2 weeks before friday at 5pm", "", false, ""},
		{"", "Call a month from friday 6pm", "Call a month from friday 6pm", "", false, ""},
		// A count from a weekday is read, and the time goes with it.
		{"", "Review 2 weeks from friday at 5pm", "Review", "2026-10-16 17:00", true, ""},
		{"", "Party the day after friday at 5pm", "Party the day after friday at 5pm", "", false, ""},
		// A phrase that was read, and a time that is not beside the other, are as before.
		{"", "Report end of next month at 5pm", "Report", "2026-10-31 17:00", true, ""},
		{"", "Call 2 weeks from tomorrow at 5pm", "Call", "2026-10-15 17:00", true, ""},
	})
}
