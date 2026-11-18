package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// -- layout -----------------------------------------------------------------

// todoLayout is how the Todos tab divides the screen: a sidebar of views
// and lists, the list or board, and a pane for the todo under the cursor.
// The side panes give way as the terminal narrows.
type todoLayout struct {
	sideW, mainW, prevW int
	h                   int
}

const sidebarWidth = 26

func (v *todosView) layout(width, height int) todoLayout {
	l := todoLayout{h: height}
	// The board wants every column it can get, so it keeps the sidebar
	// only when there is width to spare.
	if width >= 84 && height >= 10 && !v.hideSide && (!v.filter.board || width >= 130) {
		l.sideW = sidebarWidth
	}
	rest := width - l.sideW
	if !v.filter.board && rest >= 90 && height >= 10 {
		l.prevW = min(max(rest*2/5, 40), 60)
	}
	l.mainW = rest - l.prevW
	return l
}

func (v *todosView) view(width, height int) string {
	m := v.m
	l := v.layout(width, height)
	var panes []string
	if l.sideW > 0 {
		panes = append(panes, v.sidebarPane(l.sideW, l.h))
	}
	// On a wide terminal the open todo sits beside the list. Otherwise it
	// takes the list's place, and esc brings the list back.
	if m.inDetail() && l.prevW == 0 {
		panes = append(panes, v.detailPane(l.mainW, l.h))
	} else {
		panes = append(panes, v.mainPane(l.mainW, l.h))
		if l.prevW > 0 {
			panes = append(panes, v.detailPane(l.prevW, l.h))
		}
	}
	return joinPanes(panes...)
}

// framed reports whether a pane this size is drawn with a border. A very
// small one spends every cell on content instead.
func framed(w, h int) bool { return w >= 24 && h >= 6 }

func (v *todosView) mainPane(w, h int) string {
	if !framed(w, h) {
		if v.repair != nil {
			return clipBlock(v.repairBody(w, h), w, h)
		}
		return clipBlock(v.listBody(w, h), w, h)
	}
	var body string
	switch {
	case v.repair != nil:
		body = v.repairBody(w-2, h-2)
	case v.filter.board:
		body = v.boardBody(w-2, h-2)
	default:
		body = v.listBody(w-2, h-2)
	}
	return frame(body, w, h, frameOpts{
		title:   v.paneTitle(),
		foot:    v.paneFoot(w - 8),
		focused: v.m.focus == focusList,
	})
}

// -- the list ---------------------------------------------------------------

func (v *todosView) listBody(w, h int) string {
	extra := ""
	if v.parsePreview != nil {
		extra = v.parseView(w)
		h -= lipgloss.Height(extra) + 1
	}
	v.rows.setHeight(max(h, 1))
	var out string
	if len(v.lines) == 0 {
		out = v.emptyState(w, h)
	} else {
		out = v.rows.render(w, func(i int, sel bool) string {
			return v.line(i, sel, w-1)
		})
	}
	if extra != "" {
		out = clipBlock(out, w, max(h, 1)) + "\n\n" + extra
	}
	return out
}

// paneTitle says what is on screen and, just as importantly, when the view
// is narrower than the account. A saved view puts its name first, and all
// its filters are still listed after it, so opening one says what it holds.
func (v *todosView) paneTitle() string {
	if v.repair != nil {
		return v.repair.view.Name + " · needs repair"
	}
	parts := v.m.filterParts(v.filter, "", "")
	if sv, ok := v.activeView(); ok {
		parts = append([]string{sv.Name}, parts...)
	}
	return strings.Join(parts, " · ")
}

// filterParts name what a filter narrows to, one part for each thing. A
// list or person that is not known by now is called by the name the
// caller has for it, which is what a saved view kept.
func (m *Model) filterParts(f todoFilter, list, person string) []string {
	parts := []string{f.scopeLabel()}
	if f.board {
		parts = append([]string{"Board"}, parts...)
	}
	if f.listID != "" {
		name := m.listName(f.listID)
		if _, ok := m.listByID(f.listID); !ok && list != "" {
			name = list
		}
		parts = append(parts, name)
	}
	switch f.assigned {
	case "":
	case "me":
		parts = append(parts, "assigned to me")
	case "anyone":
		parts = append(parts, "unassigned")
	default:
		name := m.personName(f.assigned)
		if name == shortID(f.assigned) && person != "" {
			name = person
		}
		parts = append(parts, "assigned to "+name)
	}
	if f.undated {
		parts = append(parts, "undated")
	}
	if f.query != "" {
		parts = append(parts, "search "+strconv.Quote(f.query))
	}
	return parts
}

// repairBody is what the list area says while a view needs repair: what
// is missing, and that nothing was opened in its place.
func (v *todosView) repairBody(w, h int) string {
	r := v.repair
	lines := []string{styWarn.Bold(true).Render("“" + r.view.Name + "” needs repair"), ""}
	for _, is := range r.issues {
		lines = append(lines, styWarn.Render("!")+" "+styMuted.Render(capitalize(is.what)+"."))
	}
	lines = append(lines, "",
		styFaint.Render("It was not opened, so nothing was narrowed or widened by guess."),
		styFaint.Render("It held: "+strings.Join(v.m.filterParts(filterOf(r.view), r.view.ListName, r.view.AssignedName), " · ")),
		"",
		styAccent.Render("enter")+styFaint.Render(" pick a replacement or drop that part  ·  ")+styAccent.Render("esc")+styFaint.Render(" leave it"),
	)
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)
	block = lipgloss.NewStyle().MaxWidth(max(w, 1)).Render(block)
	if w < 10 || h < len(lines)+2 {
		return clipBlock(block, w, h)
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
}

