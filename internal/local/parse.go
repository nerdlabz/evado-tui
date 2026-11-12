// Package local keeps an account's todos on this computer and syncs them
// with the Evado service when there is one to sync with. It answers the same
// API the service does, so the rest of the program cannot tell them apart.
package local

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Parsed is what a line of text was read as: the title with the date words
// taken out, when to do it, and how often it repeats.
type Parsed struct {
	KeptAsName string
	Title      string
	DoAt       *time.Time
	HasTime    bool
	Recurrence string
	// Assumed is set when a time was typed without am/pm and one reading
	// was guessed.
	Assumed *AssumedTime
}

// AssumedTime is what a bare time was read as and what the other half of the
// day would have given, in the form the service's /parse reports it.
type AssumedTime = api.AssumedTime

// The service reads dates with chrono-node, a full natural-language
// library. This reads the phrasings people use most, so capture works with
// no account and with no network: today, tomorrow and tonight, weekdays and
// weekends, "in 3 days", "two days from now", "a week from today", "friday
// after next", "on the 15th", "end of the month", "oct 3", "first of
// october", ISO and numeric dates, and times such as 5pm, 17:30 or "at 5".
// A time with no am/pm is read as the service reads it, and Parsed.Assumed
// says so.
//
// testdata/parse_corpus.json holds phrases the service has read, and the
// corpus test holds this reader to them; where the two differ on purpose
// the entry says why. Anything not recognised here is left in the title and
// the todo lands in the inbox, which is better than a wrong day.

var weekdays = map[string]time.Weekday{
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tues": time.Tuesday, "tue": time.Tuesday,
	"wednesday": time.Wednesday, "weds": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thurs": time.Thursday, "thur": time.Thursday, "thu": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
	"sunday": time.Sunday, "sun": time.Sunday,
}

var months = map[string]time.Month{
	"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3,
	"apr": 4, "april": 4, "may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7,
	"aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
}

// counts are the numbers people write as words, as in "in two days".
var counts = map[string]int{
	"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
}

// ordinals are days of the month written as words, as in "first of
// october". "twenty-first" is kept with a hyphen and also read with a space.
var ordinals = func() map[string]int {
	ones := []string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth"}
	m := map[string]int{
		"tenth": 10, "eleventh": 11, "twelfth": 12, "thirteenth": 13, "fourteenth": 14,
		"fifteenth": 15, "sixteenth": 16, "seventeenth": 17, "eighteenth": 18,
		"nineteenth": 19, "twentieth": 20, "thirtieth": 30, "thirty-first": 31,
	}
	for i, w := range ones {
		m[w] = i + 1
		m["twenty-"+w] = 21 + i
	}
	return m
}()

const (
	weekdayWords = `monday|mon|tuesday|tues|tue|wednesday|weds|wed|thursday|thurs|thur|thu|friday|fri|saturday|sat|sunday|sun`
	monthWords   = `january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec`
	countWords   = `\d+|an|a|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve`
	unitWords    = `day|week|month|year`
	// A part of the day names no time the service will commit to, so it is
	// read as part of the day: "tomorrow evening" is tomorrow.
	partOfDay = `(?:\s+(?:morning|afternoon|evening|night))?`
)

// ordinalWords is a pattern for the ordinals, longest first so that
// "twenty first" is not read as "twenty" and then "first".
var ordinalWords = func() string {
	var words []string
	for w := range ordinals {
		words = append(words, strings.ReplaceAll(w, "-", "[- ]"))
	}
	sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	return strings.Join(words, "|")
}()

