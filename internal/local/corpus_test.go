package local

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// reading is how a phrase was read, in a form both readers can be compared
// in: the day and time of day in the corpus timezone, empty for none.
type reading struct {
	Title string `json:"title"`
	Day   string `json:"day"`
	Time  string `json:"time"`
	Rule  string `json:"rule"`
	// Assumed is "am" or "pm" when a time was typed without one and a
	// reading was guessed, empty otherwise.
	Assumed string `json:"assumed"`
	// Passed is "kept" or "moved" when the time had already passed today,
	// empty otherwise.
	Passed string `json:"passed"`
}

func (r reading) String() string {
	when := r.Day
	if when == "" {
		when = "no date"
	}
	if r.Time != "" {
		when += " " + r.Time
	}
	if r.Rule != "" {
		when += " every " + r.Rule
	}
	if r.Assumed != "" {
		when += " (assumed " + r.Assumed + ")"
	}
	if r.Passed != "" {
		when += " (passed, " + r.Passed + ")"
	}
	return fmt.Sprintf("%s, %q", when, r.Title)
}

// corpus is testdata/parse_corpus.json: phrases with what the service's
// parser made of them on a known day. A phrase with a local reading is one
// the reader here deliberately reads differently, and why says so.
type corpus struct {
	Now       time.Time `json:"now"`
	TZ        string    `json:"tz"`
	DateOrder string    `json:"dateOrder"`
	Phrases   []struct {
		In string `json:"in"`
		// Now is when the phrase was read, for one such as "in an hour"
		// whose reading moves with the clock.
		Now *time.Time `json:"now"`
		// DateOrder is how the account reads a date written in numbers,
		// for a phrase read the other way round from the corpus's own.
		DateOrder string   `json:"dateOrder"`
		Service   reading  `json:"service"`
		Local     *reading `json:"local"`
		Why       string   `json:"why"`
	} `json:"phrases"`
}

// when is the instant a phrase was read at.
func (c corpus) when(at *time.Time) time.Time {
	if at != nil {
		return *at
	}
	return c.Now
}

func readCorpus(t *testing.T) corpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/parse_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// order is how numeric dates read for a phrase: its own, or the corpus's.
func (c corpus) order(own string) string {
	if own != "" {
		return own
	}
	return c.DateOrder
}

func localReading(in string, loc *time.Location, now time.Time, dateOrder string) reading {
	p := ParseWith(in, loc, now, Options{DateOrder: dateOrder})
	r := reading{Title: p.Title, Rule: p.Recurrence, Passed: p.TimePassed}
	if p.Assumed != nil {
		r.Assumed = p.Assumed.Assumed
	}
	if p.DoAt != nil {
		r.Day = p.DoAt.In(loc).Format("2006-01-02")
		if p.HasTime {
			r.Time = p.DoAt.In(loc).Format("15:04")
		}
	}
	return r
}

// TestParseCorpus holds the reader here to the service's readings. Each
// phrase must read as the service read it, or as its recorded local reading,
// which must then differ from the service's: a difference is a decision,
// not an accident, and one that has gone away is noticed.
func TestParseCorpus(t *testing.T) {
	c := readCorpus(t)
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		t.Fatal(err)
	}
	// A phrase is in the corpus again only to be read at another moment or
	// with numeric dates the other way round.
	type key struct {
		in    string
		now   time.Time
		order string
	}
	seen := map[key]bool{}
	for _, p := range c.Phrases {
		k := key{p.In, c.when(p.Now), c.order(p.DateOrder)}
		if seen[k] {
			t.Errorf("%q is in the corpus twice for %s", p.In, k.now.Format(time.RFC3339))
		}
		seen[k] = true
		got := localReading(p.In, loc, c.when(p.Now), c.order(p.DateOrder))
		switch {
		case p.Local == nil && got != p.Service:
			t.Errorf("%q: read here as %s; the service read %s", p.In, got, p.Service)
		case p.Local != nil && p.Why == "":
			t.Errorf("%q: a local reading needs a why", p.In)
		case p.Local != nil && *p.Local == p.Service:
			t.Errorf("%q: the local reading is the service's; drop it", p.In)
		case p.Local != nil && got != *p.Local:
			t.Errorf("%q: read here as %s; the corpus expects %s (service: %s)", p.In, got, *p.Local, p.Service)
		}
	}
}

// TestParseCorpusTable prints the two readings side by side, for a pull
// request or for checking a change by eye: go test -run CorpusTable -v.
func TestParseCorpusTable(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("prints a table; run with -v")
	}
	c := readCorpus(t)
	loc, _ := time.LoadLocation(c.TZ)
	var b strings.Builder
	b.WriteString("| phrase | service | here |\n| --- | --- | --- |\n")
	for _, p := range c.Phrases {
		got := localReading(p.In, loc, c.when(p.Now), c.order(p.DateOrder))
		here := "same"
		if got != p.Service {
			here = got.String()
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.In, p.Service, here)
	}
	t.Log("\n" + b.String())
}