// paneFoot carries the count, and says the quiet part out loud: a narrowed
// view, or a capped result, is not the same as "you have finished".
func (v *todosView) paneFoot(width int) string {
	var notes []string
	if len(v.marks) > 0 {
		notes = append(notes, fmt.Sprintf("%d marked", len(v.marks)))
	}
	if v.moreToLoad() {
		notes = append(notes, fmt.Sprintf("showing the first %d · L for more", len(v.all)))
	}
	if v.filter.mode == viewStatus {
		notes = append(notes, "f shows everything")
	}
	count := styMuted.Render(plural(len(v.items), "todo"))
	// Notes are dropped from the end until the line fits the edge.
	for n := len(notes); n >= 0; n-- {
		foot := count
		if n > 0 {
			foot = styWarn.Render(strings.Join(notes[:n], " · ")) + styFaint.Render(" · ") + count
		}
		if visibleWidth(foot) <= width {
			return foot
		}
	}
	return ""
}

// rowCols is how a todo row divides its width. Columns are dropped as the
// pane narrows: the list name first, then the clock, then the day.
type rowCols struct {
	titleW, listW, whenW int
}

func (v *todosView) cols(width int) rowCols {
	c := rowCols{}
	switch {
	case width >= 64:
		c.whenW = 16
	case width >= 36:
		c.whenW = 10
	}
	// The list column only earns its place when there is more than one
	// list on screen.
	if width >= 72 && v.filter.listID == "" && len(v.m.lists) > 1 {
		c.listW = 12
	}
	used := 3 // cursor bar, mark, space
	for _, w := range []int{c.listW, c.whenW} {
		if w > 0 {
			used += w + 1
		}
	}
	c.titleW = max(width-used, 6)
	return c
}

// completeFrames is the mark a todo shows while it is being ticked off.
var completeFrames = []string{"◔", "◑", "◕", "●"}

// todoMark is the checkbox for a todo.
func (v *todosView) todoMark(t api.Todo) (string, lipgloss.Style) {
	switch {
	case v.marks[t.ID]:
		return "◉", styAccent
	case t.Tombstoned():
		return "~", styFaint
	case v.animating(t.ID):
		p := v.animProgress(t.ID)
		return completeFrames[min(int(p*float64(len(completeFrames))), len(completeFrames)-1)], styOK
	case t.Completed():
		return "✓", styOK
	case v.sectionOf(t) == "overdue":
		return "○", styOver
	}
	return "○", styMuted
}

// titleCell renders a title w cells wide. While a todo is being completed
// the strike-through sweeps across it, so finishing something is seen to
// happen rather than the row just changing.
func (v *todosView) titleCell(t api.Todo, w int, base lipgloss.Style, selected bool) string {
	title := truncate(t.Title, w)
	done := base.Foreground(colFaint).Strikethrough(true)
	live := base.Foreground(colFg)
	if selected {
		live = live.Bold(true)
	}
	var out string
	switch {
	case v.animating(t.ID):
		r := []rune(title)
		n := min(int(v.animProgress(t.ID)*float64(len(r)+1)), len(r))
		out = done.Render(string(r[:n])) + live.Render(string(r[n:]))
	case t.Completed() || t.Tombstoned():
		out = done.Render(title)
	default:
		out = live.Render(title)
	}
	return out + base.Render(strings.Repeat(" ", max(w-visibleWidth(title), 0)))
}

// flags are the small marks after a title: repeats, has notes, has a
// reminder waiting, is assigned to someone.
func (v *todosView) flags(t api.Todo) []string {
	m := v.m
	var f []string
	if t.RecurrenceRule != nil && *t.RecurrenceRule != "" {
		f = append(f, "↻")
	}
	if t.Notes != nil && strings.TrimSpace(*t.Notes) != "" {
		f = append(f, "≡")
	}
	if t.RemindAt != nil && !t.RemindAt.Before(m.now) && !t.Completed() {
		f = append(f, "◷")
	}
	if t.AssigneeID != nil {
		f = append(f, "@")
	}
	if done, total := v.stepCount(t.ID); total > 0 {
		f = append(f, fmt.Sprintf("%d/%d", done, total))
	}
	return f
}

func (m *Model) todoRow(v *todosView, t api.Todo, selected bool, width int) string {
	c := v.cols(width)
	base := lipgloss.NewStyle()
	bar := " "
	if selected {
		base = base.Background(colPanel)
		if m.focus == focusList || m.inDetail() {
			bar = base.Foreground(colAccent).Render("▌")
		} else {
			bar = base.Foreground(colFaint).Render("▌")
		}
	}
	on := func(st lipgloss.Style) lipgloss.Style {
		if selected {
			return st.Background(colPanel)
		}
		return st
	}

	mark, markSty := v.todoMark(t)
	flags := strings.Join(v.flags(t), " ")
	titleW := c.titleW
	flagCell := ""
	if flags != "" && titleW > visibleWidth(flags)+8 {
		titleW -= visibleWidth(flags) + 1
		flagCell = base.Render(" ") + on(styFaint).Render(flags)
	}

	row := bar + on(markSty).Render(mark) + base.Render(" ") +
		v.titleCell(t, titleW, base, selected) + flagCell
	if c.listW > 0 {
		row += base.Render(" ") + m.listCell(t.ListID, c.listW, on)
	}
	if c.whenW > 0 {
		row += base.Render(" ") + m.whenCell(t, c.whenW, on)
	}
	return row + base.Render(strings.Repeat(" ", max(width-visibleWidth(row), 0)))
}