var (
	everyRe = regexp.MustCompile(`(?i)\bevery\s+(?:(other|\d+)\s+)?(days?|weeks?|months?|years?|` +
		`monday|mon|tuesdays?|tues|tue|wednesdays?|weds|wed|thursdays?|thurs|thur|thu|fridays?|fri|saturdays?|sat|sundays?|sun)\b\.?`)
	keywordRe = regexp.MustCompile(`(?i)\b(daily|weekly|monthly|yearly|annually)\b`)
	// danglingRe is a connector left at the end of the title once the date
	// is cut out. "coming" is not one: it is usually a verb, as in
	// "Contractor coming at 7:30".
	danglingRe = regexp.MustCompile(`(?i)[\s,]*\b(on|at|by|due|this|next|the coming|the)\b[\s,]*$`)
	// recurringDanglingRe is the shorter list the service uses where it
	// cuts a recurrence out: the title can still end in "the" after that.
	recurringDanglingRe = regexp.MustCompile(`(?i)[\s,]*\b(on|at|by|due|this|next|the coming)\b[\s,]*$`)
	spacesRe            = regexp.MustCompile(`\s{2,}`)

	// Each date pattern is tried on the whole text. A match inside a longer
	// one is dropped, and of the rest the one that starts last wins, because
	// dates sit at the end of casual input.
	relativeRe = regexp.MustCompile(`(?i)\b(?:(?:the\s+)?day\s+(after|before)\s+)?` +
		`(today|tonight|tomorrow|tmrw|yesterday|this\s+(?:morning|afternoon|evening))` + partOfDay + `\b`)
	weekdayRe  = regexp.MustCompile(`(?i)\b(?:(this\s+coming|the\s+coming|this|next)\s+)?(` + weekdayWords + `)` + partOfDay + `\b`)
	weekendRe  = regexp.MustCompile(`(?i)\b(?:(this|next|the)\s+)?weekend\b`)
	inRe       = regexp.MustCompile(`(?i)\bin\s+(` + countWords + `)\s+(` + unitWords + `)s?\b`)
	fromRe     = regexp.MustCompile(`(?i)\b(` + countWords + `)\s+(` + unitWords + `)s?\s+(?:(?:from|after)\s+(now|today|tomorrow)|later)\b`)
	soonRe     = regexp.MustCompile(`(?i)\bin\s+(` + countWords + `)\s+(hour|hr|minute|min)s?\b`)
	nextUnitRe = regexp.MustCompile(`(?i)\bnext\s+(week|month|year)\b`)
	// afterNextRe is a weekday in the week after the next one, as the
	// service reads it: "friday after next" is a week past "next friday".
	afterNextRe = regexp.MustCompile(`(?i)\b(` + weekdayWords + `)\s+after\s+next\b`)
	weekAfterRe = regexp.MustCompile(`(?i)\b(?:the\s+)?week\s+after\s+next\b`)
	// endOfRe is the last day of the month or year. It is a date only, and
	// "end of the week" is deliberately not read: Friday or Sunday is a guess.
	endOfRe = regexp.MustCompile(`(?i)\b(?:the\s+)?(?:end|last\s+day)\s+of\s+(?:(the\s+next|next)\s+|the\s+|this\s+)?(month|year)\b`)
	// onTheRe is "on the 15th". It is only a day of the month when nothing
	// but a time follows (onTheEndRe), so "on the 3rd floor" is left alone.
	onTheRe    = regexp.MustCompile(`(?i)\bon\s+the\s+(\d{1,2})(?:st|nd|rd|th)\b`)
	onTheEndRe = regexp.MustCompile(`(?i)^(?:\s*$|\s+(?:at\b|\d{1,2}(?::\d{2})?\s*[ap]\.?m\b))`)
	// backwardRe is a phrase about the past. Capture never schedules into the
	// past, so a todo that ends in one gets no date.
	backwardRe = regexp.MustCompile(`(?i)\b(?:(?:the\s+)?day\s+before\s+yesterday|yesterday` +
		`|(?:last|previous|past)\s+(?:` + weekdayWords + `|week|weekend|month|year|night)` +
		`|(?:` + countWords + `)\s+(?:` + unitWords + `|hour|minute|second)s?\s+ago)\b`)
	// halfReadRe is what is left in front of a date when only part of its
	// phrase was understood ("a week from" friday, "end of" friday).
	halfReadRe = regexp.MustCompile(`(?i)\b(?:(?:a|an|\d+|[a-z]+)\s+(?:day|week|month|year)s?\s+(?:from|after)` +
		`|(?:end|beginning|start|middle)\s+of|last|previous|past` +
		`|(?:half\s+past|quarter\s+(?:past|to))(?:\s+(?:\d{1,2}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve))?)\s*$`)
	clockAfterMonth = regexp.MustCompile(`(?i)^(?::\d{2}|\s*(?:am|pm|o['’]clock)\b)`)
	monthDayRe      = regexp.MustCompile(`(?i)\b(` + monthWords + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?\b`)
	dayMonthRe      = regexp.MustCompile(`(?i)\b(?:the\s+)?(\d{1,2}|` + ordinalWords + `)(?:st|nd|rd|th)?\s+(?:of\s+)?(` +
		monthWords + `)\.?(?:,?\s+(\d{4}))?\b`)
	isoRe = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	// isoTimeRe is an ISO date with a time, which is read as the date
	// followed by the time.
	isoTimeRe = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})\b`)
	// slashRe is a numeric date. It is month first, as the service reads
	// it, unless the first number cannot be a month.
	slashRe = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})(?:/(\d{4}|\d{2}))?\b`)

	// unclaimedRe finds longer phrases that hold a date word but mean
	// something this reader does not work out. Reading the date word on its
	// own would give the wrong day, so a match that falls inside one of
	// these, without covering it, is not taken: "end of next week" is not
	// next week, and "a week from monday" is not monday.
	unclaimedRe = regexp.MustCompile(`(?i)\b(?:` +
		`weekend\s+after\s+next` +
		`|end\s+of\s+(?:the\s+|this\s+|next\s+)?(?:day|week|month|year)` +
		`|(?:` + countWords + `)\s+(?:` + unitWords + `|hour|minute)s?\s+(?:before|after|from)(?:\s+\w+)?` +
		`|day\s+(?:after|before)(?:\s+\w+)?` +
		`)\b`)

	clockRe  = regexp.MustCompile(`(?i)\b(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)
	clock24  = regexp.MustCompile(`(?i)\b(?:at\s+)?([01]?\d|2[0-3]):([0-5]\d)\b`)
	clockNom = regexp.MustCompile(`(?i)\b(?:at\s+)?(noon|midday|midnight)\b`)

	// An hour after "at", with or without minutes, that says nothing of am
	// or pm. Whether it is a bare time, one that leaves the half of the day
	// to be worked out, or a time as typed is decided in findClock.
	clockAtRe = regexp.MustCompile(`(?i)\bat\s+(\d{1,2})(?::(\d{2}))?`)
	// "5 o'clock", and "half past 5", "quarter past 5" and "quarter to 6".
	oclockRe = regexp.MustCompile(`(?i)\b(?:at\s+)?(1[0-2]|[1-9])\s+o'?clock\b`)
	pastToRe = regexp.MustCompile(`(?i)\b(?:at\s+)?(half\s+past|quarter\s+past|quarter\s+to)\s+` +
		`(1[0-2]|[1-9]|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\b`)
	// A part of the day after a time says which half it is: "at 3 in the
	// afternoon", "at 8 tonight".
	partAfterRe = regexp.MustCompile(`(?i)^(?:\s+(in\s+the)\s+(morning|afternoon|evening|night)\b` +
		`|\s+(?:this\s+)?(morning|afternoon|evening|night|tonight)\b)`)
	// notBareRe is what stops a number after "at" from being a time:
	// "at 5th", "at 5.30", "at 5,000", "at 5 pm" and "at 3 in the" something.
	notBareRe = regexp.MustCompile(`(?i)^(?:[\w:/-]|[.,]\d|\s*(?:[ap]\.?m\b|o'?clock|noon|midnight)|\s+in\s+the\b)`)
)

