package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/dates"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// Everything the Todos tab changes on the server starts here: the change is
// shown straight away where that is safe, and sent through the mutation
// queue so the changes to any one todo go out in the order they were made.
// The forms that create, edit or assign todos end here too.

// markLocal shows a completion or reopening straight away. The server's
// answer replaces it, and a failure reloads the list to undo it.
func (v *todosView) markLocal(id string, done bool) {
	for i := range v.all {
		if v.all[i].ID != id {
			continue
		}
		v.all[i].IsCompleted = done
		if done {
			now := v.m.now
			v.all[i].CompletedAt = &now
			if animDur > 0 {
				v.anims[id] = time.Now()
			}
		} else {
			v.all[i].CompletedAt = nil
		}
	}
	v.applyLive(v.selectedID())
}

// patchLocal shows an edit straight away, the way markLocal does.
func (v *todosView) patchLocal(id string, edit func(*api.Todo)) {
	keep := v.selectedID()
	for i := range v.all {
		if v.all[i].ID == id {
			edit(&v.all[i])
		}
	}
	v.dropHay()
	v.applyLive(keep)
}

func (v *todosView) toggle(m *Model) tea.Cmd {
	if len(v.marks) > 0 {
		return v.bulkComplete(m)
	}
	t := v.selected()
	if t == nil {
		return nil
	}
	id, wasDone := t.ID, t.Completed()
	// Show the result now; the server confirms it a moment later.
	v.markLocal(id, !wasDone)
	m.busy = true
	if wasDone {
		return tea.Batch(m.setBusy("reopening"), m.mutate(id, uncompleteTodo(m.ctx, m.client, id)))
	}
	return tea.Batch(m.setBusy("completing"), m.mutate(id, completeTodo(m.ctx, m.client, id)))
}

// shiftDay moves a todo a day earlier or later. Something with no day, or
// one that has already gone by, moves to today first: pushing a late todo
// forward should never land it on another day in the past.
func (v *todosView) shiftDay(m *Model, delta int) tea.Cmd {
	t := v.currentTodo(m)
	if t == nil {
		return nil
	}
	if t.Completed() {
		return m.warnf("that one is done; reopen it to move it")
	}
	today := m.now.In(m.loc)
	day, ok := m.todoDay(*t)
	to := today
	switch {
	case !ok && delta < 0:
		return m.warnf("it has no day to move back from")
	case !ok, dayKey(day) < dayKey(today) && delta > 0:
	default:
		to = day.AddDate(0, 0, delta)
	}
	id, iso := t.ID, to.Format("2006-01-02")
	v.patchLocal(id, func(t *api.Todo) { t.ScheduledDay = &iso })
	m.busy = true
	return tea.Batch(m.setBusy("moving"), m.mutate(id,
		rescheduleTodo(m.ctx, m.client, id, api.TodoPatch{ScheduledDay: api.Val(iso)},
			"moved to "+strings.ToLower(formatDay(&to, m.loc, m.now)))))
}

// reschedule moves the marked todos, or the one under the cursor, to a day
// counted from today in the account timezone. Only the day changes; a time
// of day or a deadline on the todo is left as it was.
func (v *todosView) reschedule(m *Model, days int, label string) tea.Cmd {
	day := m.now.In(m.loc).AddDate(0, 0, days).Format("2006-01-02")
	patch := api.TodoPatch{ScheduledDay: api.Val(day)}
	c, ctx := m.client, m.ctx
	if len(v.marks) > 0 {
		ids := v.markedIDs()
		m.busy = true
		return tea.Batch(m.setBusy("rescheduling"), m.mutateAll(ids, bulkTodos("rescheduled to "+label+":", ids,
			m.backend, local.Change{Patch: patch}, func(id string) error {
				_, _, err := c.UpdateTodo(ctx, id, patch)
				return err
			})))
	}
	t := v.currentTodo(m)
	if t == nil {
		return nil
	}
	m.busy = true
	return tea.Batch(m.setBusy("rescheduling"), m.mutate(t.ID, rescheduleTodo(ctx, c, t.ID, patch, "moved to "+label)))
}

