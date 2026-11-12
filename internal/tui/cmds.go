package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/auth"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
	"github.com/nerdlabz/evado-tui/internal/ops"
)

// -- results returned by commands -------------------------------------------

type meMsg struct {
	user  api.User
	lists []api.List
	err   error
	// seq is the request this answers. write says the request changed the
	// account and read it back, so its answer is newer than any read sent
	// while it was on its way, which may have looked before the change.
	seq   int
	write bool
}

type todosMsg struct {
	todos []api.Todo
	// steps are the checklists that came with the todos, by todo id, where
	// the source sends them.
	steps map[string][]api.ChecklistItem
	err   error
	// seq is the load this answers. Zero means the message is a mutation
	// outcome rather than a load, and is always handled.
	seq int
	// unfiltered says it asked for the whole account. cursor, when the
	// service gave one, is the point from which the change feed continues.
	unfiltered bool
	cursor     string
	// next is the cursor of the page after the last todo here, empty when
	// the list ends there. A full sync has none: it is not cut into pages.
	next string
	// of is the request this answers, without its page size or cursor. A
	// page is only added to the list that asked for it, because the cursor
	// means nothing under other filters.
	of api.TodoFilter
	// more says these are the next page of the list on screen, to be added
	// after what is there, not a reading of the list from its start.
	more bool
	// snap says it answers a load of the whole-account snapshot made while
	// the view is narrowed, not the list on screen.
	snap bool
}

// feedMsg is the result of catching up through the change feed.
type feedMsg struct {
	changes []api.SyncTodo
	cursor  string
	seq     int
	err     error
	// snap says it carries changes for the snapshot, not the list on screen.
	snap bool
}

// memberCacheMsg fills the member cache without touching the Lists tab.
type memberCacheMsg struct {
	listID  string
	members []api.Member
}

// bulkMsg is the outcome of an action applied to several marked todos.
type bulkMsg struct {
	verb   string
	done   int
	failed int
	err    error
}

// editorMsg carries text back from an external editor.
type editorMsg struct {
	key  string
	text string
	err  error
}

// pollMsg fires when it is time to refresh in the background.
type pollMsg struct{}

// todoMsg is the result of any todo mutation. action says what was done,
// so the receipt can name the outcome rather than just "done".
type todoMsg struct {
	todo api.Todo
	next *api.Todo
	err  error

	action todoAction
	// count is how many todos a create produced; quick capture can make
	// several from one submission.
	count int
	// title is the subject of a delete or reopen, for the receipt.
	title string
	// deleted is what a delete removed, kept so it can be put back.
	deleted *api.Todo
	// note replaces the generic receipt, for actions with something more
	// useful to say, such as where a todo was moved to.
	note string
	// warn marks an answer that shows the todo as it is but did not do
	// what was asked, such as a todo someone else took first.
	warn bool
	// undo is the undo entry this is the outcome of taking back, or zero.
	// Such an outcome must not itself be recorded as something to undo.
	undo int
	// order is where the action falls among all the changes the user
	// made, so the undo history follows what they did, not which answer
	// came back first. Zero means it did not go through the queue.
	order int
}

// todoAction is which mutation produced a todoMsg.
type todoAction int

const (
	actionNone todoAction = iota
	actionCreated
	actionUpdated
	actionCompleted
	actionReopened
	actionDeleted
)

// failed names the action in the error shown when it fails.
func (a todoAction) failed() string {
	switch a {
	case actionCreated:
		return "add todo"
	case actionDeleted:
		return "delete todo"
	}
	return "todo"
}

// detailMsg and checklistsMsg carry the pane request they answer, so an
// answer for a todo that is no longer open is dropped.
type detailMsg struct {
	req  paneReq
	todo api.Todo
	err  error
}

type checklistsMsg struct {
	req   paneReq
	items []api.ChecklistItem
	err   error
	// edited marks the answer to changing a step, rather than to opening
	// the pane: the change was made either way, so a failure is reported
	// even when the pane has moved on.
	edited bool
}

type listsMsg struct {
	lists []api.List
	// joined is set when the response came from joining an invite.
	joined *api.List
	err    error
}

type membersMsg struct {
	listID  string
	members []api.Member
	err     error
}

type inviteMsg struct {
	listID string
	invite api.Invite
	err    error
}

type syncMsg struct {
	resp api.SyncResponse
	err  error
}

type parseMsg struct {
	input   string
	preview api.ParsePreview
	err     error
}