// datedRe finds words that suggest a line holds a date. "may", "mar",
// "sun", "sat" and "wed" are left out: they are more often other words.
var datedRe = regexp.MustCompile(`(?i)\b(?:` +
	`january|jan|february|feb|march|april|apr|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec` +
	`|monday|mon|tuesday|tues|tue|wednesday|weds|thursday|thurs|thur|thu|friday|fri|saturday|sunday` +
	`|today|tonight|tomorrow|tmrw|yesterday|weekend|fortnight|christmas|easter|noon|midnight` +
	`|(?:in|next|last|this|every)\s+(?:\w+\s+)?(?:days?|weeks?|fortnights?|months?|years?|hours?|minutes?)` +
	`|\d{1,2}(?:st|nd|rd|th)` +
	`)\b`)

// LooksDated reports whether text holds a word that suggests a date, and
// the first such word. A reading that found no date in such text likely
// missed one, which is worth telling the person before the todo goes to the
// inbox.
func LooksDated(text string) (string, bool) {
	w := datedRe.FindString(text)
	return w, w != ""
}

// span is a stretch of the text that was read as something.
type span struct{ from, to int }

func (s span) overlaps(o span) bool { return s.from < o.to && o.from < s.to }
func (s span) covers(o span) bool   { return s.from <= o.from && o.to <= s.to }

// Parse reads a line of capture text in loc, as of now.
func Parse(raw string, loc *time.Location, now time.Time) Parsed {
	text := strings.Join(strings.Fields(raw), " ")
	if text == "" {
		return Parsed{}
	}
	text = isoTimeRe.ReplaceAllString(text, "$1 $2")
	rule, rest := extractRecurrence(text)
	now = now.In(loc)

	found, haveDay := findDay(rest, now)
	daySpan := found.at
	var clk clock
	var haveClock bool
	if !found.backward {
		clk, haveClock = findClock(rest, daySpan, haveDay)
	}
	if found.timed {
		// "in an hour" is already a time; a clock beside it is not read.
		clk, haveClock = clock{hour: found.day.Hour(), minute: found.day.Minute()}, false
	}
	// A phrase about the past, or only half of one that was understood
	// ("a week from" friday), gets no date: that is better than a wrong one.
	if found.backward ||
		haveDay && halfRead(rest[:matchStart(rest, daySpan, clk.at, haveClock)]) ||
		haveClock && halfRead(rest[:matchStart(rest, clk.at, daySpan, haveDay)]) ||
		haveClock && besideUnclaimed(rest, clk.at, daySpan, haveDay) {
		haveDay, haveClock = false, false
	}

	if !haveDay && !haveClock {
		if rule == "" {
			return Parsed{Title: text}
		}
		// A recurrence without any other date still needs its first
		// occurrence.
		at := NextOccurrence(rule, loc, now, 0, 0)
		title := stripConnectors(rest)
		if title == "" {
			title = text
		}
		return Parsed{Title: title, DoAt: &at, Recurrence: rule}
	}

	// when is the moment for an hour on the 24-hour clock.
	when := func(hour int) time.Time {
		switch {
		case !haveDay && ruleWeekday(rule):
			// "every monday at 9am": the time belongs to the first occurrence.
			return firstWeekdayOccurrence(rule, loc, now, hour, clk.minute)
		case haveDay:
			d := found.day
			return time.Date(d.Year(), d.Month(), d.Day(), hour, clk.minute, 0, 0, loc)
		}
		// A time on its own means today, or tomorrow once it has passed.
		d := dateOf(now)
		at := time.Date(d.Year(), d.Month(), d.Day(), hour, clk.minute, 0, 0, loc)
		if !at.After(now) {
			at = time.Date(d.Year(), d.Month(), d.Day()+1, hour, clk.minute, 0, 0, loc)
		}
		return at
	}
	hour := clk.hour
	var assumed *AssumedTime
	if haveClock && clk.bare {
		// A time with a day of its own, or one that repeats, is read by the
		// hour. Without, it is the soonest the clock reaches it. "Next
		// month" names no day, so "next month at 8" is the second kind.
		pm := byHour(clk.hour)
		if (!haveDay || found.loose) && rule == "" {
			pm = soonest(clk.hour, clk.minute, now, loc)
		}
		hour = hour24(clk.hour, pm)
		assumed = &AssumedTime{
			Text:            rest[clk.at.from:clk.at.to],
			Assumed:         meridiem(pm),
			AlternativeText: spoken(clk.hour, clk.minute, !pm),
			AlternativeDoAt: when(hour24(clk.hour, !pm)),
		}
	}
	at := when(hour)

	title := cut(rest, daySpan, haveDay, clk.at, haveClock)
	if title == "" {
		title = text // never hand back an empty title
	}
	return Parsed{Title: title, DoAt: &at, HasTime: haveClock || found.timed, Recurrence: rule, Assumed: assumed}
}