// listCell is the list a todo belongs to, as a coloured dot and its name.
func (m *Model) listCell(id string, w int, on func(lipgloss.Style) lipgloss.Style) string {
	name := m.listName(id)
	dot := on(lipgloss.NewStyle().Foreground(tagColor(id))).Render("●")
	text := on(styMuted)
	if l, ok := m.listByID(id); ok && l.ReadOnly {
		dot = on(styFaint).Render("⊘")
		text = on(styFaint)
	}
	label := truncate(name, max(w-2, 1))
	return dot + on(lipgloss.NewStyle()).Render(" ") + text.Render(label) +
		on(lipgloss.NewStyle()).Render(strings.Repeat(" ", max(w-2-visibleWidth(label), 0)))
}

// whenTone colours a date by urgency: late, today, soon, or later.
func (m *Model) whenTone(t api.Todo, day time.Time) lipgloss.Style {
	diff := dayKey(day) - dayKey(m.now.In(m.loc))
	switch {
	case t.Completed():
		return styFaint
	case t.DueAt != nil && t.DueAt.Before(m.now):
		return styOver
	case diff < 0:
		return styOver
	case diff == 0:
		return stySoon
	case diff <= 2:
		return styWarn
	}
	return styIdle
}

// todoDay is the day a todo belongs to: the day it is planned for, or the
// day it is due when it has no plan.
func (m *Model) todoDay(t api.Todo) (time.Time, bool) {
	if t.ScheduledDay != nil {
		if d, err := time.ParseInLocation("2006-01-02", *t.ScheduledDay, m.loc); err == nil {
			return d, true
		}
	}
	for _, at := range []*time.Time{t.DoAt, t.DueAt} {
		if at != nil {
			return at.In(m.loc), true
		}
	}
	return time.Time{}, false
}

// todoClock is the one time of day that matters most: the planned time,
// then the deadline.
func (m *Model) todoClock(t api.Todo) string {
	switch {
	case t.HasTime && t.DoAt != nil:
		return formatClock(t.DoAt, m.loc)
	case t.DueAt != nil:
		return formatClock(t.DueAt, m.loc)
	}
	return ""
}

// whenCell renders the day and time, right-aligned and coloured by
// urgency. An undated todo leaves it blank: the Inbox heading above those
// rows already says they have no date.
func (m *Model) whenCell(t api.Todo, w int, on func(lipgloss.Style) lipgloss.Style) string {
	blank := on(lipgloss.NewStyle())
	if t.Tombstoned() {
		return blank.Render(strings.Repeat(" ", max(w-7, 0))) + on(styFaint).Render("deleted")
	}
	day, ok := m.todoDay(t)
	if !ok {
		return blank.Render(strings.Repeat(" ", w))
	}
	text := formatDay(&day, m.loc, m.now)
	if clock := m.todoClock(t); clock != "" && visibleWidth(text)+1+len(clock) <= w {
		text += " " + clock
	}
	text = truncate(text, w)
	return blank.Render(strings.Repeat(" ", max(w-visibleWidth(text), 0))) + on(m.whenTone(t, day)).Render(text)
}

// line renders one row: a section heading, or a todo.
func (v *todosView) line(i int, sel bool, width int) string {
	l := v.lines[i]
	if l.header {
		return v.headerRow(l, width)
	}
	return v.m.todoRow(v, v.items[l.index], sel, width)
}

// headerRow is a section heading: the label in the section's own tone, a
// count once there is more than one, and a quiet rule out to the edge so
// the groups read as groups.
func (v *todosView) headerRow(l todoRow, width int) string {
	head := " " + l.style.Render(l.label)
	if l.count > 1 {
		head += styFaint.Render(fmt.Sprintf(" %d", l.count))
	}
	tail := ""
	if l.hint != "" {
		tail = " " + styFaint.Render(l.hint)
	}
	fill := width - visibleWidth(head) - visibleWidth(tail) - 1
	if fill < 2 {
		return head
	}
	return head + " " + lipgloss.NewStyle().Foreground(colBorder).Render(strings.Repeat("─", fill)) + tail
}

// emptyState explains an empty list instead of shrugging at it, and offers
// the action that fills it.
func (v *todosView) emptyState(w, h int) string {
	var lines []string
	switch {
	case v.filter.query != "":
		lines = []string{
			styMuted.Render(fmt.Sprintf("Nothing matches %q.", v.filter.query)),
			styFaint.Render("esc clears the search"),
		}
	case v.filter.mode == viewStatus:
		lines = []string{
			styMuted.Render("No " + strings.ToLower(strings.TrimSuffix(v.filter.scopeLabel(), " only")) + " todos."),
			styFaint.Render("f shows everything, including your inbox"),
		}
	case !v.filter.unfiltered():
		// Narrowed by a list, a person or undated, so it is not that
		// everything is done.
		lines = []string{
			styMuted.Render("Nothing matches these filters."),
			styFaint.Render("the title above lists them; S opens a saved view"),
		}
	case v.tooBig:
		// Not empty: too big to read, which the default would hide by
		// saying every todo is done.
		lines = []string{
			styMuted.Render(fmt.Sprintf("This account has more than %s todos,", groupDigits(accountLimit))),
			styMuted.Render("the most this version reads at once."),
			styFaint.Render("F shows one list and / searches"),
			styFaint.Render("evado ls -cursor reads them all in a shell"),
		}
	default:
		// Everything is empty: genuinely nothing left.
		if h >= catchyRows+6 && w >= 40 {
			lines = append(lines, strings.Split(renderCatchy(), "\n")...)
			lines = append(lines, "")
		}
		lines = append(lines,
			styOK.Bold(true).Render("Every todo is done."),
			styFaint.Render("n adds one  ·  N captures several at once"),
		)
	}
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)
	if w < 10 || h < len(lines)+2 {
		return block
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
}