type exportMsg struct {
	export api.Export
	// path is where the export was written; empty means preview only.
	path string
	// local means it was made from the todos kept on this computer.
	local bool
	err   error
}

type accountDeletedMsg struct {
	frozen int
	err    error
}

type tokensMsg struct {
	tokens []api.APIToken
	err    error
}

type channelsMsg struct {
	channels []api.Channel
	err      error
}

// usernameMsg is the outcome of setting or clearing the username. username
// is what was asked for, nil for a removal, so a refusal can bring the
// form back with it.
type usernameMsg struct {
	username *string
	user     api.User
	err      error
}

type toastMsg struct {
	kind toastKind
	text string
}

type tickMsg time.Time

type copyMsg struct{ text string }

type quitMsg struct{}

// signedOutMsg reports that this computer's copy of the account is gone.
// cfg is what the next screen starts from, and leftover is something that
// could not be removed, which the user should hear about without it
// undoing the sign-out.
type signedOutMsg struct {
	cfg      config.Config
	err      error
	leftover error
	// unrevoked is why the server could not be told to end the session,
	// which is gone from this computer all the same.
	unrevoked error
	// deleted says the account itself is gone, with frozen the shared
	// lists it left read-only. Its session went with it, so unrevoked
	// means nothing then.
	deleted bool
	frozen  int
}

// -- commands ---------------------------------------------------------------

// signOut forgets the account on this computer: the token or session, the
// remembered view in the settings file, and the todos kept here. It works
// from what is on disk, so a -theme or -poll given for one run is not saved
// by accident. Changes the server has not heard are sent first, if it can
// be reached, and a session is revoked last, once nothing needs it.
func signOut(ctx context.Context, running config.Config, b *local.Backend, s *auth.Session) tea.Cmd {
	return func() tea.Msg {
		if b != nil {
			_, _ = b.Sync(ctx)
		}
		if s == nil {
			// A token given for this run leaves a kept session unused, and
			// signing out removes it all the same.
			if store, err := auth.DefaultStore(); err == nil {
				s, _ = auth.Open(store, running.BaseURL)
			}
		}
		revoke := func(context.Context) error { return nil }
		if s != nil {
			var err error
			if revoke, err = s.Forget(ctx); err != nil {
				return signedOutMsg{err: err}
			}
		}
		disk, err := config.Load()
		if err != nil {
			// An unreadable file cannot be edited, so it is replaced.
			disk = running
		}
		if err := disk.SignedOut().Save(); err != nil {
			return signedOutMsg{err: err}
		}
		removeLegacyCache()
		var leftover error
		if b != nil {
			leftover = b.Forget()
		}
		return signedOutMsg{cfg: running.SignedOut(), leftover: leftover, unrevoked: revoke(ctx)}
	}
}

func toastCmd(kind toastKind, format string, args ...any) tea.Cmd {
	return func() tea.Msg {
		return toastMsg{kind: kind, text: fmt.Sprintf(format, args...)}
	}
}

// tickInterval paces the clock refresh and toast expiry. It is a variable
// so tests do not have to wait for real time to pass.
var tickInterval = 3 * time.Second

func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fetchMe(ctx context.Context, c *api.Client, seq int) tea.Cmd {
	return func() tea.Msg {
		u, ls, err := c.Me(ctx)
		return meMsg{user: u, lists: ls, err: err, seq: seq}
	}
}

// fetchTodos reads f from its first page. The service gives a list a page at
// a time, so it goes on to the next until it holds at least want todos or the
// list ends; a want of 0 is just the first page. That is how a reload puts
// back the pages the person had already asked for.
func fetchTodos(ctx context.Context, c *api.Client, f api.TodoFilter, seq int, unfiltered bool, want int) tea.Cmd {
	return func() tea.Msg {
		out := readPages(ctx, c, f, want)
		out.seq, out.unfiltered = seq, unfiltered
		return out
	}
}

// fetchMoreTodos reads the page of f that follows f.Cursor.
func fetchMoreTodos(ctx context.Context, c *api.Client, f api.TodoFilter, seq int) tea.Cmd {
	return func() tea.Msg {
		out := readPages(ctx, c, f, 0)
		out.seq, out.more = seq, true
		return out
	}
}

