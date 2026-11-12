package tui

import (
	"math"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// todosView is the todo list plus the detail pane. This file holds its
// state and the todos loaded into it. The rest is split by what it does,
// so each part owns what it changes: todos_filter.go describes what is
// shown, todos_sections.go lays it out, todos_nav.go moves around it and
// chooses the filter, todos_actions.go changes todos, todos_prefs.go
// remembers the view between runs, todos_views.go saves named filters and
// opens them again, and todos_view.go draws it.
type todosView struct {
	m *Model

	rows table

	items  []api.Todo
	detail api.Todo
	// pane is the todo the detail pane was last opened on. Answers for the
	// pane carry the one they were asked for, and any other is dropped.
	pane paneReq

	// checklist is the step editor inside the detail pane.
	checklist table
	steps     []api.ChecklistItem
	// known holds the steps of todos other than the open one, so the
	// preview and the list rows can show them. See peek.go.
	known map[string]knownSteps
	// peekKey is the cursor todo the preview last settled on, and peekSeq
	// numbers the settlings so a pause that was overtaken does nothing.
	peekKey string
	peekSeq int

	// lines is the flattened list: section headings interleaved with todos,
	// in display order. The cursor only ever rests on a todo.
	lines []todoRow

	// filter is what is being shown, and how it is laid out.
	filter todoFilter
	// opened is the name of the saved view last opened. The list counts as
	// showing it only while filter is still what it saved. repair is a view
	// that was asked for and not opened, because part of it is missing, and
	// openSeq numbers the reads that open one, so a slow answer for an
	// earlier choice is dropped.
	opened  string
	repair  *repairState
	openSeq int

	// next is the cursor of the page after the last todo in all, empty when
	// the list ends there. The view says there is more rather than imply it
	// shows everything, and L reads the page next points at. loadedFor is
	// the request that all and next answer, without page size or cursor:
	// next means nothing under any other filters.
	next      string
	loadedFor api.TodoFilter

	// parse preview shown before creating a todo.
	parseTitle   string
	parsePreview *api.ParsePreview

	// seq numbers each load. A response is only applied when it answers the
	// latest one, so a slow reply cannot overwrite a newer view.
	seq int
	// all is what the API returned; items is all narrowed by live, the text
	// typed into the search box so far.
	all  []api.Todo
	live string
	// hay is the lowercased title and notes of each todo in all, which a
	// keystroke in the search box matches against. It is built on the first
	// search and dropped, by dropHay, whenever all is replaced or edited.
	hay []string
	// itemBuf is where a narrowed items is built, so a keystroke does not
	// allocate a list as long as everything loaded.
	itemBuf []api.Todo
	// snapshot is the whole account, read apart from the list when the view is
	// narrowed. The header, sidebar and Lists tab counts come from it, so
	// they do not change with the view.
	snapshot []api.Todo
	// snapSeq numbers loads of the snapshot made while the view is narrowed,
	// and snapCursor is where the change feed continues for it, so a
	// narrowed view keeps it current without reading the account again.
	snapSeq    int
	snapCursor string
	// marks are the todos picked for a bulk action.
	marks map[string]bool
	// pendingReveal is the outcome of a mutation waiting for the list to be
	// reloaded, so the cursor can be placed on the todo that changed.
	pendingReveal *reveal
	// owed says a bulk action was answered without the list being
	// reloaded, because a later change to one of its todos was on its way.
	// The next refresh has to be a real one, not a merge of that change.
	owed bool
	// cursor is the point from which the change feed continues, exactly as
	// the service gave it. It is set once the whole account is loaded
	// through the service, and cleared whenever the view is narrowed to
	// something the feed cannot answer.
	cursor string
	// whole says the loaded todos are the whole account, so a change can be
	// merged into them instead of reloading. It holds for the todos kept on
	// this computer too, which have no cursor.
	whole bool

	// st summarises the whole account for the header and the sidebar.
	st todoStats
	// side is the sidebar's own cursor, and hideSide folds the sidebar away.
	side     int
	hideSide bool

	// bcols are the board's columns, when the filter lays the todos out as
	// a board. bcol and brow are its cursor, and boff the first column on
	// screen.
	bcols            []boardCol
	bcol, brow, boff int
	// boardStale means bcols was not rebuilt with the last rebuild because
	// the list was on screen. Turning the board on rebuilds it, so nothing
	// reads it while it is stale.
	boardStale bool
	// regroupAt is when a todo on screen next moves to another section
	// with no change to it: the next midnight, or the next due time to
	// pass. The sections are laid out again then.
	regroupAt time.Time

	// anims are completions still being drawn, by todo id. A todo stays
	// where it is until its animation ends, so the tick is seen to land.
	anims map[string]time.Time
}

// animDur is how long a completion takes to draw. Tests set it to zero,
// which turns the animation off.
var animDur = 420 * time.Millisecond

// animating reports whether a todo's completion is still being drawn.
func (v *todosView) animating(id string) bool {
	at, ok := v.anims[id]
	return ok && time.Since(at) < animDur
}

// animProgress is how far through its animation a todo is, from 0 to 1.
func (v *todosView) animProgress(id string) float64 {
	return min(float64(time.Since(v.anims[id]))/float64(animDur), 1)
}

// reapAnims drops the animations that have finished and lets their todos
// move to where they now belong.
func (v *todosView) reapAnims() {
	ended := false
	for id, at := range v.anims {
		if time.Since(at) >= animDur {
			delete(v.anims, id)
			ended = true
		}
	}
	if ended {
		v.rebuild(v.selectedID())
	}
}

// refresh brings the list up to date as cheaply as it can: through the
// change feed when the whole account is on screen, and with a full load
// when the view is filtered or has no starting point yet.
func (v *todosView) refresh() tea.Cmd {
	// With the todos kept on this computer a full load costs nothing, and
	// the change feed would be the server's, not what is kept here.
	if v.cursor == "" || !v.unfiltered() || v.m.backend != nil {
		return v.load()
	}
	v.seq++
	v.owed = false
	return pullFeed(v.m.ctx, v.m.client, v.cursor, v.seq)
}

// merge folds changes into the loaded todos: upserts replace or add by id,
// removals drop them. It only makes sense for the unfiltered view.
func (v *todosView) merge(upserts []api.Todo, removes []string) {
	keep := v.keepAfter(removes)
	out := mergeTodos(v.all, upserts, removes)
	v.all = out
	v.snapshot = out
	v.dropHay()
	v.freshenDetail(upserts)
	v.applyLive(keep)
}

// drop takes a deleted todo off the list on screen, for a view that is
// read again from the server rather than merged into.
func (v *todosView) drop(id string) {
	gone := []string{id}
	keep := v.keepAfter(gone)
	v.all = mergeTodos(v.all, nil, gone)
	v.dropHay()
	v.applyLive(keep)
}

// keepAfter is the todo the cursor should stay on once the gone ones are
// removed. When the one under it is going, that is the next todo down, or
// the one above it at the end, so working down a list deleting todos does
// not send the cursor back to the top each time. The board keeps its
// column and row on its own.
func (v *todosView) keepAfter(gone []string) string {
	keep := v.selectedID()
	if keep == "" || v.filter.board || !slices.Contains(gone, keep) {
		return keep
	}
	going := make(map[string]bool, len(gone))
	for _, id := range gone {
		going[id] = true
	}
	stays := func(r int) bool {
		l := v.lines[r]
		return !l.header && !going[v.items[l.index].ID]
	}
	at := v.rows.selected()
	for r := at + 1; r < len(v.lines); r++ {
		if stays(r) {
			return v.items[v.lines[r].index].ID
		}
	}
	for r := at - 1; r >= 0; r-- {
		if stays(r) {
			return v.items[v.lines[r].index].ID
		}
	}
	return ""
}

// freshenDetail takes a newer copy of the todo in the detail pane from
// todos read for the list, so a change made on another device shows there
// as it does in its row, without the pane being opened again. A copy older
// than the pane's is left alone: a list read sent before an edit can
// answer after the pane has the edited todo.
func (v *todosView) freshenDetail(todos []api.Todo) {
	if v.detail.ID == "" {
		return
	}
	for _, t := range todos {
		if t.ID == v.detail.ID && !t.UpdatedAt.Before(v.detail.UpdatedAt) {
			v.detail = t
		}
	}
}

// mergeTodos returns base with the upserts replacing or joining it by id and
// the removals dropped.
func mergeTodos(base, upserts []api.Todo, removes []string) []api.Todo {
	gone := make(map[string]bool, len(removes))
	for _, id := range removes {
		gone[id] = true
	}
	pos := make(map[string]int, len(base))
	out := make([]api.Todo, 0, len(base)+len(upserts))
	for _, t := range base {
		if gone[t.ID] {
			continue
		}
		pos[t.ID] = len(out)
		out = append(out, t)
	}
	for _, t := range upserts {
		if gone[t.ID] {
			continue
		}
		if i, ok := pos[t.ID]; ok {
			out[i] = t
			continue
		}
		pos[t.ID] = len(out)
		out = append(out, t)
	}
	return out
}

// applyFeed folds a change feed into the list.
func (v *todosView) applyFeed(changes []api.SyncTodo) {
	v.merge(splitFeed(changes))
}

// applyLocal folds the server's answer to a mutation straight into the
// list, which saves asking for it again. It reports false when the view is
// not the whole account, or the outcome is not one it can place, and the
// caller reloads instead.
func (v *todosView) applyLocal(msg todoMsg) bool {
	if !v.whole || !v.unfiltered() {
		return false
	}
	switch msg.action {
	case actionCompleted, actionReopened, actionUpdated:
		if msg.todo.ID == "" {
			return false
		}
		ups := []api.Todo{msg.todo}
		if msg.next != nil {
			ups = append(ups, *msg.next)
		}
		v.merge(ups, nil)
		return true
	case actionDeleted:
		if msg.deleted == nil {
			return false
		}
		v.merge(nil, []string{msg.deleted.ID})
		return true
	}
	return false
}

// pageSize is how many todos a read of the list asks for at a time. It is the
// most the service gives in one page.
const pageSize = api.MaxPageSize

// localAll is the limit asked of the todos kept on this computer when the
// whole account is wanted. Reading them costs nothing, and leaving the limit
// out would get the store's default page of 100, not everything.
const localAll = math.MaxInt32

func newTodosView(m *Model) todosView {
	return todosView{
		m: m, filter: defaultFilter(),
		marks: map[string]bool{},
		anims: map[string]time.Time{},
	}
}

// unfiltered reports whether the view shows the whole account.
func (v *todosView) unfiltered() bool { return v.filter.unfiltered() }

// setLive narrows the loaded todos to those matching what is being typed,
// so the list answers on every keystroke instead of after a round trip.
// The counts in the header come from the unfiltered load, which typing
// does not change, so they are not recounted.
func (v *todosView) setLive(text string) {
	v.live = strings.TrimSpace(text)
	keep := v.selectedID()
	v.narrow()
	v.arrange(keep)
}

// applyLive rebuilds items from all, and drops marks on todos that are no
// longer on screen, so a bulk action never touches something hidden.
func (v *todosView) applyLive(keep string) {
	v.narrow()
	v.rebuild(keep)
}

// narrow sets items to the loaded todos that match the live search, and
// drops the marks on the ones it leaves out.
func (v *todosView) narrow() {
	v.items = v.filterLive()
	if len(v.marks) > 0 {
		visible := make(map[string]bool, len(v.items))
		for _, t := range v.items {
			visible[t.ID] = true
		}
		for id := range v.marks {
			if !visible[id] {
				delete(v.marks, id)
			}
		}
	}
}

// dropHay forgets the search text kept for the loaded todos, which has to
// happen whenever a title or notes may have changed.
func (v *todosView) dropHay() { v.hay = nil }

// filterLive keeps the todos whose title or notes contain every word typed.
func (v *todosView) filterLive() []api.Todo {
	words := strings.Fields(strings.ToLower(v.live))
	if len(words) == 0 {
		return v.all
	}
	if len(v.hay) != len(v.all) {
		v.hay = make([]string, len(v.all))
		for i := range v.all {
			v.hay[i] = strings.ToLower(v.all[i].Title + " " + deref(v.all[i].Notes))
		}
	}
	out := v.itemBuf[:0]
next:
	for i, hay := range v.hay {
		for _, w := range words {
			if !strings.Contains(hay, w) {
				continue next
			}
		}
		out = append(out, v.all[i])
	}
	v.itemBuf = out
	return out
}

// markedIDs are the marked todos in display order.
func (v *todosView) markedIDs() []string {
	var ids []string
	for _, l := range v.lines {
		if !l.header && v.marks[v.items[l.index].ID] {
			ids = append(ids, v.items[l.index].ID)
		}
	}
	return ids
}

// setSteps replaces the checklist shown in the detail pane.
func (v *todosView) setSteps(items []api.ChecklistItem) {
	v.steps = items
	v.checklist.setItems(len(items))
	v.checklist.clamp()
}

// setItems stores what the API returned and rebuilds the display.
func (v *todosView) setItems(todos []api.Todo) {
	keep := v.selectedID()
	v.all = todos
	v.dropHay()
	if v.unfiltered() {
		v.snapshot = todos
	}
	v.freshenDetail(todos)
	v.applyLive(keep)
}

// addPage adds the page after the todos loaded, which next pointed at, and
// takes the cursor of the one after that. A todo that turns up again, because
// it changed while the pages were being read, replaces the earlier copy.
func (v *todosView) addPage(todos []api.Todo, next string) {
	v.setItems(mergeTodos(v.all, todos, nil))
	v.next = next
}

// moreToLoad reports whether the list has todos beyond the ones loaded.
func (v *todosView) moreToLoad() bool { return v.next != "" }

// selectedID is the todo under the cursor, if any.
func (v *todosView) selectedID() string {
	i := v.cursorTodo()
	if i < 0 {
		return ""
	}
	return v.items[i].ID
}

// cursorTodo is the index into items of the cursor row, or -1 when the
// cursor is not on a todo.
func (v *todosView) cursorTodo() int {
	if v.filter.board {
		if v.bcol < 0 || v.bcol >= len(v.bcols) {
			return -1
		}
		idxs := v.bcols[v.bcol].idxs
		if v.brow < 0 || v.brow >= len(idxs) {
			return -1
		}
		return idxs[v.brow]
	}
	r := v.rows.selected()
	if r < 0 || r >= len(v.lines) {
		return -1
	}
	if v.lines[r].header {
		return -1
	}
	return v.lines[r].index
}

func (v *todosView) selected() *api.Todo {
	i := v.cursorTodo()
	if i < 0 {
		return nil
	}
	return &v.items[i]
}

func (v *todosView) setViewport(w, h int) {
	// The list sits inside a frame, which takes a row above and below.
	v.rows.setHeight(max(h-2, 1))
	v.checklist.setHeight(max(min(h/2, 8), 1))
}

// splitFeed separates a change feed into the todos to upsert and the ids
// of those removed.
func splitFeed(changes []api.SyncTodo) (ups []api.Todo, gone []string) {
	for _, c := range changes {
		if c.Tombstoned() {
			gone = append(gone, c.ID)
			continue
		}
		ups = append(ups, c.Todo)
	}
	return ups, gone
}

// loadSnapshot keeps the whole-account snapshot current while the view is
// narrowed, since the narrowed load cannot feed it. With the todos kept on
// this computer the read is free, so it is not limited. Through the service
// the account is read once and then followed through the change feed.
func (v *todosView) loadSnapshot() tea.Cmd {
	m := v.m
	if v.unfiltered() {
		return nil
	}
	v.snapSeq++
	var cmd tea.Cmd
	switch {
	case m.backend != nil:
		cmd = fetchTodos(m.ctx, m.client, todoFilter{}.request(localAll), v.snapSeq, true, 0)
	case v.snapCursor == "":
		cmd = fetchAccount(m.ctx, m.client, v.snapSeq)
	default:
		cmd = pullFeed(m.ctx, m.client, v.snapCursor, v.snapSeq)
	}
	return func() tea.Msg {
		switch r := cmd().(type) {
		case todosMsg:
			r.snap = true
			return r
		case feedMsg:
			r.snap = true
			return r
		default:
			return r
		}
	}
}

// onSnapshot applies a read of the whole account. A failure leaves the counts
// as they were and makes the next try start from a full read.
func (v *todosView) onSnapshot(msg todosMsg) {
	if msg.err != nil {
		v.snapCursor = ""
		return
	}
	v.setSnapshot(msg.todos, msg.cursor)
}

// onSnapshotFeed folds changes since the snapshot's cursor into it.
func (v *todosView) onSnapshotFeed(msg feedMsg) {
	if msg.err != nil || msg.cursor == "" {
		v.snapCursor = ""
		return
	}
	ups, gone := splitFeed(msg.changes)
	v.setSnapshot(mergeTodos(v.snapshot, ups, gone), msg.cursor)
}

// setSnapshot replaces the snapshot and recounts from it. A cursor of ""
// makes the next narrowed load read the account afresh.
func (v *todosView) setSnapshot(todos []api.Todo, cursor string) {
	v.snapshot = todos
	v.snapCursor = cursor
	v.computeStats()
}

// load asks the API for whatever the current mode needs.
func (v *todosView) load() tea.Cmd {
	m := v.m
	v.seq++
	v.owed = false
	if !v.unfiltered() {
		// Pages already loaded under these same filters are read again, so a
		// refresh after a change does not fold a long list back to its first
		// page. Under other filters there is nothing to put back.
		want := 0
		if v.filter.request(0) == v.loadedFor {
			want = len(v.all)
		}
		return tea.Batch(fetchTodos(m.ctx, m.client, v.filter.request(pageSize), v.seq, false, want), v.loadSnapshot())
	}
	// The feed cannot start without a cursor, and only a sync hands one out,
	// so the whole account is read that way when the service is the source.
	if v.unfiltered() && m.backend == nil {
		return fetchAccount(m.ctx, m.client, v.seq)
	}
	// Reading the todos kept on this computer costs nothing, so they are
	// all read rather than a page, which the counts would fall short by.
	return fetchTodos(m.ctx, m.client, v.filter.request(localAll), v.seq, true, 0)
}

// loadMore adds the next page to a list that did not fit in one. The page is
// of the filters the todos on screen were read under, and is dropped on
// arrival if the view has since been read under others.
func (v *todosView) loadMore(m *Model) tea.Cmd {
	if !v.moreToLoad() {
		return m.okf("everything is already loaded")
	}
	if v.filter.request(0) != v.loadedFor {
		// The filters just changed and the list for them is on its way, with
		// its own cursor. This one belongs to the list being replaced.
		return nil
	}
	f := v.filter.request(pageSize)
	f.Cursor = v.next
	v.seq++
	m.busy = true
	return tea.Batch(m.setBusy("loading more"), fetchMoreTodos(m.ctx, m.client, f, v.seq))
}

func (v *todosView) currentTodo(m *Model) *api.Todo {
	if m.inDetail() && v.detail.ID != "" {
		return &v.detail
	}
	return v.selected()
}

// -- small helpers ----------------------------------------------------------

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
