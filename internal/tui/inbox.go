package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// The inbox lists the invitations to shared lists that are waiting for an
// answer. It lives inside the Lists tab, opened with I, so the keys of the
// tabs stay where they were.

// inviteAction is what is done to an invitation.
type inviteAction int

const (
	inviteAccept inviteAction = iota
	inviteDecline
	inviteRestore
)

// maxDeclined is how many declines stay undoable. The service allows seven
// days, but this only has to cover changing your mind.
const maxDeclined = 10

type (
	// invitationsMsg is the answer to loading the inbox. seq says which
	// load it is, so a slow answer to an old one is dropped.
	invitationsMsg struct {
		seq  int
		list api.InvitationList
		err  error
	}
	// answeredMsg is the outcome of accepting, declining or restoring one.
	// On success list is the inbox read again, and lists the account's lists
	// after an accept; a failed re-read leaves listErr, and the inbox is
	// then corrected by hand.
	answeredMsg struct {
		action  inviteAction
		inv     api.Invitation
		list    api.InvitationList
		listErr error
		lists   []api.List
		err     error
	}
)

func fetchInvitations(ctx context.Context, c *api.Client, seq int) tea.Cmd {
	return func() tea.Msg {
		got, err := c.Invitations(ctx)
		return invitationsMsg{seq: seq, list: got, err: err}
	}
}

// answerInvitation does one thing to an invitation and then reads the
// inbox again, so what is shown is what the service holds.
func answerInvitation(ctx context.Context, c *api.Client, action inviteAction, inv api.Invitation) tea.Cmd {
	return func() tea.Msg {
		out := answeredMsg{action: action, inv: inv}
		switch action {
		case inviteAccept:
			_, out.err = c.AcceptInvitation(ctx, inv.ID)
		case inviteDecline:
			out.err = c.DeclineInvitation(ctx, inv.ID)
		case inviteRestore:
			_, out.err = c.RestoreInvitation(ctx, inv.ID)
		}
		if out.err != nil {
			return out
		}
		out.list, out.listErr = c.Invitations(ctx)
		if action == inviteAccept {
			// The joined list has to be in the lists before its todos can
			// be shown, and the local copy pulls them as part of accepting.
			if ls, err := c.ListLists(ctx); err == nil {
				out.lists = ls
			}
		}
		return out
	}
}

// inboxView is the invitation inbox.
type inboxView struct {
	m *Model

	open  bool
	rows  table
	items []api.Invitation

	// loaded says a list has been read at least once, loading that one is
	// on its way, and seq numbers the loads.
	loaded, loading bool
	seq             int
	// err is why the last load failed, and stale says what is shown is the
	// copy kept on this computer, or an earlier read, rather than a fresh one.
	err   error
	stale bool
	// unavailable is set when there is nothing to show at all and no way to
	// get it, which is being offline with nothing saved yet.
	unavailable bool
	// answering is set while one of the actions is in flight.
	answering bool
	// declined are the invitations turned down and still undoable, the
	// latest last.
	declined []api.Invitation
}

// close leaves the inbox.
func (v *inboxView) close() { v.open, v.answering = false, false }

// show opens the inbox and starts reading it. A guest has no account for
// anyone to invite, so there is nothing to read.
func (v *inboxView) show() tea.Cmd {
	v.open = true
	v.rows.top()
	if v.m.guest() {
		return nil
	}
	return v.load()
}

func (v *inboxView) load() tea.Cmd {
	m := v.m
	if m.guest() {
		return nil
	}
	v.seq++
	v.loading = true
	m.busy = true
	return tea.Batch(m.setBusy("loading invitations"), fetchInvitations(m.ctx, m.client, v.seq))
}

func (v *inboxView) selected() *api.Invitation {
	i := v.rows.selected()
	if i < 0 || i >= len(v.items) {
		return nil
	}
	return &v.items[i]
}