// A time typed without am/pm is read as the half of the day people mostly
// meant, never the small hours and never late at night. The bounds, and the
// two ways of choosing, are the service's.
const (
	earliestBareHour = 6
	latestBareHour   = 21
)

func hour24(hour int, pm bool) int {
	if pm {
		return hour%12 + 12
	}
	return hour % 12
}

func meridiem(pm bool) string {
	if pm {
		return "pm"
	}
	return "am"
}

// byHour is the half of the day for a bare time on a day of its own: 7 to
// 11 is the morning, the rest the afternoon.
func byHour(hour int) (pm bool) { return hour < 7 || hour > 11 }

// soonest is the half of the day whose reading of a bare time comes soonest
// after now, among those between 06:00 and 21:00. At 9 PM "wake up at 8" is
// 8 AM tomorrow, at 10 AM "meet at 3" is 3 PM, and at 8 PM "call at 9" is 9
// AM tomorrow, not an hour from now.
func soonest(hour, minute int, now time.Time, loc *time.Location) (pm bool) {
	pm = true
	var best time.Time
	for _, half := range []bool{false, true} {
		h := hour24(hour, half)
		if h < earliestBareHour || h >= latestBareHour {
			continue
		}
		today := dateOf(now)
		for ahead := 0; ahead <= 1; ahead++ {
			at := time.Date(today.Year(), today.Month(), today.Day()+ahead, h, minute, 0, 0, loc)
			if !at.After(now) {
				continue
			}
			if best.IsZero() || at.Before(best) {
				pm, best = half, at
			}
			break
		}
	}
	return pm
}

// spoken is a time said outright, as the service puts it in the text that
// would replace a bare one: "at 8pm", "at 5:30am".
func spoken(hour, minute int, pm bool) string {
	out := "at " + strconv.Itoa(hour)
	if minute != 0 {
		out += fmt.Sprintf(":%02d", minute)
	}
	return out + meridiem(pm)
}

func halfRead(before string) bool { return halfReadRe.MatchString(before) }

// matchStart is where the date or time at s begins, counting a time or date
// right beside it as one phrase: in "half past 5 tomorrow" the words in front
// of tomorrow belong to the time.
func matchStart(text string, s, other span, haveOther bool) int {
	if haveOther && other.to <= s.from && strings.TrimSpace(text[other.to:s.from]) == "" {
		return other.from
	}
	return s.from
}

// stripConnectors takes connector words off the end of a title, however
// many are stacked: "Clean house at the" weekend is "Clean house". A title
// that is only connectors keeps its last one, so "Due by" sept 30 is "Due".
func stripConnectors(title string) string {
	return strings.Trim(dropConnectors(title), " ,")
}

// dropConnectors is stripConnectors without tidying the ends of the text,
// for a stretch that sits in the middle of a title.
func dropConnectors(text string) string {
	out := danglingRe.ReplaceAllString(text, "")
	for {
		next := danglingRe.ReplaceAllString(out, "")
		if next == out || strings.Trim(next, " ,") == "" {
			return out
		}
		out = next
	}
}

// cut removes the date and time words from the text and tidies what is
// left, including connectors such as "at the" stranded at the end.
func cut(text string, day span, haveDay bool, clock span, haveClock bool) string {
	var spans []span
	if haveDay {
		spans = append(spans, day)
	}
	if haveClock {
		spans = append(spans, clock)
	}
	if len(spans) == 2 && spans[0].from > spans[1].from {
		spans[0], spans[1] = spans[1], spans[0]
	}
	// Cut from the end so the earlier offsets stay valid. Connectors are
	// only taken from the stretch in front of a span that follows the other
	// one, so that the other is not eaten.
	for i := len(spans) - 1; i >= 0; i-- {
		s, lo := spans[i], 0
		if i > 0 {
			lo = spans[i-1].to
		}
		text = text[:lo] + dropConnectors(text[lo:s.from]) + " " + text[s.to:]
	}
	text = strings.Trim(spacesRe.ReplaceAllString(text, " "), " ,")
	if out := stripConnectors(text); out != "" {
		return out
	}
	return text
}