// asTyped is the words a todo was captured from, when they say more than
// its title does. A capture of several lines is kept whole on every todo it
// made, so the line this one came from is picked out: the one that holds
// its title. When no line does, because the date sat in the middle of the
// words, nothing is shown: every line of the capture would be the other
// todos' text, not this one's.
func asTyped(t api.Todo) string {
	if t.RawInput == nil {
		return ""
	}
	typed := strings.TrimSpace(*t.RawInput)
	if strings.Contains(typed, "\n") {
		lines := typed
		typed = ""
		for _, line := range strings.Split(lines, "\n") {
			if line = strings.TrimSpace(line); line != "" && strings.Contains(line, t.Title) {
				typed = line
				break
			}
		}
	}
	if typed == t.Title {
		return ""
	}
	return typed
}

// parseView shows how the server read a title, without creating anything.
func (v *todosView) parseView(width int) string {
	p := v.parsePreview
	lines := []string{" " + styFaint.Render("how that reads") + "  " + styMuted.Render(strconv.Quote(v.parseTitle))}
	lines = append(lines, "   "+v.m.parseSummary(p))
	return lipgloss.NewStyle().MaxWidth(width).Render(strings.Join(lines, "\n"))
}

// parseSummary says what the server made of a line of text.
func (m *Model) parseSummary(p *api.ParsePreview) string {
	var bits []string
	switch {
	case p.ScheduledDay != nil:
		bits = append(bits, "on "+m.dayLabel(p.ScheduledDay))
	case p.DoAt != nil:
		bits = append(bits, "on "+formatDay(p.DoAt, m.loc, m.now))
	}
	if p.DoAt != nil && p.HasTime {
		at := "at " + p.DoAt.In(m.loc).Format("15:04")
		// A guess says so, and how to say the other half of the day.
		if a := p.AssumedTime; a != nil {
			if hint := a.Hint(); hint != "" {
				at += " (assumed; " + hint + ")"
			}
		}
		bits = append(bits, at)
	}
	if p.RecurrenceRule != nil && *p.RecurrenceRule != "" {
		bits = append(bits, recurLabel(*p.RecurrenceRule))
	}
	if p.DueAt != nil {
		bits = append(bits, "due "+p.DueAt.In(m.loc).Format("2 Jan 15:04"))
	}
	// A time that had already gone when it was read: which day it ended up
	// on is the one thing the date alone does not explain.
	switch p.Passed() {
	case api.TimeKept:
		bits = append(bits, "time passed, no reminder")
	case api.TimeMoved:
		bits = append(bits, "passed today, so tomorrow")
	}
	if len(bits) == 0 {
		// Text that names a day but was not read is worth a warning: the
		// person likely meant a date and would otherwise find the todo in
		// the inbox with the words still in its title.
		if p.KeptAsName != "" {
			return styMuted.Render(fmt.Sprintf("%q kept as a name, so it goes to the inbox", p.KeptAsName))
		}
		if w, ok := local.LooksDated(p.Title); ok {
			return styWarn.Render(fmt.Sprintf("%q was not understood as a date, so it goes to the inbox", w))
		}
		return styMuted.Render("no date, so it goes to the inbox")
	}
	if p.KeptAsName != "" {
		bits = append(bits, fmt.Sprintf("%q kept as a name", p.KeptAsName))
	}
	return styOK.Render(strings.Join(bits, " · "))
}

// -- the sidebar ------------------------------------------------------------

// sideItem is one row of the sidebar: a view, or a list to narrow to.
type sideItem struct {
	label string
	icon  string
	tone  lipgloss.Color
	count int
	// A view item carries the mode and status it switches to; a list item
	// carries the list, or nothing for every list. A saved view is opened
	// by its label, and broken says part of it is missing.
	isList  bool
	isSaved bool
	broken  bool
	mode    viewMode
	status  string
	listID  string
}

func (v *todosView) sideItems() []sideItem {
	s := v.st
	items := []sideItem{
		{label: "Everything", icon: "◆", tone: colAccent, count: s.open, mode: viewEverything},
		{label: "Overdue", icon: "▲", tone: colRed, count: s.overdue, mode: viewStatus, status: api.StatusOverdue},
		{label: "Today", icon: "●", tone: colBlue, count: s.dueToday, mode: viewStatus, status: api.StatusToday},
		{label: "Upcoming", icon: "◇", tone: colAmber, count: s.upcoming, mode: viewStatus, status: api.StatusUpcoming},
		{label: "Completed", icon: "✓", tone: colGreen, count: s.done, mode: viewStatus, status: api.StatusCompleted},
	}
	for _, sv := range v.savedViews() {
		items = append(items, sideItem{
			label: sv.Name, icon: "◈", tone: colAccent, isSaved: true,
			broken: len(v.issuesOf(sv)) > 0,
		})
	}
	items = append(items, sideItem{label: "All lists", icon: "○", tone: colMuted, isList: true})
	for _, l := range v.m.lists {
		it := sideItem{label: l.Name, icon: "●", tone: tagColor(l.ID), count: s.perList[l.ID], isList: true, listID: l.ID}
		if l.ReadOnly {
			it.icon, it.tone = "⊘", colFaint
		}
		items = append(items, it)
	}
	return items
}

// sideViews is how many of the sidebar's items are views; the saved views
// and then the lists follow.
const sideViews = 5

// sideActive reports whether an item is what the list is showing now.
func (v *todosView) sideActive(it sideItem) bool {
	if it.isSaved {
		if v.repair != nil {
			return config.SameName(v.repair.view.Name, it.label)
		}
		sv, ok := v.activeView()
		return ok && config.SameName(sv.Name, it.label)
	}
	if it.isList {
		return it.listID == v.filter.listID
	}
	if it.mode == viewEverything {
		return v.filter.mode == viewEverything
	}
	return v.filter.mode == viewStatus && v.filter.status == it.status
}