// setItems replaces what is shown, newest first, keeping the cursor on the
// same invitation when it is still there.
func (v *inboxView) setItems(list []api.Invitation) {
	keep := ""
	if sel := v.selected(); sel != nil {
		keep = sel.ID
	}
	v.items = append([]api.Invitation(nil), list...)
	sort.SliceStable(v.items, func(i, j int) bool { return v.items[i].CreatedAt.After(v.items[j].CreatedAt) })
	v.rows.setItems(len(v.items))
	for i, inv := range v.items {
		if inv.ID == keep {
			v.rows.cursor = i
			v.rows.clamp()
			break
		}
	}
}

// onLoaded takes in a read of the inbox.
func (v *inboxView) onLoaded(msg invitationsMsg) tea.Cmd {
	m := v.m
	if msg.seq != v.seq {
		return nil
	}
	m.done()
	v.loading = false
	switch {
	case msg.err == nil:
		v.loaded, v.err, v.unavailable = true, nil, false
		v.stale = msg.list.Cached
		v.setItems(msg.list.Invitations)
		return nil
	case api.CodeOf(msg.err) == api.CodeUnauthorized:
		return m.failf(msg.err, "")
	case unreachable(msg.err):
		// What is on screen, if anything, may be out of date now.
		v.err = nil
		v.stale = true
		v.unavailable = !v.loaded
		return nil
	}
	v.err = msg.err
	return nil
}

// onAnswered takes in the outcome of an action.
func (v *inboxView) onAnswered(msg answeredMsg) tea.Cmd {
	m := v.m
	m.done()
	v.answering = false
	name := msg.inv.List.Name
	if msg.err != nil {
		return v.onRefused(msg)
	}
	// The inbox is whatever the service now holds. If reading it failed,
	// the one answered is taken out, or put back, by hand.
	if msg.listErr == nil {
		v.stale = msg.list.Cached
		v.setItems(msg.list.Invitations)
	} else {
		v.stale = true
		v.setItems(v.adjusted(msg))
	}
	v.err, v.unavailable, v.loaded = nil, false, true

	switch msg.action {
	case inviteAccept:
		if msg.lists != nil {
			m.lists = msg.lists
			m.listsV.setItems(m.lists)
		}
		// The joined list's todos come with the next pull, which the local
		// copy has done by now; loading them is what puts them on screen.
		m.busy = true
		return tea.Batch(
			m.okf("joined %q; its todos are loading", name),
			m.setBusy("loading todos"), m.todos.load(), m.syncNow(),
		)
	case inviteDecline:
		v.declined = append(v.declined, msg.inv)
		if len(v.declined) > maxDeclined {
			v.declined = v.declined[len(v.declined)-maxDeclined:]
		}
		return m.okf("declined %q. Nobody is told. Press u to undo.", name)
	}
	v.forget(msg.inv.ID)
	return m.okf("%q is waiting for you again", name)
}

// adjusted is the inbox with one invitation's answer applied by hand.
func (v *inboxView) adjusted(msg answeredMsg) []api.Invitation {
	var out []api.Invitation
	for _, inv := range v.items {
		if inv.ID != msg.inv.ID {
			out = append(out, inv)
		}
	}
	if msg.action == inviteRestore {
		out = append(out, msg.inv)
	}
	return out
}

// forget drops an invitation from the ones that can be undone.
func (v *inboxView) forget(id string) {
	kept := v.declined[:0]
	for _, inv := range v.declined {
		if inv.ID != id {
			kept = append(kept, inv)
		}
	}
	v.declined = kept
}

// onRefused explains a failed action and leaves the inbox matching the
// service: nothing is taken out or put back on a guess.
func (v *inboxView) onRefused(msg answeredMsg) tea.Cmd {
	m := v.m
	name := msg.inv.List.Name
	switch {
	case api.CodeOf(msg.err) == api.CodeUnauthorized:
		return m.failf(msg.err, "")
	case unreachable(msg.err):
		return m.warnf("%s needs a connection. Nothing was changed; try again when you are online", verbOf(msg.action))
	case api.IsNotFound(msg.err):
		// Withdrawn, answered on another device, or past the seven days.
		// The invitation cannot be acted on again, so it is not kept as
		// something to undo either.
		v.forget(msg.inv.ID)
		text := fmt.Sprintf("the invitation to %q is no longer available", name)
		if msg.action == inviteRestore {
			text += ", so the decline cannot be undone. It may have been withdrawn, or the 7 days are up"
		}
		return tea.Batch(m.warnf("%s", text), v.load())
	}
	return tea.Batch(m.failf(msg.err, "invitation"), v.load())
}