func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// dayMatch is a stretch of text read as a day, or as a moment when timed.
type dayMatch struct {
	day   time.Time
	at    span
	timed bool
	// backward is a phrase about the past, which gets no date at all.
	backward bool
	// loose is a month or a year with no day of its own ("in 2 months", "next
	// year"): a bare time beside it is chosen as if no day had been given.
	loose bool
}

// afterColon is whether a match starts with a number that is the minutes of a
// time: in "at 10:45 day after tomorrow" the 45 is not "45 days after
// tomorrow".
func afterColon(text string, m []int) bool {
	return m[0] > 0 && text[m[0]-1] == ':' && text[m[0]] >= '0' && text[m[0]] <= '9'
}

// atHourRe is the hour of a time at the start of a match, with the space
// after it, and atBeforeRe is the "at" in front of it.
var (
	atHourRe   = regexp.MustCompile(`^\d{1,2}\b\s*`)
	atBeforeRe = regexp.MustCompile(`(?i)\bat\s+$`)
)

// afterAt is where a match starts once the hour of a time is taken off its
// front, or the match's own start if it does not begin that way: in "at 10 day
// after tomorrow" the 10 is the hour, not "10 days after tomorrow". What is
// left of the phrase is still not a count, so it is kept, but without the hour.
func afterAt(text string, m []int) int {
	if !atBeforeRe.MatchString(text[:m[0]]) {
		return m[0]
	}
	if h := atHourRe.FindString(text[m[0]:m[1]]); h != "" {
		return m[0] + len(h)
	}
	return m[0]
}

// unclaimedSpans are the phrases in the text that hold a date word and mean
// something this reader does not work out.
func unclaimedSpans(text string) []span {
	var out []span
	for _, m := range unclaimedRe.FindAllStringIndex(text, -1) {
		if !afterColon(text, m) {
			out = append(out, span{afterAt(text, m), m[1]})
		}
	}
	return out
}

// besideUnclaimed is whether the time sits right next to a phrase that this
// reader does not work out and that no day was read from. The time then
// belongs to that phrase: "end of the week at 5pm" is not today at 5pm.
func besideUnclaimed(text string, clock, day span, haveDay bool) bool {
	blank := func(s string) bool { return strings.Trim(s, " ,") == "" }
	for _, u := range unclaimedSpans(text) {
		if haveDay && day.covers(u) {
			continue
		}
		if u.overlaps(clock) || u.to <= clock.from && blank(text[u.to:clock.from]) || clock.to <= u.from && blank(text[clock.to:u.from]) {
			return true
		}
	}
	return false
}

