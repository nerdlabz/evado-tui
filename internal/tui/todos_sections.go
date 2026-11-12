package tui

import (
	"slices"
	"sort"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// How the todos on screen are grouped into dated sections, ordered inside
// them, laid out as the board's columns and counted for the header. All of
// it is worked out again from the loaded todos, by rebuild, whenever they
// change.

// todoRow is one line of the list: either a section heading or a todo.
type todoRow struct {
	header bool
	label  string
	hint   string
	count  int
	style  lipgloss.Style
	index  int // index into todosView.items
}

// section order, most pressing first. The inbox sits high on purpose: it is
// where most captures land, and burying it is what makes people lose work.
var sectionOrder = []string{"overdue", "today", "inbox", "tomorrow", "later", "done"}

// sectionHeading describes how one section is presented. The tone is there
// so urgency is legible before the words are read, without depending on
// colour: the label itself always names the section.
type sectionHeading struct {
	label string
	style lipgloss.Style
}

func newSectionHeading(key string) sectionHeading {
	bold := lipgloss.NewStyle().Bold(true)
	switch key {
	case "overdue":
		return sectionHeading{"Overdue", bold.Foreground(colRed)}
	case "today":
		return sectionHeading{"Today", bold.Foreground(colBlue)}
	case "tomorrow":
		return sectionHeading{"Tomorrow", bold.Foreground(colAmber)}
	case "inbox":
		return sectionHeading{"Inbox", bold.Foreground(colAccent)}
	case "later":
		return sectionHeading{"Later", bold.Foreground(colMuted)}
	default:
		// Done is history, so it is presented quietly rather than competing
		// with what still needs doing.
		return sectionHeading{"Done", lipgloss.NewStyle().Foreground(colFaint)}
	}
}

// donePreview is how many completed todos the Everything view shows before
// it defers to the completed filter.
const donePreview = 5

// rebuild recomputes the sections and the flattened line list, keeping the
// cursor on the same todo when it still exists, and the counts for the
// header.
func (v *todosView) rebuild(keepID string) {
	v.arrange(keepID)
	v.computeStats()
}

// arrange is rebuild without the counts, for a change that only narrows
// what is shown. The board is only laid out when it is on screen; turning
// it on goes through rebuild, which lays it out then.
func (v *todosView) arrange(keepID string) {
	buckets := make(map[string][]int, len(sectionOrder))
	now := v.m.now
	y, mo, d := now.In(v.m.loc).Date()
	v.regroupAt = time.Date(y, mo, d+1, 0, 0, 0, 0, v.m.loc)
	for i, t := range v.items {
		sec := v.sectionOf(t)
		buckets[sec] = append(buckets[sec], i)
		if t.DueAt != nil && !t.DueAt.Before(now) && t.DueAt.Before(v.regroupAt) {
			v.regroupAt = *t.DueAt
		}
	}

	v.lines = v.lines[:0]
	for _, key := range sectionOrder {
		idxs := buckets[key]
		if len(idxs) == 0 {
			continue
		}
		// Order inside the section is decided here, not left to whatever
		// order the API returned: a section that is not sorted is just a
		// list.
		v.sortSection(key, idxs)
		if key == "done" {
			// The most recent completion is the one most likely to be the
			// subject of "did that go through?".
			idxs = idxs[max(len(idxs)-donePreview, 0):]
			slices.Reverse(idxs)
		}
		hint := ""
		if key == "done" && len(buckets[key]) > len(idxs) {
			hint = "press f for all done"
		}
		heading := newSectionHeading(key)
		v.lines = append(v.lines, todoRow{
			header: true, label: heading.label, count: len(idxs), hint: hint,
			style: heading.style,
		})
		for _, i := range idxs {
			v.lines = append(v.lines, todoRow{index: i})
		}
	}
	v.rows.setItems(len(v.lines))

	// Put the cursor back on the same todo, or on the first real one.
	target := -1
	if keepID != "" {
		for r, l := range v.lines {
			if !l.header && v.items[l.index].ID == keepID {
				target = r
				break
			}
		}
	}
	if target < 0 {
		target = v.firstTodoRow()
	}
	if target >= 0 {
		v.rows.cursor = target
		v.rows.clamp()
	}
	if v.filter.board {
		v.buildBoard(keepID)
	} else {
		v.boardStale = true
	}
}

// regroup lays the sections out again once the time has come for a todo
// to move on its own, as one does at midnight or when it falls due. The
// counts are worked out on every tick, and would otherwise disagree with
// the list until something else rebuilt it.
func (v *todosView) regroup() {
	// A todo is overdue once its due time is past, not at it.
	if v.regroupAt.IsZero() || !v.m.now.After(v.regroupAt) {
		return
	}
	v.arrange(v.selectedID())
}

// sectionOf decides which bucket a todo belongs to, in the account timezone.
func (v *todosView) sectionOf(t api.Todo) string {
	// A todo that is still being ticked off holds its place until the
	// animation ends.
	if t.Completed() && !v.animating(t.ID) {
		return "done"
	}
	now := v.m.now
	today := dayKey(now.In(v.m.loc))
	day := t.ScheduledDay
	if day == nil && t.DoAt != nil {
		d := t.DoAt.In(v.m.loc).Format("2006-01-02")
		day = &d
	}
	switch {
	case day == nil && t.DueAt == nil:
		// No date anywhere: the inbox, where a plain capture lands.
		return "inbox"
	case t.DueAt != nil && t.DueAt.Before(now):
		return "overdue"
	}
	if day == nil {
		if t.DueAt != nil {
			dd := dayKey(t.DueAt.In(v.m.loc))
			if dd == today {
				return "today"
			}
		}
		return "later"
	}
	parsed, err := time.ParseInLocation("2006-01-02", *day, v.m.loc)
	if err != nil {
		return "later"
	}
	switch d := dayKey(parsed) - today; {
	case d < 0:
		return "overdue"
	case d == 0:
		return "today"
	case d == 1:
		return "tomorrow"
	default:
		return "later"
	}
}

// sortSection orders the todos inside one section. The API's ordering is
// not relied on here, because bucketing happens on this side and a section
// that is not sorted is just a list.
func (v *todosView) sortSection(key string, idxs []int) {
	// The key is worked out once for each todo rather than for each
	// comparison, which parses a date every time.
	type entry struct {
		idx int
		key int64
	}
	es := make([]entry, len(idxs))
	for i, ix := range idxs {
		es[i] = entry{ix, v.sortKey(key, &v.items[ix])}
	}
	// Scheduled days come first because that is what the user typed; the
	// rest is a stable tiebreak so a re-render never shuffles the list.
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.key != b.key {
			return a.key < b.key
		}
		ta, tb := &v.items[a.idx], &v.items[b.idx]
		if ta.HasTime != tb.HasTime {
			return ta.HasTime
		}
		return ta.Title < tb.Title
	})
	for i, e := range es {
		idxs[i] = e.idx
	}
}