// readPages reads pages of f, from f.Cursor, until it holds at least want
// todos or the list ends. Every page after the first asks only for what is
// still wanted, so a reload does not end up with more than it had.
func readPages(ctx context.Context, c *api.Client, f api.TodoFilter, want int) todosMsg {
	var out todosMsg
	out.of = f
	out.of.Limit, out.of.Cursor = 0, ""
	size := f.Limit
	for {
		page, err := c.ListTodosPage(ctx, f)
		if err != nil {
			return todosMsg{err: err}
		}
		if out.todos == nil {
			out.todos = page.Todos
		} else {
			// A todo whose date moved while the pages were being read can
			// turn up on two of them; the later copy is the newer.
			out.todos = mergeTodos(out.todos, page.Todos, nil)
		}
		for id, items := range page.Steps {
			if out.steps == nil {
				out.steps = map[string][]api.ChecklistItem{}
			}
			out.steps[id] = items
		}
		out.next = page.NextCursor
		// A cursor that does not move, or an empty page that says more
		// follow, would be asked for again forever.
		if out.next == "" || len(out.todos) >= want || out.next == f.Cursor || len(page.Todos) == 0 {
			return out
		}
		f.Cursor = out.next
		f.Limit = min(size, want-len(out.todos))
	}
}

// fetchAccount reads every live todo with an initial sync and follows it to
// the end, which also gives the cursor the change feed continues from. The
// feed repeats a todo that changed while it was being read, so the last copy
// wins, and a tombstone removes it.
func fetchAccount(ctx context.Context, c *api.Client, seq int) tea.Cmd {
	return func() tea.Msg {
		out := todosMsg{seq: seq, unfiltered: true}
		at := map[string]int{}
		var all []api.Todo
		out.steps = map[string][]api.ChecklistItem{}
		cursor := ""
		for range maxFeedPages {
			var resp api.SyncResponse
			var err error
			if cursor == "" {
				resp, err = c.Sync(ctx, api.SyncRequest{})
			} else {
				resp, err = c.ChangeFeed(ctx, cursor)
			}
			if err != nil {
				out.err = err
				return out
			}
			for _, st := range resp.Todos {
				i, seen := at[st.ID]
				switch {
				case st.Tombstoned() && seen:
					all[i] = api.Todo{}
					delete(out.steps, st.ID)
				case st.Tombstoned():
				case seen:
					all[i] = st.Todo
				default:
					at[st.ID] = len(all)
					all = append(all, st.Todo)
				}
				if !st.Tombstoned() && st.Checklist != nil {
					out.steps[st.ID] = st.Checklist
				}
			}
			if !resp.HasMore {
				out.todos = slices.DeleteFunc(all, func(t api.Todo) bool { return t.ID == "" })
				out.cursor = resp.Cursor
				return out
			}
			if resp.Cursor == "" || resp.Cursor == cursor {
				break
			}
			cursor = resp.Cursor
		}
		out.err = errors.New("the account could not be read to the end")
		return out
	}
}

// maxFeedPages bounds how long one catch-up can run.
const maxFeedPages = 50

// pullFeed asks the server only for what changed since a point in time,
// following hasMore until it has everything. It is what keeps a refresh
// cheap: a handful of todos instead of the whole account.
func pullFeed(ctx context.Context, c *api.Client, since string, seq int) tea.Cmd {
	return func() tea.Msg {
		var out feedMsg
		out.seq = seq
		cursor := since
		for i := 0; i < maxFeedPages; i++ {
			resp, err := c.ChangeFeed(ctx, cursor)
			if err != nil {
				out.err = err
				return out
			}
			out.changes = append(out.changes, resp.Todos...)
			out.cursor = resp.Cursor
			if !resp.HasMore || resp.Cursor == "" || resp.Cursor == cursor {
				return out
			}
			cursor = resp.Cursor
		}
		return out
	}
}

// fetchMemberCache loads a list's members for display elsewhere, such as
// naming an assignee, without switching the Lists tab into members mode.
func fetchMemberCache(ctx context.Context, c *api.Client, listID string) tea.Cmd {
	return func() tea.Msg {
		ms, err := c.ListMembers(ctx, listID)
		if err != nil {
			return nil
		}
		return memberCacheMsg{listID: listID, members: ms}
	}
}

// pollCmd schedules the next background refresh.
func pollCmd(every time.Duration) tea.Cmd {
	if pollOverride > 0 {
		every = pollOverride
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return pollMsg{} })
}

// pollOverride replaces the configured interval, so tests do not have to
// wait minutes for a refresh.
var pollOverride time.Duration