// findDay looks for a date in the text. Of the matches that are not part
// of a longer one, it returns the one starting last.
func findDay(text string, now time.Time) (dayMatch, bool) {
	today := dateOf(now)
	unclaimed := unclaimedSpans(text)
	var all []dayMatch
	add := func(m []int, day time.Time, loose bool) {
		s := span{m[0], m[1]}
		for _, u := range unclaimed {
			if s.overlaps(u) && !s.covers(u) {
				return
			}
		}
		all = append(all, dayMatch{day: day, at: s, loose: loose})
	}
	take := func(m []int, day time.Time) { add(m, day, false) }
	// takeUnit is take for a day counted in units, of which months and years
	// leave the day of the month to be worked out.
	takeUnit := func(m []int, day time.Time, unit string) { add(m, day, unit == "month" || unit == "year") }
	sub := func(m []int, i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return strings.ToLower(text[m[2*i]:m[2*i+1]])
	}
	each := func(re *regexp.Regexp, fn func(m []int)) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if !afterColon(text, m) {
				fn(m)
			}
		}
	}

	each(relativeRe, func(m []int) {
		n := 0
		switch sub(m, 2) {
		case "tomorrow", "tmrw":
			n = 1
		case "yesterday":
			return // backwardRe has it
		}
		switch sub(m, 1) {
		case "after":
			n++
		case "before":
			n--
		}
		take(m, today.AddDate(0, 0, n))
	})
	each(weekdayRe, func(m []int) {
		// Forward to the next such day, or today if it is that day; "next
		// friday" is the one in the week after this one.
		ahead := (int(weekdays[sub(m, 2)]) - int(today.Weekday()) + 7) % 7
		if sub(m, 1) == "next" {
			if ahead == 0 {
				ahead = 7
			}
			if ahead <= 7-weekdayIndex(today.Weekday()) {
				ahead += 7
			}
		}
		take(m, today.AddDate(0, 0, ahead))
	})
	each(weekendRe, func(m []int) {
		// The weekend starts on Saturday; on a Sunday it is today.
		ahead := (int(time.Saturday) - int(today.Weekday()) + 7) % 7
		if today.Weekday() == time.Sunday {
			ahead = 0
		}
		if sub(m, 1) == "next" {
			ahead += 7
		}
		take(m, today.AddDate(0, 0, ahead))
	})
	each(afterNextRe, func(m []int) {
		// The weekday a week after the coming one, which is never today.
		ahead := (weekdayIndex(weekdays[sub(m, 1)])-weekdayIndex(today.Weekday())+6)%7 + 1
		take(m, today.AddDate(0, 0, ahead+7))
	})
	each(weekAfterRe, func(m []int) {
		take(m, today.AddDate(0, 0, 14))
	})
	each(endOfRe, func(m []int) {
		ahead := 0
		if sub(m, 1) != "" {
			ahead = 1
		}
		if sub(m, 2) == "year" {
			take(m, time.Date(today.Year()+ahead, time.December, 31, 0, 0, 0, 0, today.Location()))
			return
		}
		// The day before the first of the month after.
		first := time.Date(today.Year(), today.Month()+time.Month(ahead+1), 1, 0, 0, 0, 0, today.Location())
		take(m, first.AddDate(0, 0, -1))
	})
	each(onTheRe, func(m []int) {
		d, _ := strconv.Atoi(sub(m, 1))
		if d < 1 || d > 31 || !onTheEndRe.MatchString(text[m[1]:]) {
			return
		}
		take(m, nextDayOfMonth(today, d))
	})
	each(inRe, func(m []int) {
		takeUnit(m, addUnit(today, count(sub(m, 1)), sub(m, 2)), sub(m, 2))
	})
	each(fromRe, func(m []int) {
		if afterAt(text, m) != m[0] {
			return // the hour of a time, so not a count; unclaimedRe holds the rest
		}
		base := today
		if sub(m, 3) == "tomorrow" {
			base = today.AddDate(0, 0, 1)
		}
		take(m, addUnit(base, count(sub(m, 1)), sub(m, 2)))
	})
	each(nextUnitRe, func(m []int) {
		takeUnit(m, addUnit(today, 1, sub(m, 1)), sub(m, 1))
	})
	monthDay := func(m []int, month time.Month, day, year string) {
		d, ok := ordinals[strings.ReplaceAll(day, " ", "-")]
		if !ok {
			d, _ = strconv.Atoi(day)
		}
		y := today.Year()
		if year != "" {
			y, _ = strconv.Atoi(year)
			if y < 100 {
				y += 2000
			}
		}
		at := time.Date(y, month, d, 0, 0, 0, 0, today.Location())
		if d < 1 || at.Day() != d {
			return // no such day, such as 31 april
		}
		// With no year given, a date that has passed means next year's.
		if year == "" && at.Before(today) {
			at = at.AddDate(1, 0, 0)
		}
		take(m, at)
	}
	each(monthDayRe, func(m []int) {
		// A number following the month can be the hour after a day/month date.
		if clockAfterMonth.MatchString(text[m[5]:]) {
			return
		}
		monthDay(m, months[sub(m, 1)], sub(m, 2), sub(m, 3))
	})
	each(dayMonthRe, func(m []int) { monthDay(m, months[sub(m, 2)], sub(m, 1), sub(m, 3)) })
	each(slashRe, func(m []int) {
		a, _ := strconv.Atoi(sub(m, 1))
		b, _ := strconv.Atoi(sub(m, 2))
		if a > 12 {
			a, b = b, a
		}
		if a >= 1 && a <= 12 {
			monthDay(m, time.Month(a), strconv.Itoa(b), sub(m, 3))
		}
	})
	each(isoRe, func(m []int) {
		if d, err := time.ParseInLocation("2006-01-02", text[m[0]:m[1]], today.Location()); err == nil {
			take(m, d)
		}
	})
	each(backwardRe, func(m []int) {
		all = append(all, dayMatch{at: span{afterAt(text, m), m[1]}, backward: true})
	})
	timed := len(all)
	each(soonRe, func(m []int) {
		d := time.Hour
		if u := sub(m, 2); u == "minute" || u == "min" {
			d = time.Minute
		}
		take(m, now.Add(time.Duration(count(sub(m, 1)))*d).Truncate(time.Minute))
	})
	for i := timed; i < len(all); i++ {
		all[i].timed = true
	}

	var best dayMatch
	found := false
	for i, c := range all {
		inside := false
		for j, o := range all {
			if i != j && o.at.covers(c.at) && o.at != c.at {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		if !found || c.at.from > best.at.from || c.at.from == best.at.from && c.at.to >= best.at.to {
			best, found = c, true
		}
	}
	return best, found
}

// count reads a number written as digits or as a word.
func count(s string) int {
	if n, ok := counts[s]; ok {
		return n
	}
	n, _ := strconv.Atoi(s)
	return n
}

// weekdayIndex counts a weekday from Monday, 1 to 7.
func weekdayIndex(d time.Weekday) int {
	if d == time.Sunday {
		return 7
	}
	return int(d)
}

func addUnit(day time.Time, n int, unit string) time.Time {
	switch unit {
	case "week":
		return day.AddDate(0, 0, 7*n)
	case "month":
		return addMonths(day, n)
	case "year":
		return addMonths(day, 12*n)
	}
	return day.AddDate(0, 0, n)
}

// addMonths moves a day by calendar months, stopping at the end of a month
// that is too short: a month after 31 January is 28 February.
func addMonths(day time.Time, n int) time.Time {
	first := time.Date(day.Year(), day.Month()+time.Month(n), 1, 0, 0, 0, 0, day.Location())
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day.Day(), last), 0, 0, 0, 0, day.Location())
}