// verbOf names an action for a message.
func verbOf(a inviteAction) string {
	switch a {
	case inviteAccept:
		return "accepting"
	case inviteDecline:
		return "declining"
	}
	return "undoing a decline"
}

// answer starts an action on the selected invitation.
func (v *inboxView) answer(action inviteAction) tea.Cmd {
	m := v.m
	if m.guest() {
		return m.warnf("answering invitations needs an Evado account; press s to sign in")
	}
	if v.answering {
		return nil
	}
	var inv api.Invitation
	switch action {
	case inviteRestore:
		if len(v.declined) == 0 {
			return m.warnf("nothing to undo: no invitation was declined just now")
		}
		inv = v.declined[len(v.declined)-1]
	default:
		sel := v.selected()
		if sel == nil {
			return nil
		}
		inv = *sel
	}
	if v.stale {
		return m.warnf("this list is the last one saved on this computer. Accepting or declining needs a connection: " +
			"press r when you are online")
	}
	// A read that is still on its way may predate this answer, and must not
	// put back what the answer is about to take out.
	v.seq++
	v.loading = false
	v.answering = true
	m.busy = true
	return tea.Batch(m.setBusy(verbOf(action)), answerInvitation(m.ctx, m.client, action, inv))
}

func (v *inboxView) onKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		v.rows.move(-1)
	case "down", "j":
		v.rows.move(1)
	case "pgup", "ctrl+b":
		v.rows.pageUp()
	case "pgdown", "ctrl+f":
		v.rows.pageDown()
	case "g", "home":
		v.rows.top()
	case "G", "end":
		v.rows.bottom()
	case "enter", "a":
		return m, v.answer(inviteAccept)
	case "d":
		return m, v.answer(inviteDecline)
	case "u":
		return m, v.answer(inviteRestore)
	case "s":
		if m.guest() {
			return m, m.openSignIn()
		}
	case "esc", "I", "h", "left":
		v.close()
	}
	return m, nil
}

// openSignIn shows the sign-in screen from inside a running session, the
// way Account, Connection does.
func (m *Model) openSignIn() tea.Cmd {
	m.layer = layerLogin
	m.login = m.newLogin(true)
	return nil
}

// -- drawing ----------------------------------------------------------------

func (v *inboxView) summary() string {
	n := len(v.items)
	if !v.loaded || v.m.guest() {
		return ""
	}
	return styMuted.Render(plural(n, "invitation"))
}

// fromLabel is who sent an invitation: their username, or else the masked
// email the service returned.
func fromLabel(inv api.Invitation) string {
	if h := api.Handle(inv.InvitedBy.Username); h != "" {
		return h
	}
	return inv.InvitedBy.Email
}

func (v *inboxView) view(width, height int) string {
	if !framed(width, height) {
		return v.body(width, height)
	}
	return scrollable(frame(v.body(width-2, height-2), width, height, frameOpts{title: "Invitations", foot: v.summary(), focused: true}), v.m.wheelKeys)
}