// bulkTodos makes one change to every id, counting what worked so a
// partial failure is reported rather than hidden. The todos kept on this
// computer, in b, take the change as one batch, which writes their file
// once instead of once for each todo. The service is sent one request for
// each, through do.
func bulkTodos(verb string, ids []string, b *local.Backend, ch local.Change, do func(id string) error) tea.Cmd {
	if b != nil {
		return func() tea.Msg {
			out := bulkMsg{verb: verb}
			for _, o := range b.ApplyMany(ids, ch) {
				out.count(o.Err)
			}
			return out
		}
	}
	return func() tea.Msg {
		out := bulkMsg{verb: verb}
		for _, id := range ids {
			err := do(id)
			out.count(err)
			if err != nil && api.CodeOf(err) == api.CodeUnauthorized {
				break
			}
		}
		return out
	}
}

// count adds one todo's outcome to the tally.
func (b *bulkMsg) count(err error) {
	if err == nil {
		b.done++
		return
	}
	b.failed++
	if b.err == nil {
		b.err = err
	}
}

func fetchTodo(ctx context.Context, c *api.Client, req paneReq) tea.Cmd {
	return func() tea.Msg {
		t, err := c.GetTodo(ctx, req.id)
		return detailMsg{req: req, todo: t, err: err}
	}
}

func createTodo(ctx context.Context, c *api.Client, n api.NewTodo) tea.Cmd {
	return func() tea.Msg {
		ts, err := c.CreateTodos(ctx, n)
		if api.CodeOf(err) == api.CodeTooManyLines {
			// More lines than one request holds. Nothing was made, so send
			// them a batch at a time, the way `evado add -` does.
			ts, err = createInBatches(ctx, c, n)
		}
		if err != nil {
			return todoMsg{err: err, action: actionCreated}
		}
		if len(ts) == 0 {
			return todoMsg{action: actionCreated}
		}
		return todoMsg{todo: ts[0], next: nil, action: actionCreated, count: len(ts), title: ts[0].Title}
	}
}

// createInBatches makes the todos of a capture too long for one request, in
// order. A batch that fails stops the rest and the error says how many todos
// were already made, since they are there and the lines are not to be sent
// again.
func createInBatches(ctx context.Context, c *api.Client, n api.NewTodo) ([]api.Todo, error) {
	batches, err := ops.Captures(n.Title)
	if err != nil {
		return nil, err
	}
	var made []api.Todo
	for _, b := range batches {
		n.Title = b
		ts, err := c.CreateTodos(ctx, n)
		made = append(made, ts...)
		if err != nil {
			if len(made) > 0 {
				err = fmt.Errorf("%d todos were added before this failed: %w", len(made), err)
			}
			return made, err
		}
	}
	return made, nil
}

func patchTodo(ctx context.Context, c *api.Client, id string, p api.TodoPatch) tea.Cmd {
	return func() tea.Msg {
		t, next, err := c.UpdateTodo(ctx, id, p)
		return todoMsg{todo: t, next: next, err: err, action: actionUpdated, title: t.Title}
	}
}

// rescheduleTodo patches a todo's day and words the receipt for it.
func rescheduleTodo(ctx context.Context, c *api.Client, id string, p api.TodoPatch, note string) tea.Cmd {
	return func() tea.Msg {
		t, next, err := c.UpdateTodo(ctx, id, p)
		return todoMsg{todo: t, next: next, err: err, action: actionUpdated, title: t.Title, note: note}
	}
}

func completeTodo(ctx context.Context, c *api.Client, id string) tea.Cmd {
	return func() tea.Msg {
		t, next, err := c.CompleteTodo(ctx, id)
		return todoMsg{todo: t, next: next, err: err, action: actionCompleted, title: t.Title}
	}
}

func uncompleteTodo(ctx context.Context, c *api.Client, id string) tea.Cmd {
	return func() tea.Msg {
		t, err := c.UncompleteTodo(ctx, id)
		return todoMsg{todo: t, err: err, action: actionReopened, title: t.Title}
	}
}

// deleteTodo takes the whole todo, so the receipt can name what is gone and
// an undo can put it back.
func deleteTodo(ctx context.Context, c *api.Client, t api.Todo) tea.Cmd {
	return func() tea.Msg {
		if err := c.DeleteTodo(ctx, t.ID); err != nil {
			return todoMsg{err: err, action: actionDeleted}
		}
		return todoMsg{action: actionDeleted, title: t.Title, deleted: &t}
	}
}

// asUndo marks the todoMsg a command produces as the outcome of taking
// back undo entry seq.
func asUndo(seq int, cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		r := cmd()
		if t, ok := r.(todoMsg); ok {
			t.undo = seq
			return t
		}
		return r
	}
}

