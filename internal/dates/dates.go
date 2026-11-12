// Package dates turns what a person types into the date and time values the
// API takes. It is shared by the TUI forms, the command line and the
// forms, so a date means the same thing everywhere.
package dates

import (
	"fmt"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Field names for the four date fields a todo has.
const (
	Day    = "scheduledDay"
	DoAt   = "doAt"
	DueAt  = "dueAt"
	Remind = "remindAt"
)

// Label names a field in an error message.
func Label(field string) string {
	switch field {
	case Day:
		return "do day"
	case DoAt:
		return "do time"
	case DueAt:
		return "deadline"
	case Remind:
		return "reminder"
	}
	return field
}

var layouts = []string{
	time.RFC3339,
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04",
	"2006/01/02",
	"15:04",
	"3pm",
	"3:04pm",
}

// Normalise reports whether v is a date or time in one of the accepted
// layouts, and returns its normal form. Words such as "friday" are not
// dates in this sense; they need the server's parser, see FromParse.
func Normalise(v string) (string, bool) {
	for _, l := range layouts {
		t, err := time.Parse(l, v)
		if err != nil {
			continue
		}
		switch l {
		case "2006-01-02", "2006/01/02":
			return t.Format("2006-01-02"), true
		case "15:04", "3pm", "3:04pm":
			return t.Format("15:04"), true
		case time.RFC3339:
			return t.Format(time.RFC3339), true
		default:
			return t.Format("2006-01-02T15:04"), true
		}
	}
	return "", false
}

// clockOnly reports whether a normal form is a time of day with no date.
func clockOnly(norm string) bool {
	return len(norm) == 5 && norm[2] == ':'
}

// NeedsZone reports whether Wire has to know the account's timezone to
// write norm for field. A caller that must fetch the zone can skip the
// request when this is false.
func NeedsZone(field, norm string) bool {
	_, err := time.Parse(time.RFC3339, norm)
	if field == Day {
		// The day an instant falls on depends on whose calendar it is.
		return clockOnly(norm) || err == nil
	}
	return err != nil
}

// Wire writes a normal form from Normalise the way the service takes it.
// The service refuses a date and time without a UTC offset, so one that has
// none is read on loc's clock and given loc's offset on that date, which
// matters across a daylight-saving change. A value that already carries Z
// or an offset is sent as it is. The day field takes only YYYY-MM-DD: the
// day on loc's calendar for a value with an offset, as words that name a
// time give it, so that a do day and a do time given the same instant
// agree, and the date as written otherwise. A bare date for any other
// field has no time of day; dateOnly says so, and the value is then
// midnight on loc's clock, the day the service stores.
func Wire(field, norm string, loc *time.Location, now time.Time) (val string, dateOnly bool) {
	if clockOnly(norm) {
		norm = now.In(loc).Format("2006-01-02") + "T" + norm
	}
	t, err := time.Parse(time.RFC3339, norm)
	if field == Day {
		if err == nil {
			return t.In(loc).Format("2006-01-02"), false
		}
		return norm[:10], false
	}
	if err == nil {
		return t.Format(time.RFC3339), false
	}
	layout, dateOnly := "2006-01-02T15:04", false
	if !strings.Contains(norm, "T") {
		layout, dateOnly = "2006-01-02", true
	}
	t, err = time.ParseInLocation(layout, norm, loc)
	if err != nil {
		return norm, dateOnly
	}
	return t.Format(time.RFC3339), dateOnly
}

// Assign sets a date field of a patch to a value from Wire. A do time that
// is only a date also says hasTime is false, so the service keeps it a do
// date instead of treating the midnight as a time of day.
func Assign(p *api.TodoPatch, field, val string, dateOnly bool) {
	switch field {
	case Day:
		p.ScheduledDay = api.Val(val)
	case DoAt:
		p.DoAt = api.Val(val)
		p.HasTime = api.BoolVal(!dateOnly)
	case DueAt:
		p.DueAt = api.Val(val)
	case Remind:
		p.RemindAt = api.Val(val)
	}
}

// FromParse picks the part of a parse result that fits a date field, in the
// account timezone, and reports whether the words held one. Times come out
// as RFC 3339 with the zone's offset, which is what the service takes.
//
// Words that name only a day ("friday") are read the way the same day
// written as a date is: the do day takes it, and every other field takes
// midnight on that day on loc's clock, with dateOnly true, as Wire does.
// The service answers such words with a do date at midnight and hasTime
// false, so that midnight is not a time of day anyone asked for.
func FromParse(field string, pv api.ParsePreview, loc *time.Location) (val string, dateOnly, ok bool) {
	const clock = time.RFC3339
	if field == Day {
		switch {
		case pv.ScheduledDay != nil:
			return *pv.ScheduledDay, false, true
		case pv.DoAt != nil:
			return pv.DoAt.In(loc).Format("2006-01-02"), false, true
		case pv.DueAt != nil:
			return pv.DueAt.In(loc).Format("2006-01-02"), false, true
		}
		return "", false, false
	}
	var timed *time.Time
	switch field {
	case DoAt:
		if pv.HasTime {
			timed = pv.DoAt
		}
	case DueAt:
		timed = pv.DueAt
		if timed == nil && pv.HasTime {
			timed = pv.DoAt
		}
	case Remind:
		if pv.HasTime {
			timed = pv.DoAt
		}
		if timed == nil {
			timed = pv.DueAt
		}
	}
	if timed != nil {
		return timed.In(loc).Format(clock), false, true
	}
	var day string
	switch {
	case pv.ScheduledDay != nil:
		day = *pv.ScheduledDay
	case pv.DoAt != nil:
		day = pv.DoAt.In(loc).Format("2006-01-02")
	default:
		return "", false, false
	}
	val, dateOnly = Wire(field, day, loc, time.Time{})
	return val, dateOnly, true
}

// ParseProblem explains why words cannot be used in a single date field.
func ParseProblem(field, text string, pv api.ParsePreview) error {
	if pv.RecurrenceRule != nil && *pv.RecurrenceRule != "" {
		return fmt.Errorf("%q includes a repeat, which a %s cannot hold; set the repeat separately with -repeat or the Repeat field", text, Label(field))
	}
	return nil
}

func Unreadable(field, text string) error {
	return fmt.Errorf("could not read %q as a %s; use a date like 2026-03-10 or words like \"friday 5pm\"", text, Label(field))
}