// body is what goes inside the frame, width and height being its own.
func (v *inboxView) body(width, height int) string {
	m := v.m
	inner := max(width, 10)
	wrap := func(style lipgloss.Style, text string) []string {
		var out []string
		for _, l := range wrapText(text, inner-2) {
			out = append(out, " "+style.Render(l))
		}
		return out
	}
	switch {
	case m.guest():
		lines := []string{""}
		lines = append(lines, wrap(styBold, "Invitations need an Evado account.")...)
		lines = append(lines, "")
		lines = append(lines, wrap(styMuted, "This computer keeps its todos to itself, so nobody has an account here to invite. "+
			"Sign in and the invitations people send you will show up here.")...)
		lines = append(lines, "", " "+keyed("s", styKey.Render("s")+styFaint.Render(" sign in"))+"   "+keyed("esc", styKey.Render("esc")+styFaint.Render(" back to lists")))
		return strings.Join(lines, "\n")
	case v.unavailable:
		lines := []string{""}
		lines = append(lines, wrap(styWarn, "Invitations are unavailable offline.")...)
		lines = append(lines, "")
		lines = append(lines, wrap(styMuted, "Nothing has been saved on this computer yet. Reconnect and press r.")...)
		return strings.Join(lines, "\n")
	case !v.loaded && v.err != nil:
		lines := []string{""}
		lines = append(lines, wrap(styErr, "Could not load your invitations: "+errText(v.err))...)
		lines = append(lines, "", " "+keyed("r", styKey.Render("r")+styFaint.Render(" try again")))
		return strings.Join(lines, "\n")
	case !v.loaded:
		return "\n " + styAccent.Render(m.spinner()) + " " + styMuted.Render("loading invitations")
	}

	var head []string
	switch {
	case v.stale:
		head = wrap(styWarn, "Offline: this is the list from the last sync. Answering needs a connection.")
	case v.err != nil:
		head = wrap(styErr, "Could not refresh: "+errText(v.err))
	}
	var tail []string
	if n := len(v.declined); n > 0 {
		last := v.declined[n-1]
		tail = append([]string{""}, wrap(styMuted, fmt.Sprintf("Declined %q. u undoes it; nobody was told.", last.List.Name))...)
	}
	if len(v.items) == 0 {
		lines := append(head, "")
		lines = append(lines, wrap(styBold, "No invitations waiting.")...)
		hint := "When someone invites you to a shared list it shows up here."
		if m.user.Username == nil {
			hint += " They find you by username, which you can set in Account, Profile."
		}
		lines = append(lines, wrap(styMuted, hint)...)
		return strings.Join(append(lines, tail...), "\n")
	}
	// What is left after the notes is the room the rows have.
	v.rows.setHeight(max(height-len(head)-len(tail), 1))
	v.rows.setItems(len(v.items))
	list := v.rows.render(inner, func(i int, sel bool) string { return v.row(v.items[i], sel, inner-1) }, func(i int, _ bool) tea.Cmd {
		v.rows.cursor = i
		v.rows.clamp()
		return nil
	})
	out := append(head, list)
	return strings.Join(append(out, tail...), "\n")
}

// row is one invitation: the list it is to, who sent it, and when.
func (v *inboxView) row(inv api.Invitation, sel bool, width int) string {
	m := v.m
	base := lipgloss.NewStyle()
	bar := " "
	if sel {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	when := relative(&inv.CreatedAt, m.now)
	if inv.CreatedAt.IsZero() {
		when = ""
	}
	right := base.Foreground(colFaint).Render(when)
	from := base.Foreground(colMuted).Render("  from " + fromLabel(inv))
	nameW := max(width-visibleWidth(right)-visibleWidth(from)-4, 6)
	name := truncate(inv.List.Name, nameW)
	text := base.Foreground(colFg)
	if sel {
		text = text.Bold(true)
	}
	left := bar + base.Foreground(tagColor(inv.List.ID)).Render("● ") + text.Render(name) + from
	gap := max(width-visibleWidth(left)-visibleWidth(right)-1, 1)
	row := left + base.Render(strings.Repeat(" ", gap)) + right
	return row + base.Render(strings.Repeat(" ", max(width-visibleWidth(row), 0)))
}

func (v *inboxView) footer() []helpEntry {
	if v.m.guest() {
		return []helpEntry{{"s", "sign in"}, {"esc", "back to lists"}}
	}
	return []helpEntry{
		{"j/k", "move"},
		{"enter", "accept"},
		{"d", "decline"},
		{"u", "undo decline"},
		{"r", "refresh"},
		{"esc", "back to lists"},
	}
}