func fetchChecklist(ctx context.Context, c *api.Client, req paneReq) tea.Cmd {
	return func() tea.Msg {
		items, err := c.ListChecklist(ctx, req.id)
		return checklistsMsg{req: req, items: items, err: err}
	}
}

// stepsEdited is the answer to changing one of a todo's steps.
func stepsEdited(todoID string, items []api.ChecklistItem, err error) checklistsMsg {
	return checklistsMsg{req: paneReq{id: todoID}, items: items, err: err, edited: true}
}

func addChecklistItem(ctx context.Context, c *api.Client, todoID, title string) tea.Cmd {
	return func() tea.Msg {
		if _, err := c.AddChecklistItem(ctx, todoID, title); err != nil {
			return stepsEdited(todoID, nil, err)
		}
		items, err := c.ListChecklist(ctx, todoID)
		return stepsEdited(todoID, items, err)
	}
}

func toggleChecklistItem(ctx context.Context, c *api.Client, item api.ChecklistItem) tea.Cmd {
	return func() tea.Msg {
		updated, err := c.ToggleChecklistItem(ctx, item)
		if err != nil {
			return stepsEdited(item.TodoID, nil, err)
		}
		items, err := c.ListChecklist(ctx, item.TodoID)
		if err != nil {
			return stepsEdited(item.TodoID, []api.ChecklistItem{updated}, err)
		}
		return stepsEdited(item.TodoID, items, nil)
	}
}

func renameChecklistItem(ctx context.Context, c *api.Client, item api.ChecklistItem, title string) tea.Cmd {
	return func() tea.Msg {
		if _, err := c.UpdateChecklistItem(ctx, item.ID, &title, nil); err != nil {
			return stepsEdited(item.TodoID, nil, err)
		}
		items, err := c.ListChecklist(ctx, item.TodoID)
		return stepsEdited(item.TodoID, items, err)
	}
}

func deleteChecklistItem(ctx context.Context, c *api.Client, item api.ChecklistItem) tea.Cmd {
	return func() tea.Msg {
		if err := c.DeleteChecklistItem(ctx, item.ID); err != nil {
			return stepsEdited(item.TodoID, nil, err)
		}
		items, err := c.ListChecklist(ctx, item.TodoID)
		return stepsEdited(item.TodoID, items, err)
	}
}

func fetchLists(ctx context.Context, c *api.Client) tea.Cmd {
	return func() tea.Msg {
		ls, err := c.ListLists(ctx)
		return listsMsg{lists: ls, err: err}
	}
}

func createList(ctx context.Context, c *api.Client, name string) tea.Cmd {
	return func() tea.Msg {
		if _, err := c.CreateList(ctx, name); err != nil {
			return listsMsg{err: err}
		}
		ls, err := c.ListLists(ctx)
		return listsMsg{lists: ls, err: err}
	}
}

func renameList(ctx context.Context, c *api.Client, id, name string) tea.Cmd {
	return func() tea.Msg {
		if _, err := c.RenameList(ctx, id, name); err != nil {
			return listsMsg{err: err}
		}
		ls, err := c.ListLists(ctx)
		return listsMsg{lists: ls, err: err}
	}
}

func deleteList(ctx context.Context, c *api.Client, id string) tea.Cmd {
	return func() tea.Msg {
		if err := c.DeleteList(ctx, id); err != nil {
			return listsMsg{err: err}
		}
		ls, err := c.ListLists(ctx)
		return listsMsg{lists: ls, err: err}
	}
}

func fetchMembers(ctx context.Context, c *api.Client, listID string) tea.Cmd {
	return func() tea.Msg {
		ms, err := c.ListMembers(ctx, listID)
		return membersMsg{listID: listID, members: ms, err: err}
	}
}

func removeMember(ctx context.Context, c *api.Client, listID, userID string) tea.Cmd {
	return func() tea.Msg {
		if err := c.RemoveMember(ctx, listID, userID); err != nil {
			return membersMsg{listID: listID, err: err}
		}
		ms, listErr := c.ListMembers(ctx, listID)
		return membersMsg{listID: listID, members: ms, err: listErr}
	}
}

func createInvite(ctx context.Context, c *api.Client, listID string) tea.Cmd {
	return func() tea.Msg {
		inv, err := c.CreateInvite(ctx, listID)
		return inviteMsg{listID: listID, invite: inv, err: err}
	}
}