func (v *todosView) sortKey(key string, t *api.Todo) int64 {
	switch key {
	case "done":
		if t.CompletedAt != nil {
			return t.CompletedAt.Unix()
		}
		return t.UpdatedAt.Unix()
	case "overdue":
		// Oldest deadline first: that is the one that has waited longest.
		if t.DueAt != nil {
			return t.DueAt.Unix()
		}
		if t.ScheduledDay != nil {
			if d, err := time.ParseInLocation("2006-01-02", *t.ScheduledDay, v.m.loc); err == nil {
				return d.Unix()
			}
		}
		return t.UpdatedAt.Unix()
	default:
		// Soonest scheduled day first. Undated sorts last, which the
		// Inbox section already makes obvious.
		if t.ScheduledDay != nil {
			if d, err := time.ParseInLocation("2006-01-02", *t.ScheduledDay, v.m.loc); err == nil {
				return d.Unix()
			}
		}
		if t.DoAt != nil {
			return t.DoAt.Unix()
		}
		if t.DueAt != nil {
			return t.DueAt.Unix()
		}
		return 1<<62 - 1
	}
}

func (v *todosView) firstTodoRow() int {
	for r, l := range v.lines {
		if !l.header {
			return r
		}
	}
	return -1
}

// -- the board --------------------------------------------------------------

// boardCol is one column of the board: the inbox, what is overdue, a day,
// or everything after the days shown.
type boardCol struct {
	key, label, sub string
	tone            lipgloss.Color
	idxs            []int
}

// boardDays is how many days the board gives a column each.
const boardDays = 7