// nextDayOfMonth is the next time the day of the month comes round, today
// included. A month that has no such day, such as the 31st of September, is
// skipped.
func nextDayOfMonth(today time.Time, d int) time.Time {
	ahead := 0
	if today.Day() > d {
		ahead = 1
	}
	for ; ahead < 14; ahead++ {
		first := time.Date(today.Year(), today.Month()+time.Month(ahead), 1, 0, 0, 0, 0, today.Location())
		if d <= first.AddDate(0, 1, -1).Day() {
			return first.AddDate(0, 0, d-1)
		}
	}
	return today
}

// clock is a time of day found in the text.
type clock struct {
	hour, minute int
	at           span
	// bare is a time typed without am/pm. Its hour is the one typed, 1 to
	// 12, and which half of the day it is has yet to be worked out.
	bare bool
}

// findClock looks for a time of day. A bare hour and minute such as 17:30
// is only taken as a time when it does not sit inside the date.
func findClock(text string, day span, haveDay bool) (c clock, ok bool) {
	var taken []span
	// claim keeps a stretch of the text from being read as a time twice, and
	// says whether it was free.
	claim := func(s span) bool {
		if haveDay && s.overlaps(day) {
			return false
		}
		// "5:30pm" was already read; do not read its "5:30" or "at 5" again.
		for _, t := range taken {
			if s.overlaps(t) {
				return false
			}
		}
		taken = append(taken, s)
		return true
	}
	take := func(s span, h, min int, bare bool) {
		if h > 23 || min > 59 || !claim(s) {
			return
		}
		if !ok || s.from >= c.at.from {
			c, ok = clock{hour: h, minute: min, at: s, bare: bare}, true
		}
	}
	num := func(m []int, i int) int {
		if m[2*i] < 0 {
			return 0
		}
		n, _ := strconv.Atoi(text[m[2*i]:m[2*i+1]])
		return n
	}
	// bare reads an hour typed without am/pm, unless what follows it says
	// which half of the day, or that it is not a time at all.
	bare := func(s span, h, min int) {
		rest := text[s.to:]
		if p := partAfterRe.FindStringSubmatchIndex(rest); p != nil {
			if p[2] >= 0 {
				s.to += p[1] // "in the afternoon" goes with the time
				take(s, inPartOfDay(h, strings.ToLower(rest[p[4]:p[5]])), min, false)
			} else {
				take(s, inPartOfDay(h, strings.ToLower(rest[p[6]:p[7]])), min, false)
			}
			return
		}
		if notBareRe.MatchString(rest) {
			claim(s)
			return
		}
		take(s, h, min, true)
	}
	for _, m := range clockRe.FindAllStringSubmatchIndex(text, -1) {
		h := num(m, 1)
		if h < 1 || h > 12 {
			continue
		}
		pm := strings.EqualFold(text[m[6]:m[7]], "pm")
		switch {
		case pm && h < 12:
			h += 12
		case !pm && h == 12:
			h = 0
		}
		take(span{m[0], m[1]}, h, num(m, 2), false)
	}
	for _, m := range pastToRe.FindAllStringSubmatchIndex(text, -1) {
		n, word := counts[strings.ToLower(text[m[4]:m[5]])]
		if !word {
			n = num(m, 2)
		}
		switch strings.Join(strings.Fields(strings.ToLower(text[m[2]:m[3]])), " ") {
		case "half past":
			bare(span{m[0], m[1]}, n, 30)
		case "quarter past":
			bare(span{m[0], m[1]}, n, 15)
		default:
			// A quarter to 1 is 12:45.
			bare(span{m[0], m[1]}, (n+10)%12+1, 45)
		}
	}
	for _, m := range oclockRe.FindAllStringSubmatchIndex(text, -1) {
		bare(span{m[0], m[1]}, num(m, 1), 0)
	}
	for _, m := range clockAtRe.FindAllStringSubmatchIndex(text, -1) {
		s, h, min := span{m[0], m[1]}, num(m, 1), num(m, 2)
		switch {
		case min > 59:
			claim(s)
		case h == 12 && partAfterRe.MatchString(text[s.to:]):
			bare(s, h, min)
		case h >= 1 && h <= 11 && text[m[2]] != '0':
			bare(s, h, min)
		case notBareRe.MatchString(text[s.to:]):
			claim(s)
		default:
			take(s, h, min, false)
		}
	}
	for _, m := range clock24.FindAllStringSubmatchIndex(text, -1) {
		take(span{m[0], m[1]}, num(m, 1), num(m, 2), false)
	}
	for _, m := range clockNom.FindAllStringSubmatchIndex(text, -1) {
		if strings.EqualFold(text[m[2]:m[3]], "midnight") {
			take(span{m[0], m[1]}, 0, 0, false)
		} else {
			take(span{m[0], m[1]}, 12, 0, false)
		}
	}
	return c, ok
}