// bulkComplete completes every marked todo, or reopens them when they are
// all done already.
func (v *todosView) bulkComplete(m *Model) tea.Cmd {
	var open []string
	for _, id := range v.markedIDs() {
		for _, t := range v.items {
			if t.ID == id && !t.Completed() {
				open = append(open, id)
			}
		}
	}
	c, ctx := m.client, m.ctx
	if len(open) == 0 {
		ids := v.markedIDs()
		m.busy = true
		return tea.Batch(m.setBusy("reopening"), m.mutateAll(ids, bulkTodos("reopened", ids,
			m.backend, local.Change{Patch: api.TodoPatch{IsCompleted: api.BoolVal(false)}}, func(id string) error {
				_, err := c.UncompleteTodo(ctx, id)
				return err
			})))
	}
	m.busy = true
	return tea.Batch(m.setBusy("completing"), m.mutateAll(open, bulkTodos("completed", open,
		m.backend, local.Change{Patch: api.TodoPatch{IsCompleted: api.BoolVal(true)}}, func(id string) error {
			_, _, err := c.CompleteTodo(ctx, id)
			return err
		})))
}

// bulkDelete asks once for every marked todo.
func (v *todosView) bulkDelete(m *Model) tea.Cmd {
	ids := v.markedIDs()
	c, ctx := m.client, m.ctx
	return m.ask("delete-todos",
		fmt.Sprintf("Delete %s?", plural(len(ids), "todo")),
		"They will be removed for every device. This cannot be undone with z.", false,
		func() tea.Cmd {
			return m.mutateAll(ids, bulkTodos("deleted", ids, m.backend, local.Change{Delete: true},
				func(id string) error { return c.DeleteTodo(ctx, id) }))
		})
}

// openMove asks which list the marked todos should move to.
func (v *todosView) openMove(m *Model) tea.Cmd {
	if len(v.marks) == 0 {
		return m.warnf("mark todos with v first, then press m to move them")
	}
	f := newForm(formMoveTodos, "Move "+plural(len(v.marks), "todo"), m.width)
	f.selectValue("list", "to list", v.listOptions(m, ""), "")
	return m.openForm(f)
}

// bulkMove moves every marked todo to the chosen list.
func (v *todosView) bulkMove(m *Model, name string) tea.Cmd {
	listID := m.listIDByName(name)
	if listID == "" {
		return m.warnf("pick a list to move them to")
	}
	ids := v.markedIDs()
	c, ctx := m.client, m.ctx
	patch := api.TodoPatch{ListID: api.Val(listID)}
	m.busy = true
	return tea.Batch(m.setBusy("moving"), m.mutateAll(ids, bulkTodos("moved", ids,
		m.backend, local.Change{Patch: patch}, func(id string) error {
			_, _, err := c.UpdateTodo(ctx, id, patch)
			return err
		})))
}

func (v *todosView) askDelete(m *Model) tea.Cmd {
	t := v.selected()
	if t == nil {
		return nil
	}
	if len(v.marks) > 0 {
		return v.bulkDelete(m)
	}
	todo := *t
	return m.ask("delete-todo",
		"Delete this todo?",
		fmt.Sprintf("%q will be removed. Syncing clients see a tombstone, so other devices drop it too. z brings it back as a new todo.", todo.Title),
		false,
		func() tea.Cmd {
			return m.mutate(todo.ID, deleteTodo(m.ctx, m.client, todo))
		})
}

// onBulk reports the outcome of an action on several todos.
func (m *Model) onBulk(msg bulkMsg, overtaken bool) tea.Cmd {
	m.todos.marks = map[string]bool{}
	var cmds []tea.Cmd
	if overtaken {
		// A later change to one of these todos is on its way, as with a
		// single change overtaken. Reloading now could land after its
		// answer and paint this one back over it, so that answer reloads
		// the whole list instead.
		m.todos.owed = true
	} else {
		m.done()
		cmds = append(cmds, m.todos.refresh())
	}
	switch {
	case msg.failed == 0:
		cmds = append(cmds, m.okf("%s %s", msg.verb, plural(msg.done, "todo")))
	case msg.done == 0:
		cmds = append(cmds, m.failf(msg.err, "no todos were "+msg.verb))
	default:
		cmds = append(cmds, m.warnf("%s %d of %d: %s", msg.verb, msg.done, msg.done+msg.failed, errText(msg.err)))
	}
	return tea.Batch(cmds...)
}