// buildBoard sorts the open todos into the board's columns, keeping the
// cursor on the same todo, or failing that in the same column.
func (v *todosView) buildBoard(keepID string) {
	m := v.m
	prev := ""
	if v.bcol >= 0 && v.bcol < len(v.bcols) {
		prev = v.bcols[v.bcol].key
	}
	today := m.now.In(m.loc)
	cols := []boardCol{
		{key: "inbox", label: "Inbox", sub: "no day yet", tone: colAccent},
		{key: "overdue", label: "Overdue", sub: "needs a new day", tone: colRed},
	}
	for d := 0; d < boardDays; d++ {
		day := today.AddDate(0, 0, d)
		c := boardCol{key: day.Format("2006-01-02"), label: day.Format("Monday"), sub: day.Format("2 January"), tone: colMuted}
		switch d {
		case 0:
			c.label, c.tone = "Today", colBlue
		case 1:
			c.label, c.tone = "Tomorrow", colAmber
		}
		cols = append(cols, c)
	}
	cols = append(cols, boardCol{key: "later", label: "Later", sub: "after " + today.AddDate(0, 0, boardDays-1).Format("2 January"), tone: colMuted})

	for i, t := range v.items {
		at := len(cols) - 1
		switch v.sectionOf(t) {
		case "done":
			continue
		case "inbox":
			at = 0
		case "overdue":
			at = 1
		default:
			if day, ok := m.todoDay(t); ok {
				if d := dayKey(day) - dayKey(today); d >= 0 && d < boardDays {
					at = 2 + d
				}
			}
		}
		cols[at].idxs = append(cols[at].idxs, i)
	}
	kept := cols[:0]
	for _, c := range cols {
		// Nothing overdue is good news, not a column.
		if c.key == "overdue" && len(c.idxs) == 0 {
			continue
		}
		v.sortSection(c.key, c.idxs)
		kept = append(kept, c)
	}
	v.bcols = kept
	v.boardStale = false

	if keepID != "" && v.boardFind(keepID) {
		return
	}
	for i, c := range v.bcols {
		if c.key == prev {
			v.bcol = i
			v.brow = min(v.brow, max(len(c.idxs)-1, 0))
			return
		}
	}
	// A fresh board opens on the first column with something in it.
	v.bcol, v.brow = 0, 0
	for i, c := range v.bcols {
		if len(c.idxs) > 0 {
			v.bcol = i
			return
		}
	}
}

// boardFind puts the board's cursor on a todo, reporting whether it is
// on the board at all.
func (v *todosView) boardFind(id string) bool {
	if v.boardStale {
		return false
	}
	for ci, c := range v.bcols {
		for ri, i := range c.idxs {
			if v.items[i].ID == id {
				v.bcol, v.brow = ci, ri
				return true
			}
		}
	}
	return false
}

// -- counts -----------------------------------------------------------------

// todoStats counts the account the way the sections do.
type todoStats struct {
	open, overdue, dueToday, upcoming, done, doneToday int
	perList                                            map[string]int
	// byDay counts the open todos on each day, and late marks the days
	// holding something overdue. Both are keyed by dayKey.
	byDay map[int]int
	late  map[int]bool
	// recent is how many todos were finished on each of the last fourteen
	// days, oldest first, and streak the run of days with at least one.
	recent [14]int
	streak int
}

// stats is the summary of the whole account as of the last rebuild.
func (v *todosView) stats() todoStats { return v.st }

// computeStats recounts the account from the last unfiltered load.
func (v *todosView) computeStats() {
	m := v.m
	s := todoStats{perList: map[string]int{}, byDay: map[int]int{}, late: map[int]bool{}}
	today := dayKey(m.now.In(m.loc))
	for _, t := range v.snapshot {
		sec := v.sectionOf(t)
		if sec == "done" {
			s.done++
			if t.CompletedAt != nil {
				ago := today - dayKey(t.CompletedAt.In(m.loc))
				if ago == 0 {
					s.doneToday++
				}
				if ago >= 0 && ago < len(s.recent) {
					s.recent[len(s.recent)-1-ago]++
				}
			}
			continue
		}
		s.open++
		s.perList[t.ListID]++
		switch sec {
		case "overdue":
			s.overdue++
		case "today":
			s.dueToday++
		case "tomorrow", "later":
			s.upcoming++
		}
		if day, ok := m.todoDay(t); ok {
			s.byDay[dayKey(day)]++
			if sec == "overdue" {
				s.late[dayKey(day)] = true
			}
		}
	}
	// A day with nothing finished yet does not break a streak until it is
	// over, so the count may start from yesterday.
	i := len(s.recent) - 1
	if s.recent[i] == 0 {
		i--
	}
	for ; i >= 0 && s.recent[i] > 0; i-- {
		s.streak++
	}
	v.st = s
}