// inPartOfDay is an hour typed from 1 to 12 on the 24-hour clock when a part
// of the day goes with it: "8 in the evening" is 20, "8 in the morning" is 8.
func inPartOfDay(h int, part string) int {
	switch part {
	case "morning":
		return h % 12
	case "afternoon", "evening":
		if h < 12 {
			return h + 12
		}
	default: // night, tonight
		if h >= 6 && h < 12 {
			return h + 12
		}
		return h % 12
	}
	return h
}

// -- recurrence -------------------------------------------------------------

// A rule is "N:unit[:weekday]", weekday 1 for Monday to 7 for Sunday, which
// is the form the service stores.

// extractRecurrence finds and removes a phrase such as "every wednesday",
// "every 3 months" or "daily".
func extractRecurrence(text string) (rule, remainder string) {
	strip := func(m []int) string {
		out := spacesRe.ReplaceAllString(text[:m[0]]+" "+text[m[1]:], " ")
		return strings.TrimSpace(recurringDanglingRe.ReplaceAllString(out, ""))
	}
	if m := everyRe.FindStringSubmatchIndex(text); m != nil {
		interval := 1
		if m[2] >= 0 {
			if c := strings.ToLower(text[m[2]:m[3]]); c == "other" {
				interval = 2
			} else if n, err := strconv.Atoi(c); err == nil && n > 0 {
				interval = min(n, 365)
			}
		}
		unit := strings.ToLower(text[m[4]:m[5]])
		if d, ok := weekdays[strings.TrimSuffix(unit, "s")]; ok {
			return strconv.Itoa(interval) + ":week:" + strconv.Itoa(weekdayIndex(d)), strip(m)
		}
		if d, ok := weekdays[unit]; ok {
			return strconv.Itoa(interval) + ":week:" + strconv.Itoa(weekdayIndex(d)), strip(m)
		}
		return strconv.Itoa(interval) + ":" + strings.TrimSuffix(unit, "s"), strip(m)
	}
	if m := keywordRe.FindStringSubmatchIndex(text); m != nil {
		unit := map[string]string{"daily": "day", "weekly": "week", "monthly": "month"}[strings.ToLower(text[m[2]:m[3]])]
		if unit == "" {
			unit = "year"
		}
		return "1:" + unit, strip(m)
	}
	return "", text
}

type rule struct {
	interval int
	unit     string
	weekday  int
}

// ruleWeekday reports whether a rule falls on a given day of the week.
func ruleWeekday(ruleText string) bool {
	r, ok := parseRule(ruleText)
	return ok && r.weekday > 0
}

// firstWeekdayOccurrence is where a weekly rule on a day of the week starts
// when a time was typed with it: today while that time is still ahead and
// today is that day, otherwise the next such day.
func firstWeekdayOccurrence(ruleText string, loc *time.Location, now time.Time, hour, minute int) time.Time {
	if r, ok := parseRule(ruleText); ok && weekdayIndex(now.Weekday()) == r.weekday {
		if today := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc); today.After(now) {
			return today
		}
	}
	return NextOccurrence(ruleText, loc, now, hour, minute)
}

// ValidRule reports whether s is a recurrence rule the service accepts.
func ValidRule(s string) bool {
	if TextUnits(s) > 64 {
		return false
	}
	_, ok := parseRule(s)
	return ok
}

func parseRule(s string) (rule, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return rule{}, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return rule{}, false
	}
	switch parts[1] {
	case "day", "week", "month", "year":
	default:
		return rule{}, false
	}
	r := rule{interval: n, unit: parts[1]}
	if len(parts) == 3 {
		d, err := strconv.Atoi(parts[2])
		if err != nil || d < 1 || d > 7 {
			return rule{}, false
		}
		r.weekday = d
	}
	return r, true
}

// NextOccurrence is the next time a rule falls strictly after a moment, at
// the given time of day in loc. A rule that cannot be read gives back the
// moment it was asked about.
func NextOccurrence(ruleText string, loc *time.Location, after time.Time, hour, minute int) time.Time {
	r, ok := parseRule(ruleText)
	if !ok {
		return after
	}
	w := after.In(loc)
	at := func(years, months, days int) time.Time {
		d := time.Date(w.Year()+years, w.Month()+time.Month(months), w.Day()+days, 0, 0, 0, 0, time.UTC)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc)
	}
	switch r.unit {
	case "day":
		for i := r.interval; i <= 400; i += r.interval {
			if t := at(0, 0, i); t.After(after) {
				return t
			}
		}
	case "week":
		if r.weekday > 0 {
			for i := 1; i <= 28; i++ {
				if t := at(0, 0, i); weekdayIndex(t.Weekday()) == r.weekday && t.After(after) {
					return t
				}
			}
			return after
		}
		for i := 7 * r.interval; i <= 400; i += 7 * r.interval {
			if t := at(0, 0, i); t.After(after) {
				return t
			}
		}
	case "month":
		for i := r.interval; i <= 120; i += r.interval {
			if t := at(0, i, 0); t.After(after) {
				return t
			}
		}
	case "year":
		for i := r.interval; i <= 40; i += r.interval {
			if t := at(i, 0, 0); t.After(after) {
				return t
			}
		}
	}
	return after
}