func (v *todosView) sideRow(it sideItem, cursor bool, w int) string {
	base := lipgloss.NewStyle()
	bar := " "
	if cursor {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	active := v.sideActive(it)
	text := base.Foreground(colMuted)
	num := base.Foreground(colFaint)
	if active {
		text = base.Foreground(colFg).Bold(true)
		num = base.Foreground(colAccent).Bold(true)
	}
	count := ""
	if it.count > 0 {
		count = strconv.Itoa(it.count)
	}
	if it.broken {
		// A view that cannot open says so before it is tried.
		count = "!"
		num = base.Foreground(colAmber).Bold(true)
	}
	labelW := max(w-5-len(count), 1)
	name := truncate(it.label, labelW)
	return bar + base.Foreground(it.tone).Render(it.icon) + base.Render(" ") +
		text.Render(name) + base.Render(strings.Repeat(" ", max(labelW-visibleWidth(name), 0)+1)) +
		num.Render(count) + base.Render(" ")
}

// sidebarLines lays the sidebar out. itemAt gives the item drawn on each
// line, or -1, which is what lets a click find what it landed on.
func (v *todosView) sidebarLines(w, h int) (lines []string, itemAt []int) {
	m := v.m
	items := v.sideItems()
	focused := m.focus == focusSidebar
	add := func(s string, item int) {
		lines = append(lines, s)
		itemAt = append(itemAt, item)
	}
	for i := 0; i < sideViews; i++ {
		add(v.sideRow(items[i], focused && v.side == i, w), i)
	}

	// Saved views and lists follow, and scroll when there are more than
	// fit, keeping the cursor or the active one in view. The headings
	// scroll with them, so a long list of views cannot push the lists off.
	type tailRow struct {
		text string
		item int
	}
	var tail []tailRow
	saved := 0
	for _, it := range items[sideViews:] {
		if it.isSaved {
			saved++
		}
	}
	if saved > 0 {
		tail = append(tail, tailRow{"", -1}, tailRow{" " + label("saved"), -1})
		for i := sideViews; i < sideViews+saved; i++ {
			tail = append(tail, tailRow{v.sideRow(items[i], focused && v.side == i, w), i})
		}
	}
	tail = append(tail, tailRow{"", -1}, tailRow{" " + label("lists"), -1})
	for i := sideViews + saved; i < len(items); i++ {
		tail = append(tail, tailRow{v.sideRow(items[i], focused && v.side == i, w), i})
	}
	room := max(h-len(lines), 1)
	start := 0
	if len(tail) > room {
		at := 0
		for i, r := range tail {
			if r.item < 0 {
				continue
			}
			if (focused && v.side == r.item) || (!focused && v.sideActive(items[r.item])) {
				at = i
			}
		}
		start = min(max(at-room/2, 0), len(tail)-room)
		tail = tail[start : start+room]
	}
	for _, r := range tail {
		add(r.text, r.item)
	}

	// Whatever is left goes to the month and the recent record, pinned to
	// the bottom so they do not jump as lists come and go.
	var extras []string
	stats := v.statsLines(w)
	cal := v.calendarLines(w)
	left := h - len(lines)
	if left >= len(stats)+1 {
		extras = stats
		left -= len(stats) + 1
		if left >= len(cal)+1 {
			extras = append(append(cal, ""), stats...)
		}
	}
	if len(extras) > 0 {
		for len(lines) < h-len(extras) {
			add("", -1)
		}
		for _, l := range extras {
			add(l, -1)
		}
	}
	return lines, itemAt
}

func (v *todosView) sidebarPane(w, h int) string {
	lines, _ := v.sidebarLines(w-2, h-2)
	return frame(strings.Join(lines, "\n"), w, h, frameOpts{
		title:   "Views",
		focused: v.m.focus == focusSidebar,
	})
}

// calendarLines draws this month. A day with something planned is
// coloured, a day holding something late is red, and today is filled.
func (v *todosView) calendarLines(w int) []string {
	m := v.m
	now := m.now.In(m.loc)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.loc)
	pad := strings.Repeat(" ", max((w-20)/2, 0))
	title := now.Format("January 2006")
	lines := []string{
		pad + strings.Repeat(" ", max((20-len(title))/2, 0)) + styBold.Render(title),
		pad + styFaint.Render("Mo Tu We Th Fr Sa Su"),
	}
	// Monday starts the week.
	col := (int(first.Weekday()) + 6) % 7
	row := strings.Repeat("   ", col)
	for d := first; d.Month() == now.Month(); d = d.AddDate(0, 0, 1) {
		key := dayKey(d)
		cell := fmt.Sprintf("%2d", d.Day())
		switch {
		case key == dayKey(now):
			cell = styPill.Render(cell)
		case v.st.late[key]:
			cell = styOver.Bold(true).Render(cell)
		case v.st.byDay[key] > 0:
			cell = stySoon.Bold(true).Render(cell)
		case key < dayKey(now):
			cell = styFaint.Render(cell)
		default:
			cell = styMuted.Render(cell)
		}
		row += cell
		col++
		if col == 7 {
			lines = append(lines, pad+row)
			row, col = "", 0
		} else {
			row += " "
		}
	}
	if strings.TrimSpace(row) != "" {
		lines = append(lines, pad+row)
	}
	return lines
}