// -- forms that change todos ------------------------------------------------

// listOptions are the lists a todo can be put in. A read-only list takes no
// new todos, so it is left out, except as the list the todo being edited is
// already in, which the form has to be able to show.
func (v *todosView) listOptions(m *Model, current string) []string {
	opts := make([]string, 0, len(m.lists))
	for _, l := range m.lists {
		if l.ReadOnly && l.ID != current {
			continue
		}
		opts = append(opts, l.Name)
	}
	if len(opts) == 0 {
		opts = []string{"(personal list)"}
	}
	return opts
}

func (v *todosView) openCreate(m *Model) tea.Cmd {
	f := newForm(formNewTodo, "New todo", m.width)
	f.area("title", "title", "", "what needs doing? e.g. Pay the cable bill Friday at 5pm", 2).titles()
	f.preview(previewHint())
	// A new todo goes to the list on screen, which is where it is expected
	// to turn up.
	f.selectValue("list", "list", v.listOptions(m, ""), m.listName(v.filter.listID))
	m.previewFor = ""
	f.prose("notes", "notes", "", "optional", 3)
	f.optionalField("remindAt", "remind", "", "leave empty, or a time like \"2pm tomorrow\"")
	return m.openForm(f)
}

func (v *todosView) openQuickCapture(m *Model) tea.Cmd {
	f := newForm(formQuickAdd, "Quick capture", m.width)
	f.note("hint", "One line per todo. The server parses dates in the text.")
	f.prose("title", "todo", "", "one per line", 6).titles()
	return m.openForm(f)
}

func (v *todosView) openEdit(m *Model) tea.Cmd {
	t := v.selected()
	if t == nil {
		if m.focus == focusDetail && v.detail.ID != "" {
			return v.openEditFor(m, v.detail)
		}
		return nil
	}
	return v.openEditFor(m, *t)
}