func joinList(ctx context.Context, c *api.Client, code string) tea.Cmd {
	return func() tea.Msg {
		l, err := c.JoinList(ctx, code)
		if err != nil {
			return listsMsg{err: err}
		}
		ls, listErr := c.ListLists(ctx)
		return listsMsg{lists: ls, joined: &l, err: listErr}
	}
}

// pullChanges fetches the change feed. With no cursor it runs an initial
// sync, which the spec defines as "every live todo"; with a cursor it
// pulls only what changed after that cursor.
func pullChanges(ctx context.Context, c *api.Client, since string) tea.Cmd {
	return func() tea.Msg {
		if since == "" {
			resp, err := c.Sync(ctx, api.SyncRequest{})
			return syncMsg{resp: resp, err: err}
		}
		resp, err := c.ChangeFeed(ctx, since)
		return syncMsg{resp: resp, err: err}
	}
}

func parseTitle(ctx context.Context, c *api.Client, title string) tea.Cmd {
	return func() tea.Msg {
		p, err := c.Parse(ctx, title)
		return parseMsg{input: title, preview: p, err: err}
	}
}

func exportAccount(ctx context.Context, c *api.Client, path string) tea.Cmd {
	return func() tea.Msg {
		ex, err := c.ExportTodos(ctx)
		if err != nil {
			return exportMsg{err: err}
		}
		if path == "" {
			return exportMsg{export: ex}
		}
		if err := writeExport(path, ex); err != nil {
			return exportMsg{err: fmt.Errorf("write %s: %w", path, err)}
		}
		return exportMsg{export: ex, path: path}
	}
}

// exportComputer writes the todos kept on this computer, for when there
// is no account to ask or the account will not export to this sign-in.
func exportComputer(b *local.Backend, path string) tea.Cmd {
	return func() tea.Msg {
		ex, err := b.ExportChecked()
		if err != nil {
			return exportMsg{err: err}
		}
		if path == "" {
			return exportMsg{export: ex, local: true}
		}
		if err := writeExport(path, ex); err != nil {
			return exportMsg{err: fmt.Errorf("write %s: %w", path, err)}
		}
		return exportMsg{export: ex, path: path, local: true}
	}
}

// forgetDeleted signs this computer out of an account that was just
// deleted. Otherwise its session and its todos stay here, and the next run
// opens on a session the server no longer knows.
func forgetDeleted(ctx context.Context, running config.Config, b *local.Backend, s *auth.Session, frozen int) tea.Cmd {
	out := signOut(ctx, running, b, s)
	return func() tea.Msg {
		msg, _ := out().(signedOutMsg)
		msg.deleted, msg.frozen = true, frozen
		return msg
	}
}

func deleteAccount(ctx context.Context, c *api.Client, email string) tea.Cmd {
	return func() tea.Msg {
		frozen, err := c.DeleteAccount(ctx, email)
		return accountDeletedMsg{frozen: frozen, err: err}
	}
}

func setTimezone(ctx context.Context, c *api.Client, tz string, seq int) tea.Cmd {
	return func() tea.Msg {
		u, err := c.SetTimezone(ctx, tz)
		if err == nil {
			// PATCH returns a user, but availability lives on GET /me.
			if fresh, _, readErr := c.Me(ctx); readErr == nil {
				u = fresh
			}
		}
		return meMsg{user: u, err: err, seq: seq, write: true}
	}
}

// saveUsername sets the username, or removes it when username is nil.
func saveUsername(ctx context.Context, c *api.Client, username *string) tea.Cmd {
	return func() tea.Msg {
		u, err := c.SetUsername(ctx, username)
		return usernameMsg{username: username, user: u, err: err}
	}
}

func fetchTokens(ctx context.Context, c *api.Client) tea.Cmd {
	return func() tea.Msg {
		ts, err := c.ListTokens(ctx)
		return tokensMsg{tokens: ts, err: err}
	}
}

func fetchChannels(ctx context.Context, c *api.Client) tea.Cmd {
	return func() tea.Msg {
		cs, err := c.ListChannels(ctx)
		return channelsMsg{channels: cs, err: err}
	}
}

// smartParsingMsg carries a confirmed account choice, never an optimistic toggle.
type smartParsingMsg struct {
	user api.User
	err  error
}

func saveSmartParsing(ctx context.Context, c *api.Client, enabled bool) tea.Cmd {
	return func() tea.Msg {
		u, err := c.UpdateProfile(ctx, api.ProfilePatch{SmartParsing: &enabled})
		return smartParsingMsg{user: u, err: err}
	}
}