// statsLines is the recent record: what was finished each day for two
// weeks, and the run of days with something finished.
func (v *todosView) statsLines(w int) []string {
	s := v.st
	total := 0
	for _, n := range s.recent {
		total += n
	}
	pad := strings.Repeat(" ", max((w-20)/2, 0))
	lines := []string{
		pad + label("done · 14 days"),
		pad + sparkline(s.recent[:]) + styMuted.Render(fmt.Sprintf(" %d", total)),
	}
	switch {
	case s.streak >= 2:
		lines = append(lines, pad+styWarn.Render("▲ ")+styMuted.Render(fmt.Sprintf("%d-day streak", s.streak)))
	case s.doneToday > 0:
		lines = append(lines, pad+styOK.Render("✓ ")+styMuted.Render(fmt.Sprintf("%d done today", s.doneToday)))
	default:
		lines = append(lines, pad+styFaint.Render("nothing done yet today"))
	}
	return lines
}

// -- the board --------------------------------------------------------------

// boardColWidth is the narrowest a board column is drawn.
const boardColWidth = 24

// cardRows is how many rows one card takes, with the gap under it.
const cardRows = 3

// boardWindow is how many columns fit, and how wide each is.
func (v *todosView) boardWindow(w int) (n, cw int) {
	n = min(max(w/boardColWidth, 1), max(len(v.bcols), 1))
	return n, w / n
}

func (v *todosView) boardBody(w, h int) string {
	if len(v.bcols) == 0 {
		return v.emptyState(w, h)
	}
	n, cw := v.boardWindow(w)
	v.bcol = min(max(v.bcol, 0), len(v.bcols)-1)
	// Scroll sideways just far enough to keep the cursor's column in view.
	if v.bcol < v.boff {
		v.boff = v.bcol
	}
	if v.bcol >= v.boff+n {
		v.boff = v.bcol - n + 1
	}
	v.boff = min(max(v.boff, 0), len(v.bcols)-n)
	cols := make([]string, 0, n)
	for i := v.boff; i < v.boff+n; i++ {
		cols = append(cols, v.boardColumn(i, cw, h))
	}
	return joinPanes(cols...)
}

func (v *todosView) boardColumn(i, w, h int) string {
	m := v.m
	col := v.bcols[i]
	active := i == v.bcol && m.focus == focusList
	tone := lipgloss.NewStyle().Foreground(col.tone).Bold(true)
	count := ""
	if len(col.idxs) > 0 {
		count = styFaint.Render(strconv.Itoa(len(col.idxs)))
	}
	rule := lipgloss.NewStyle().Foreground(colBorder)
	if active {
		rule = lipgloss.NewStyle().Foreground(col.tone)
	}
	lines := []string{
		hstack(" "+tone.Render(col.label), count+" ", w),
		" " + styFaint.Render(truncate(col.sub, max(w-2, 1))),
		" " + rule.Render(strings.Repeat("━", max(w-2, 0))),
	}

	// Cards scroll inside the column to keep the cursor in view.
	fit := max((h-boardHead)/cardRows, 1)
	start := 0
	if i == v.bcol && v.brow >= fit {
		start = v.brow - fit + 1
	}
	end := min(start+fit, len(col.idxs))
	for r := start; r < end; r++ {
		lines = append(lines, v.card(v.items[col.idxs[r]], active && r == v.brow, w)...)
	}
	if len(col.idxs) == 0 {
		lines = append(lines, " "+styFaint.Render("nothing here"))
	}
	if more := len(col.idxs) - end; more > 0 && len(lines) < h {
		lines = append(lines, " "+styFaint.Render(fmt.Sprintf("+%d more", more)))
	}
	return clipBlock(strings.Join(lines, "\n"), w, h)
}