func (v *todosView) openEditFor(m *Model, t api.Todo) tea.Cmd {
	f := newForm(formEditTodo, "Edit todo", m.width)
	f.area("title", "title", t.Title, "", 3).titles()
	f.selectValue("list", "list", v.listOptions(m, t.ListID), m.listName(t.ListID))
	f.prose("notes", "notes", deref(t.Notes), "", 4)
	f.optionalField("scheduledDay", "do day", deref(t.ScheduledDay), "YYYY-MM-DD, or words like \"friday\"; empty clears")
	f.optionalField("doAt", "do at", isoLocal(t.DoAt, m.loc), "YYYY-MM-DDTHH:MM, or words like \"5pm friday\"")
	f.optionalField("dueAt", "due", isoLocal(t.DueAt, m.loc), "YYYY-MM-DDTHH:MM, or words like \"next monday 9am\"")
	f.optionalField("remindAt", "remind", isoLocal(t.RemindAt, m.loc), "YYYY-MM-DDTHH:MM, or words like \"tomorrow 8am\"")
	f.optionalField("recurrenceRule", "repeat", deref(t.RecurrenceRule), "1:day, 1:week, 1:week:3 (every Wed)")
	f.toggle("isCompleted", "done", t.IsCompleted)
	f.note("note", "Empty date fields clear them. Setting a due date also moves the reminder to it.")
	// Remember what the todo looks like now, so saving sends only the
	// fields the user actually touched. Without this, every empty
	// optional field would be sent as an explicit null and quietly wipe
	// dates the form simply did not display.
	f.orig = map[string]string{
		"title":          t.Title,
		"list":           m.listName(t.ListID),
		"notes":          deref(t.Notes),
		"scheduledDay":   deref(t.ScheduledDay),
		"doAt":           isoLocal(t.DoAt, m.loc),
		"dueAt":          isoLocal(t.DueAt, m.loc),
		"remindAt":       isoLocal(t.RemindAt, m.loc),
		"recurrenceRule": deref(t.RecurrenceRule),
		"isCompleted":    boolWord(t.IsCompleted),
	}
	return m.openForm(f)
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (v *todosView) openNewStep(m *Model) tea.Cmd {
	t := v.currentTodo(m)
	if t == nil {
		return m.warnf("select a todo first")
	}
	f := newForm(formNewStep, "Add checklist step", m.width)
	f.field("title", "step", "", "one step at a time").titles()
	return m.openForm(f)
}

func (v *todosView) openRenameStep(m *Model, item api.ChecklistItem) tea.Cmd {
	f := newForm(formRenameStep, "Rename step", m.width)
	f.field("title", "step", item.Title, "").titles()
	return m.openForm(f)
}

// openAssign asks who should have the todo. Until the list's members have
// been read the picker could offer nobody but Anyone, so they are read
// first when nothing has filled the cache yet.
func (v *todosView) openAssign(m *Model) tea.Cmd {
	t := v.currentTodo(m)
	if t == nil {
		return nil
	}
	if _, ok := m.acct.memberCache[t.ListID]; ok {
		return v.showAssign(m, *t, "")
	}
	v.openSeq++
	m.busy = true
	return tea.Batch(m.setBusy(""), fetchAssignees(m.ctx, m.client, *t, v.openSeq))
}

// assigneesMsg is the members of a todo's list, read to fill its assignee
// picker.
type assigneesMsg struct {
	seq     int
	todoID  string
	listID  string
	members []api.Member
	err     error
}

func fetchAssignees(ctx context.Context, c *api.Client, t api.Todo, seq int) tea.Cmd {
	return func() tea.Msg {
		ms, err := c.ListMembers(ctx, t.ListID)
		return assigneesMsg{seq: seq, todoID: t.ID, listID: t.ListID, members: ms, err: err}
	}
}

// onAssignees opens the assignee picker once the members are in, unless
// something else was opened or picked while they were read.
func (v *todosView) onAssignees(msg assigneesMsg) tea.Cmd {
	m := v.m
	if msg.seq != v.openSeq {
		return nil
	}
	m.done()
	t := v.currentTodo(m)
	if t == nil || t.ID != msg.todoID || m.layer != layerNone {
		return nil
	}
	if msg.err != nil {
		// Left out of the cache, so the next try reads them again.
		return v.showAssign(m, *t, "Only Anyone is offered, because the members of this list could not be read: "+errText(msg.err))
	}
	m.acct.memberCache[msg.listID] = msg.members
	return v.showAssign(m, *t, "")
}

// showAssign opens the assignee picker with the members cached for the
// todo's list. warning, when set, replaces the usual hint.
func (v *todosView) showAssign(m *Model, t api.Todo, warning string) tea.Cmd {
	l, _ := m.listByID(t.ListID)
	opts := []string{"Anyone"}
	for _, mem := range m.acct.membersFor(t.ListID) {
		label := mem.Name()
		if mem.Role == "owner" {
			label += " (owner)"
		}
		opts = append(opts, label)
	}
	f := newForm(formAssignTodo, "Assign in "+l.Name, m.width)
	if warning == "" {
		warning = "The assignee must already be a member of the list."
	}
	f.note("hint", warning)
	f.selectValue("assignee", "assign to", opts, "")
	return m.openForm(f)
}

// onParse shows the server's parse preview for a line of text.
func (v *todosView) onParse(msg parseMsg) tea.Cmd {
	if msg.err != nil {
		v.parsePreview = nil
		return v.m.warnf("parse failed: %s", errText(msg.err))
	}
	v.parseTitle = msg.input
	v.parsePreview = &msg.preview
	return nil
}

func (v *todosView) previewParse(m *Model) tea.Cmd {
	t := v.selected()
	if t == nil {
		return m.warnf("select a todo to preview its dates")
	}
	title := t.Title
	if m.focus == focusDetail && v.detail.ID != "" {
		title = v.detail.Title
	}
	return tea.Batch(m.setBusy("parsing"), parseTitle(m.ctx, m.client, title))
}

func isoLocal(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("2006-01-02T15:04")
}

// -- submitting them --------------------------------------------------------

// createTodo sends the form as one create call. Newlines in the title are
// split server-side, so quick capture can create many at once.
func (m *Model) createTodo(v map[string]string) tea.Cmd {
	title := strings.TrimSpace(v["title"])
	if title == "" {
		return m.warnf("a todo needs a title")
	}
	n := api.NewTodo{
		Title:  title,
		Smart:  m.captureSmart,
		Notes:  optString(v["notes"]),
		ListID: m.listIDByName(v["list"]),
	}
	if title != m.previewFor {
		n.Smart = nil
	}
	if r := strings.TrimSpace(v["remindAt"]); r != "" {
		norm, ok := dates.Normalise(r)
		if !ok {
			// Words such as "2pm tomorrow" go to the server's parser first;
			// the service takes a reminder only as a time.
			m.busy = true
			return tea.Batch(m.setBusy("reading dates"), createTodoWithRemindWords(m.ctx, m.client, m.loc, n, r))
		}
		r, _ = dates.Wire(dates.Remind, norm, m.loc, time.Now())
		n.RemindAt = &r
	}
	m.busy = true
	return tea.Batch(m.setBusy("creating"), createTodo(m.ctx, m.client, n))
}

// createTodoWithRemindWords reads the reminder typed as words through the
// server's parser, then creates the todo. If the words hold no date nothing
// is created and the message says so.
func createTodoWithRemindWords(ctx context.Context, c *api.Client, loc *time.Location, n api.NewTodo, text string) tea.Cmd {
	return func() tea.Msg {
		pv, err := c.Parse(ctx, text)
		if err != nil {
			return todoMsg{err: err, action: actionCreated}
		}
		if problem := dates.ParseProblem(dates.Remind, text, pv); problem != nil {
			return todoMsg{err: problem, action: actionCreated}
		}
		val, _, ok := dates.FromParse(dates.Remind, pv, loc)
		if !ok {
			return todoMsg{err: dates.Unreadable(dates.Remind, text), action: actionCreated}
		}
		n.RemindAt = &val
		return createTodo(ctx, c, n)()
	}
}

// editTodo sends only the fields that changed, so clearing one field does
// not wipe the others.
func (m *Model) editTodo(v, orig map[string]string) tea.Cmd {
	t := m.todos.currentTodo(m)
	if t == nil {
		return m.warnf("select a todo first")
	}
	p := api.TodoPatch{}

	title := strings.TrimSpace(v["title"])
	if title == "" {
		return m.warnf("a todo needs a title")
	}
	if title != t.Title {
		p.Title = api.Val(title)
	}
	if strings.TrimSpace(v["notes"]) != strings.TrimSpace(deref(t.Notes)) {
		setString(&p.Notes, v["notes"])
	}
	if id := m.listIDByName(v["list"]); id != "" && id != t.ListID {
		p.ListID = api.Val(id)
	}
	// Date fields are only sent when the value differs from what the form
	// was opened with; that keeps an untouched empty field from clearing
	// a date the user never saw. Words such as "friday 5pm" cannot be
	// turned into a date here, so those go to the server's parser first.
	words := map[string]string{}
	for _, key := range []string{dates.Day, dates.DoAt, dates.DueAt, dates.Remind} {
		if text := setDateIfChanged(&p, key, v[key], orig[key], m.loc); text != "" {
			words[key] = text
		}
	}
	if rule := strings.TrimSpace(v["recurrenceRule"]); rule != strings.TrimSpace(orig["recurrenceRule"]) {
		if rule == "" {
			p.RecurrenceRule = api.Null
		} else {
			p.RecurrenceRule = api.Val(rule)
		}
	}
	done := v["isCompleted"] == "yes"
	if done != t.IsCompleted {
		p.IsCompleted = api.BoolVal(done)
	}
	if p.Empty() && len(words) == 0 {
		return m.okf("nothing changed")
	}
	m.busy = true
	if len(words) > 0 {
		return tea.Batch(m.setBusy("reading dates"), m.mutate(t.ID, patchTodoWithWords(m.ctx, m.client, m.loc, t.ID, p, words)))
	}
	return tea.Batch(m.setBusy("saving"), m.mutate(t.ID, patchTodo(m.ctx, m.client, t.ID, p)))
}

// patchTodoWithWords resolves date fields typed as words through the
// server's parser, then saves everything in one patch. If any of them
// cannot be read the todo is left untouched and the message says which.
func patchTodoWithWords(ctx context.Context, c *api.Client, loc *time.Location, id string, p api.TodoPatch, words map[string]string) tea.Cmd {
	return func() tea.Msg {
		for key, text := range words {
			pv, err := c.Parse(ctx, text)
			if err != nil {
				return todoMsg{err: err}
			}
			if problem := dates.ParseProblem(key, text, pv); problem != nil {
				return todoMsg{err: problem}
			}
			val, dateOnly, ok := dates.FromParse(key, pv, loc)
			if !ok {
				return todoMsg{err: dates.Unreadable(key, text)}
			}
			dates.Assign(&p, key, val, dateOnly)
		}
		t, next, err := c.UpdateTodo(ctx, id, p)
		return todoMsg{todo: t, next: next, err: err, action: actionUpdated, title: t.Title}
	}
}

func (m *Model) assignTodo(v map[string]string) tea.Cmd {
	t := m.todos.currentTodo(m)
	if t == nil {
		return nil
	}
	who := v["assignee"]
	if who == "Anyone" {
		m.busy = true
		return tea.Batch(m.setBusy("assigning"), m.mutate(t.ID, patchTodo(m.ctx, m.client, t.ID,
			api.TodoPatch{AssigneeID: api.Null})))
	}
	name := strings.TrimSuffix(strings.TrimSuffix(who, " (owner)"), " (you)")
	for _, mem := range m.acct.membersFor(t.ListID) {
		if mem.Name() == name {
			id := mem.UserID
			m.busy = true
			return tea.Batch(m.setBusy("assigning"), m.mutate(t.ID, patchTodo(m.ctx, m.client, t.ID,
				api.TodoPatch{AssigneeID: api.Val(id)})))
		}
	}
	return m.warnf("member list not loaded: open the list in the Lists tab and press m first")
}

// sharedList is the list a todo is in when taking it is on offer: a
// shared list that is not read-only. A personal list has nobody to take a
// todo from.
func (m *Model) sharedList(t *api.Todo) (api.List, bool) {
	if t == nil {
		return api.List{}, false
	}
	l, ok := m.listByID(t.ListID)
	return l, ok && !l.IsPersonal && !l.ReadOnly
}

// mine reports whether a todo is assigned to the person signed in.
func (m *Model) mine(t api.Todo) bool {
	return t.AssigneeID != nil && m.user.ID != "" && *t.AssigneeID == m.user.ID
}

// takeOrHandBack takes the todo for you ("I'll do it"), or hands yours back
// to Anyone. Nothing is shown ahead of the answer, as completing does: the
// service decides who has a todo when two people try at once, and the row
// changes to whatever it says.
func (v *todosView) takeOrHandBack(m *Model) tea.Cmd {
	t := v.currentTodo(m)
	if t == nil {
		return nil
	}
	l, found := m.listByID(t.ListID)
	switch {
	case !found:
		return nil
	case l.IsPersonal:
		return m.warnf("only todos in shared lists can be taken; this one is in your personal list")
	case l.ReadOnly:
		return m.warnf("%s is read-only, so its todos cannot be taken or handed back", l.Name)
	case t.Completed():
		return m.warnf("that one is done; reopen it to take it")
	case m.backend != nil && !m.backend.Status().SignedIn:
		return m.warnf("taking a todo needs an Evado account, which decides who has it; sign in from Account, Connection")
	}
	release := m.mine(*t)
	m.busy = true
	verb := map[bool]string{false: "taking", true: "handing back"}[release]
	cmds := []tea.Cmd{m.setBusy(verb), m.mutate(t.ID, claimTodo(m.ctx, m.client, *t, release))}
	if _, ok := m.acct.memberCache[t.ListID]; !ok {
		// Whoever the answer says has it is named by the list's members.
		cmds = append(cmds, fetchMemberCache(m.ctx, m.client, t.ListID))
	}
	return tea.Batch(cmds...)
}

// claimTodo sends a claim or a release. Losing to someone else is not a
// failure to reload from: the answer carries the todo as it is now, which
// updates the row, and who has it, which the receipt names.
func claimTodo(ctx context.Context, c *api.Client, t api.Todo, release bool) tea.Cmd {
	return func() tea.Msg {
		call := c.ClaimTodo
		if release {
			call = c.ReleaseTodo
		}
		got, err := call(ctx, t.ID)
		var conflict *api.AssignmentConflict
		switch {
		case errors.As(err, &conflict) && conflict.Todo != nil:
			return todoMsg{todo: *conflict.Todo, action: actionUpdated, title: conflict.Todo.Title, note: lostTo(conflict), warn: true}
		case errors.As(err, &conflict) && conflict.Err.Code == api.CodeTodoCompleted:
			return todoMsg{err: errors.New("it is done now, so it cannot be taken or handed back; reopen it first")}
		case err != nil:
			return todoMsg{err: err}
		}
		note := map[bool]string{false: "yours now", true: "back to Anyone"}[release]
		if sameAssignee(t.AssigneeID, got.AssigneeID) {
			// A retry the service had already applied: nothing changed.
			note = map[bool]string{false: "already yours", true: "already Anyone's"}[release]
		}
		return todoMsg{todo: got, action: actionUpdated, title: got.Title, note: note}
	}
}

// lostTo says who has a todo that could not be taken or handed back.
func lostTo(c *api.AssignmentConflict) string {
	who := c.Holder()
	switch {
	case c.Err.Code == api.CodeNotAssignedToYou && who != "":
		return who + " has it now, so nothing changed"
	case c.Err.Code == api.CodeNotAssignedToYou:
		return "someone who left the list has it now, so nothing changed"
	case who != "":
		return who + " has it already"
	}
	return "someone else has it"
}

func sameAssignee(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (m *Model) addStep(v map[string]string) tea.Cmd {
	title := strings.TrimSpace(v["title"])
	if title == "" {
		return m.warnf("a step needs a title")
	}
	t := m.todos.currentTodo(m)
	if t == nil {
		return m.warnf("select a todo first")
	}
	m.busy = true
	return tea.Batch(m.setBusy("adding step"), addChecklistItem(m.ctx, m.client, t.ID, title))
}

func (m *Model) renameStep(v map[string]string) tea.Cmd {
	title := strings.TrimSpace(v["title"])
	if title == "" {
		return m.warnf("a step needs a title")
	}
	i := m.todos.checklist.selected()
	if i < 0 || i >= len(m.todos.steps) {
		return m.warnf("select a step first")
	}
	item := m.todos.steps[i]
	m.busy = true
	return tea.Batch(m.setBusy("renaming step"), renameChecklistItem(m.ctx, m.client, item, title))
}

// listIDByName maps a select option back to a list id.
func (m *Model) listIDByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "(personal list)" {
		return m.personalList()
	}
	for _, l := range m.lists {
		if l.Name == name {
			return l.ID
		}
	}
	return ""
}

// setString stores a text field: empty clears it with an explicit null.
func setString(dst *json.RawMessage, v string) {
	if strings.TrimSpace(v) == "" {
		*dst = api.Null
		return
	}
	*dst = api.Val(v)
}

func optString(s string) *string {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// setDateIfChanged normalises a date field and writes it for the service
// on loc's clock, and does nothing when the value is unchanged. Empty clears
// the field with an explicit null. Text that is not a date at all is not
// stored; it is returned so the caller can have it read as words.
func setDateIfChanged(p *api.TodoPatch, field, v, orig string, loc *time.Location) string {
	v = strings.TrimSpace(v)
	if v == strings.TrimSpace(orig) {
		return ""
	}
	if v == "" {
		switch field {
		case dates.Day:
			p.ScheduledDay = api.Null
		case dates.DoAt:
			p.DoAt = api.Null
		case dates.DueAt:
			p.DueAt = api.Null
		case dates.Remind:
			p.RemindAt = api.Null
		}
		return ""
	}
	norm, ok := dates.Normalise(v)
	if !ok {
		return v
	}
	val, dateOnly := dates.Wire(field, norm, loc, time.Now())
	dates.Assign(p, field, val, dateOnly)
	return ""
}

// -- outcomes ---------------------------------------------------------------

// refreshAfter updates the list after a mutation. When the whole account
// is on screen the server's answer is merged in directly, which costs no
// request at all; creating several todos at once, any narrowed view, or a
// list still owed a bulk action's outcome asks the server instead.
func (v *todosView) refreshAfter(m *Model, msg todoMsg) tea.Cmd {
	if !v.owed && v.applyLocal(msg) {
		if m.layer != layerSearch {
			v.live = ""
		}
		return v.settleReveal(m)
	}
	if msg.action == actionDeleted && msg.deleted != nil {
		// Taken off at once, so the cursor moves to the next todo instead
		// of looking for the deleted one in the reload and not finding it.
		v.drop(msg.deleted.ID)
	}
	return v.refresh()
}

// reveal is a mutation outcome waiting for the refreshed list, so the
// cursor can be placed on the todo that actually changed.
type reveal struct {
	id     string
	action todoAction
	title  string
	count  int
	next   *api.Todo
	// scope is the view that was on screen when the action was taken.
	scope string
	// widened records that the view was already opened up once, so the
	// retry cannot loop.
	widened bool
	// undo is set when the action was taking an earlier one back.
	undo bool
	// note, when set, is what the receipt says instead of "saved".
	note string
	// warn shows the receipt as a warning: the todo is shown as it is, but
	// the action did not do what was asked.
	warn bool
}

// stage records what an action did, to be shown once the list is fresh.
func (v *todosView) stage(msg todoMsg) {
	if msg.action == actionNone || msg.action == actionDeleted {
		return
	}
	v.pendingReveal = &reveal{
		id: msg.todo.ID, action: msg.action, title: msg.title,
		count: msg.count, next: msg.next, scope: v.filter.scopeLabel(),
		undo: msg.undo != 0, note: msg.note, warn: msg.warn,
	}
}

// settleReveal runs after a list reload: it makes sure the todo the user
// just touched is on screen, and tells them what happened. If the view is
// too narrow to show it, the view is opened up and the message says so
// rather than leaving the user to wonder where it went.
func (v *todosView) settleReveal(m *Model) tea.Cmd {
	r := v.pendingReveal
	if r == nil {
		return nil
	}
	v.pendingReveal = nil

	// Hidden by a narrowed view: open up and try once more.
	if !v.shown(r.id) && v.filter.mode == viewStatus && !r.widened {
		r.widened = true
		v.pendingReveal = r
		return tea.Batch(
			v.setMode(m, viewEverything, api.StatusToday),
			m.setBusy(""),
		)
	}
	v.reveal(r.id)

	var msg string
	switch r.action {
	case actionCreated:
		if r.undo {
			msg = fmt.Sprintf("restored %q as a new todo; its steps are not restored", r.title)
		} else if r.count > 1 {
			msg = fmt.Sprintf("added %d todos", r.count)
		} else {
			msg = fmt.Sprintf("added %q", r.title)
		}
	case actionUpdated:
		msg = fmt.Sprintf("saved %q", r.title)
		if r.note != "" {
			msg = fmt.Sprintf("%q: %s", r.title, r.note)
		}
	case actionCompleted:
		if r.next != nil {
			msg = "done, the next one is " + m.dayLabel(r.next.ScheduledDay)
		} else {
			msg = fmt.Sprintf("done: %q", r.title)
		}
	case actionReopened:
		msg = fmt.Sprintf("reopened %q", r.title)
	default:
		return nil
	}
	if r.undo && r.action != actionCreated {
		msg = "undone: " + msg
	}
	if r.widened {
		msg += fmt.Sprintf(". The %s view would have hidden it, so it now shows everything", strings.ToLower(r.scope))
	}
	if r.warn {
		return toastCmd(toastWarn, "%s", msg)
	}
	return toastCmd(toastOK, "%s", msg)
}