// card is one todo on the board: the title, and under it the time and the
// list, with a gap below.
func (v *todosView) card(t api.Todo, selected bool, w int) []string {
	m := v.m
	base := lipgloss.NewStyle()
	bar := " "
	if selected {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	on := func(st lipgloss.Style) lipgloss.Style {
		if selected {
			return st.Background(colPanel)
		}
		return st
	}
	inner := max(w-1, 4)
	mark, markSty := v.todoMark(t)
	top := bar + on(markSty).Render(mark) + base.Render(" ") + v.titleCell(t, max(inner-3, 1), base, selected)

	var meta []string
	if clock := m.todoClock(t); clock != "" {
		meta = append(meta, clock)
	}
	if v.filter.listID == "" && len(m.lists) > 1 {
		meta = append(meta, m.listName(t.ListID))
	}
	meta = append(meta, v.flags(t)...)
	sub := truncate(strings.Join(meta, " "), max(inner-3, 1))
	bottom := bar + base.Render("  ") + on(styFaint).Render(sub) +
		base.Render(strings.Repeat(" ", max(inner-3-visibleWidth(sub), 0)))
	return []string{top, bottom, ""}
}

// -- the open todo ----------------------------------------------------------

// detailPane shows one todo in full: the open one, or on a wide terminal
// the one under the cursor.
func (v *todosView) detailPane(w, h int) string {
	m := v.m
	t, steps, haveSteps := v.detail, v.steps, true
	if !m.inDetail() {
		sel := v.selected()
		if sel == nil {
			t = api.Todo{}
		} else {
			t = *sel
		}
		// The preview draws its own checklist from what the list has read.
		haveSteps = false
	}
	if !framed(w, h) {
		return clipBlock(v.todoCard(t, steps, haveSteps, w, h), w, h)
	}
	foot := ""
	if t.ID != "" {
		foot = styFaint.Render(shortID(t.ID))
	}
	return frame(v.todoCard(t, steps, haveSteps, w-2, h-2), w, h, frameOpts{
		title:   "Todo",
		foot:    foot,
		focused: m.inDetail(),
	})
}

// recurLabel reads a recurrence rule such as 1:week:3 back in words.
func recurLabel(rule string) string {
	parts := strings.Split(rule, ":")
	if len(parts) < 2 {
		return rule
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return rule
	}
	out := "every " + parts[1]
	if n > 1 {
		out = fmt.Sprintf("every %d %ss", n, parts[1])
	}
	if len(parts) > 2 {
		// The rule counts Monday as 1 and Sunday as 7.
		if d, err := strconv.Atoi(parts[2]); err == nil && d >= 1 && d <= 7 {
			out += " on " + time.Weekday(d % 7).String()[:3]
		}
	}
	return out
}

// lateness says how far past its deadline a todo is.
func (m *Model) lateness(t api.Todo) string {
	if t.Completed() || t.DueAt == nil || !t.DueAt.Before(m.now) {
		return ""
	}
	d := m.now.Sub(*t.DueAt)
	switch {
	case d < time.Hour:
		return plural(max(int(d.Minutes()), 1), "minute") + " late"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " late"
	}
	return plural(int(d.Hours()/24), "day") + " late"
}

// assigneeChip is who a todo is assigned to, led by an at sign. A username
// already starts with one, so it stands in for the marker rather than
// following it.
func (m *Model) assigneeChip(listID, userID string) string {
	name := m.memberLabel(listID, userID)
	if strings.HasPrefix(name, "@") {
		return styAccent.Render("@") + styMuted.Render(name[1:])
	}
	return styAccent.Render("@ ") + styMuted.Render(name)
}

func (v *todosView) todoCard(t api.Todo, steps []api.ChecklistItem, haveSteps bool, w, h int) string {
	m := v.m
	if t.ID == "" {
		msg := "nothing selected"
		if m.inDetail() {
			msg = "loading…"
		}
		return lipgloss.Place(max(w, 1), max(h, 1), lipgloss.Center, lipgloss.Center, styFaint.Render(msg))
	}
	tw := max(w-4, 8)
	var b []string
	add := func(s ...string) { b = append(b, s...) }

	// Title.
	mark, markSty := v.todoMark(t)
	titleSty := styBold
	if t.Completed() {
		titleSty = styFaint.Strikethrough(true)
	}
	for i, l := range wrapText(t.Title, tw-2) {
		lead := "  "
		if i == 0 {
			lead = markSty.Render(mark) + " "
		}
		add(" " + lead + titleSty.Render(l))
	}
	add("")

	// Who and where.
	chips := []string{lipgloss.NewStyle().Foreground(tagColor(t.ListID)).Render("● ") + styMuted.Render(m.listName(t.ListID))}
	if t.AssigneeID != nil {
		chips = append(chips, m.assigneeChip(t.ListID, *t.AssigneeID))
	} else {
		chips = append(chips, styFaint.Render("anyone"))
	}
	if t.RecurrenceRule != nil && *t.RecurrenceRule != "" {
		chips = append(chips, stySoon.Render("↻ ")+styMuted.Render(recurLabel(*t.RecurrenceRule)))
	}
	if l, ok := m.listByID(t.ListID); ok && l.ReadOnly {
		chips = append(chips, styWarn.Render("⊘ list is read-only"))
	}
	add(" " + truncate(strings.Join(chips, styFaint.Render("  ·  ")), tw+2))

	// When.
	type row struct {
		k, v string
		tone lipgloss.Style
	}
	var rows []row
	if t.ScheduledDay != nil {
		day, _ := m.todoDay(t)
		rows = append(rows, row{"planned", scheduledLabel(t, m), m.whenTone(t, day)})
	}
	if t.DoAt != nil && t.HasTime {
		rows = append(rows, row{"at", formatStamp(t.DoAt, m.loc), styMuted})
	}
	if t.DueAt != nil {
		due := formatStamp(t.DueAt, m.loc)
		tone := styMuted
		if late := m.lateness(t); late != "" {
			due += "  · " + late
			tone = styOver
		}
		rows = append(rows, row{"due", due, tone})
	}
	if t.RemindAt != nil {
		remind := formatStamp(t.RemindAt, m.loc)
		if t.RemindedAt != nil {
			remind += " (sent " + relative(t.RemindedAt, m.now) + ")"
		}
		rows = append(rows, row{"remind", remind, styMuted})
	}
	if t.CompletedAt != nil {
		rows = append(rows, row{"done", formatStamp(t.CompletedAt, m.loc), styOK})
	}
	if len(rows) > 0 {
		add("", " "+label("when"))
		for _, r := range rows {
			add(" " + pad(styFaint.Render(r.k), 9) + r.tone.Render(truncate(r.v, max(tw-8, 4))))
		}
	} else if !t.Completed() {
		add("", " "+styFaint.Render("no date · t today · T tomorrow · w next week"))
	}

	if t.Notes != nil && strings.TrimSpace(*t.Notes) != "" {
		add("", " "+label("notes"))
		for _, l := range wrapText(*t.Notes, tw) {
			add(" " + lipgloss.NewStyle().Foreground(colFg).Render(l))
		}
	}
	if typed := asTyped(t); typed != "" {
		add("", " "+label("as typed"))
		for _, l := range wrapText(typed, tw) {
			add(" " + styFaint.Render(l))
		}
	}

	if t.ID != "" && !m.inDetail() {
		// Two rows below this are the blank and "updated" lines.
		add(v.previewChecklist(t, w, h-len(b)-2)...)
	}

	if haveSteps {
		head := " " + label("checklist")
		if len(steps) > 0 {
			done := 0
			for _, s := range steps {
				if s.Completed() {
					done++
				}
			}
			head += "  " + meter(done, len(steps), 10) + styMuted.Render(fmt.Sprintf(" %d/%d", done, len(steps)))
		}
		add("", head)
		if len(steps) == 0 {
			add(" " + styFaint.Render("no steps yet · c adds one"))
		} else {
			add(v.checklist.render(w, func(i int, sel bool) string {
				return v.stepRow(steps[i], sel && m.focus == focusChecklist, w-1)
			}))
		}
	}

	add("", " "+styFaint.Render("updated "+relative(&t.UpdatedAt, m.now)))
	return strings.Join(b, "\n")
}

// previewChecklist is the checklist of the todo under the cursor, read
// only, in the rows it is given. It says so while the steps are on their
// way, so a todo with steps never reads as one without.
func (v *todosView) previewChecklist(t api.Todo, w, rows int) []string {
	k, ok := v.known[t.ID]
	head := " " + label("checklist")
	switch {
	case !ok:
		return []string{"", head + "  " + styFaint.Render("loading…")}
	case k.failed:
		return []string{"", head + "  " + styFaint.Render("could not be read")}
	case len(k.items) == 0:
		return []string{"", head + "  " + styFaint.Render("no steps")}
	}
	done := 0
	for _, s := range k.items {
		if s.Completed() {
			done++
		}
	}
	head += "  " + meter(done, len(k.items), 10) + styMuted.Render(fmt.Sprintf(" %d/%d", done, len(k.items)))
	out := []string{"", head}
	// The head and the blank above it come out of the rows.
	room := rows - 2
	show := len(k.items)
	if show > room {
		show = max(room-1, 0)
	}
	for _, s := range k.items[:show] {
		out = append(out, v.stepRow(s, false, w-1))
	}
	if more := len(k.items) - show; more > 0 && room > 0 {
		out = append(out, " "+styFaint.Render(fmt.Sprintf("+%d more · enter opens the todo", more)))
	}
	return out
}

func (v *todosView) stepRow(s api.ChecklistItem, sel bool, w int) string {
	base := lipgloss.NewStyle()
	bar := " "
	if sel {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	mark := base.Foreground(colFaint).Render("□")
	text := base.Foreground(colFg)
	if s.Completed() {
		mark = base.Foreground(colGreen).Render("☑")
		text = base.Foreground(colFaint).Strikethrough(true)
	}
	title := truncate(s.Title, max(w-4, 4))
	row := bar + mark + base.Render(" ") + text.Render(title)
	return row + base.Render(strings.Repeat(" ", max(w-visibleWidth(row), 0)))
}

// scheduledLabel is the scheduled day as a person would say it, with the
// exact date alongside for when it matters.
func scheduledLabel(t api.Todo, m *Model) string {
	if t.ScheduledDay == nil {
		return ""
	}
	parsed, err := time.ParseInLocation("2006-01-02", *t.ScheduledDay, m.loc)
	if err != nil {
		return *t.ScheduledDay
	}
	return formatDay(&parsed, m.loc, m.now) + "  (" + parsed.Format("Mon 2 Jan 2006") + ")"
}

// -- footer -----------------------------------------------------------------

func (v *todosView) footer() []helpEntry {
	m := v.m
	switch {
	case m == nil:
		return nil
	case m.focus == focusChecklist:
		// The detail and its checklist share the screen, so the footer
		// follows whichever of them has the keyboard.
		return []helpEntry{
			{"j/k", "step"},
			{"space", "tick"},
			{"c", "add step"},
			{"e", "rename"},
			{"d", "delete"},
			{"tab", "back to todo"},
			{"esc", "back to list"},
		}
	case m.focus == focusDetail:
		return v.withTake([]helpEntry{
			{"tab", "checklist"},
			{"space", "complete"},
			{"e", "edit"},
			{"c", "add step"},
			{"A", "assign"},
			{"d", "delete"},
			{"y", "copy id"},
			{"esc", "back"},
		}, 5)
	case m.focus == focusSidebar:
		return []helpEntry{
			{"j/k", "move"},
			{"enter", "show"},
			{"esc", "back to todos"},
		}
	case v.repair != nil:
		return []helpEntry{
			{"enter", "repair"},
			{"h", "sidebar"},
			{"S", "saved views"},
			{"esc", "leave it"},
		}
	case len(v.marks) > 0:
		return []helpEntry{
			{"v", "mark more"},
			{"space", "complete marked"},
			{"m", "move marked"},
			{"d", "delete marked"},
			{"esc", "clear marks"},
		}
	case v.filter.board:
		return []helpEntry{
			{"h/l", "day"},
			{"j/k", "todo"},
			{"< >", "move a day"},
			{"space", "complete"},
			{"enter", "open"},
			{"n", "new"},
			{"b", "list view"},
		}
	}
	return v.withTake([]helpEntry{
		{"j/k", "move"},
		{"enter", "open"},
		{"space", "complete"},
		{"n", "new"},
		{"e", "edit"},
		{"t/T/w", "reschedule"},
		{"< >", "day"},
		{"b", "board"},
		{"/", "search"},
		{"z", "undo"},
		{"v", "mark"},
		{"f", "filter"},
		{"s", "save view"},
		{"d", "delete"},
	}, 3)
}

// withTake puts the key that takes a todo or hands it back into a footer
// at position at, when the todo under the cursor is one that can be taken:
// it is offered only in shared lists.
func (v *todosView) withTake(hints []helpEntry, at int) []helpEntry {
	m := v.m
	t := v.currentTodo(m)
	if _, ok := m.sharedList(t); !ok || t.Completed() {
		return hints
	}
	label := "take"
	if m.mine(*t) {
		label = "hand back"
	}
	return slices.Insert(hints, min(at, len(hints)), helpEntry{"i", label})
}
