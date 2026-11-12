package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// listsView shows the account's lists, their members, and the sharing
// actions available on each.
type listsView struct {
	m *Model

	rows  table
	items []api.List

	showMembers bool
	members     []api.Member
	membersFor  string
	// memberRows scrolls the member pane when a list has many members.
	memberRows table

	invite *api.Invite

	// unsynced are the lists the account does not have yet, as of the last
	// time the pane was drawn.
	unsynced map[string]bool

	// inbox is the invitations waiting for an answer, shown in place of the
	// lists while it is open.
	inbox inboxView
}

func newListsView(m *Model) listsView {
	return listsView{m: m, inbox: inboxView{m: m}}
}

func (v *listsView) setItems(lists []api.List) {
	prev := v.selectedID()
	v.items = lists
	v.rows.setItems(len(lists))
	// Keep the cursor on the same list across a refresh.
	if prev != "" {
		for i, l := range lists {
			if l.ID == prev {
				v.rows.cursor = i
				v.rows.clamp()
				return
			}
		}
	}
}

func (v *listsView) setViewport(_, h int) {
	v.rows.setHeight(max(h-2, 1))
	v.memberRows.setHeight(max(min(h/3, 8), 1))
}

func (v *listsView) selected() *api.List {
	i := v.rows.selected()
	if i < 0 || i >= len(v.items) {
		return nil
	}
	return &v.items[i]
}

// notSynced reports whether a list is one the account does not have yet,
// which can be neither shared nor asked for its members until a sync has
// made it there. With no account no list is.
func (v *listsView) notSynced(id string) bool {
	return v.m.backend != nil && v.m.backend.Unsynced()[id]
}

func (v *listsView) selectedID() string {
	if l := v.selected(); l != nil {
		return l.ID
	}
	return ""
}

func (v *listsView) load() tea.Cmd {
	if v.inbox.open {
		return v.inbox.load()
	}
	return fetchLists(v.m.ctx, v.m.client)
}

func (v *listsView) onKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if v.inbox.open {
		return v.inbox.onKey(m, msg)
	}
	if m.focus == focusMembers {
		return v.onMemberKey(m, msg)
	}
	switch k {
	case "up", "k":
		v.rows.move(-1)
		return m, nil
	case "down", "j":
		v.rows.move(1)
		return m, nil
	case "pgup", "ctrl+b":
		v.rows.pageUp()
		return m, nil
	case "pgdown", "ctrl+f":
		v.rows.pageDown()
		return m, nil
	case "g", "home":
		v.rows.top()
		return m, nil
	case "G", "end":
		v.rows.bottom()
		return m, nil
	case "n", "c":
		return m, v.openNew(m)
	case "e":
		return m, v.openRename(m)
	case "d":
		return m, v.askDelete(m)
	case "m":
		return m, v.toggleMembers(m)
	case "a":
		return m, v.startInvite(m)
	case "i":
		return m, v.makeInvite(m)
	case "I":
		return m, v.inbox.show()
	case "J":
		return m, v.openJoin(m)
	case "M":
		// Capital M manages the members; plain tab still changes tabs, so
		// the two never fight over the same key.
		if !v.showMembers || len(v.members) == 0 {
			return m, m.warnf("press m to load the members first")
		}
		m.focus = focusMembers
		v.memberRows.setItems(len(v.members))
		return m, nil
	case "t":
		// Filter the todo list to this list.
		cmd := m.todos.showList(m, v.selectedID())
		m.tab = tabTodos
		return m, cmd
	case "y":
		l := v.selected()
		if l == nil || v.invite == nil {
			return m, nil
		}
		return m, m.copyToClipboard(v.invite.InviteURL, "invite link copied")
	}
	return m, nil
}

// onMemberKey drives the member pane: navigating it, leaving a shared list,
// and an owner removing someone else.
func (v *listsView) onMemberKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		v.memberRows.move(-1)
		return m, nil
	case "down", "j":
		v.memberRows.move(1)
		return m, nil
	case "esc", "M", "tab", "h", "left", "q":
		m.focus = focusList
		return m, nil
	case "d":
		i := v.memberRows.selected()
		if i < 0 || i >= len(v.members) {
			return m, nil
		}
		mem := v.members[i]
		listID := v.membersFor
		// Leaving is always allowed for yourself; removing somebody else
		// is the owner's call, which the server enforces too.
		mine := mem.UserID == m.user.ID
		title := "Remove " + mem.Name() + "?"
		if mine {
			title = "Leave this list?"
		}
		return m, m.ask("remove-member", title,
			fmt.Sprintf("%s will lose access to the list and its todos.", mem.Name()),
			!mine,
			func() tea.Cmd { return removeMember(m.ctx, m.client, listID, mem.UserID, mine) })
	}
	return m, nil
}

func (v *listsView) openNew(m *Model) tea.Cmd {
	f := newForm(formNewList, "New list", m.width)
	f.field("name", "name", "", "e.g. Household")
	return m.openForm(f)
}

func (v *listsView) openRename(m *Model) tea.Cmd {
	l := v.selected()
	if l == nil {
		return nil
	}
	if l.Role != "owner" {
		return m.warnf("only the owner can rename this list")
	}
	f := newForm(formRenameList, "Rename list", m.width)
	f.field("name", "name", l.Name, "")
	return m.openForm(f)
}

func (v *listsView) askDelete(m *Model) tea.Cmd {
	l := v.selected()
	if l == nil {
		return nil
	}
	if l.IsPersonal {
		return m.warnf("the personal list cannot be deleted")
	}
	if l.Role != "owner" {
		return m.warnf("only the owner can delete this list")
	}
	if l.ReadOnly {
		return m.warnf("this list is frozen after the owner left")
	}
	id, name := l.ID, l.Name
	body := fmt.Sprintf("%q and every todo in it will be deleted for all members. This cannot be undone.", name)
	// Offline the list goes from here at once and from the account later,
	// where the service deletes whatever is in it by then. A todo moved out
	// first is moved before the deletion is sent, so it is kept.
	if m.backend != nil {
		if st := m.backend.Status(); st.SignedIn && local.Offline(st.Err) {
			body += " You are offline: it goes from here now and from the account at the next sync. Move out any todo to keep first."
		}
	}
	return m.ask("delete-list", "Delete this list?", body, true,
		func() tea.Cmd { return deleteList(m.ctx, m.client, id) })
}

func (v *listsView) toggleMembers(m *Model) tea.Cmd {
	l := v.selected()
	if l == nil {
		return nil
	}
	if v.showMembers && v.membersFor == l.ID {
		v.showMembers = false
		return nil
	}
	if v.notSynced(l.ID) {
		return m.warnf("%s", local.NotSyncedMessage)
	}
	m.busy = true
	return tea.Batch(m.setBusy("members"), fetchMembers(m.ctx, m.client, l.ID))
}

func (v *listsView) makeInvite(m *Model) tea.Cmd {
	l := v.selected()
	if l == nil {
		return nil
	}
	if l.Role != "owner" {
		return m.warnf("only the owner can invite people")
	}
	if l.ReadOnly {
		return m.warnf("this list is frozen after the owner left")
	}
	if v.notSynced(l.ID) {
		return m.warnf("%s", local.NotSyncedMessage)
	}
	id, name := l.ID, l.Name
	return m.ask("invite", "Create an invite link?",
		fmt.Sprintf("A 7-day link for %q will be shown once and copied to the clipboard.", name), false,
		func() tea.Cmd { return createInvite(m.ctx, m.client, id) })
}

func (v *listsView) openJoin(m *Model) tea.Cmd {
	f := newForm(formJoin, "Join a shared list", m.width)
	f.note("hint", "Paste the invite code or the whole invite URL.")
	f.field("code", "code", "", "https://… or a short code")
	return m.openForm(f)
}

// summary counts the lists for the pane's edge.
func (v *listsView) summary() string {
	shared := 0
	for _, l := range v.items {
		if !l.IsPersonal {
			shared++
		}
	}
	out := plural(len(v.items), "list")
	if shared > 0 {
		out += fmt.Sprintf(" · %d shared", shared)
	}
	if n := v.m.pendingInvitations(); n > 0 {
		return styMuted.Render(out+" · ") + styAccent.Render(plural(n, "invitation")+" (I)")
	}
	return styMuted.Render(out)
}

func (v *listsView) view(width, height int) string {
	m := v.m
	if v.inbox.open {
		return v.inbox.view(width, height)
	}
	v.unsynced = nil
	if m.backend != nil {
		v.unsynced = m.backend.Unsynced()
	}
	if !framed(width, height) {
		return v.rows.render(width, func(i int, sel bool) string { return v.row(i, sel, width-1) })
	}
	l := v.layout(width, height)
	v.rows.setHeight(max(l.leftH-2, 1))
	left := frame(v.rows.render(l.leftW-2, func(i int, sel bool) string {
		return v.row(i, sel, l.leftW-3)
	}), l.leftW, l.leftH, frameOpts{title: "Lists", foot: v.summary(), focused: m.focus != focusMembers})

	title := "List"
	if sel := v.selected(); sel != nil {
		title = sel.Name
	}
	right := frame(v.detailBody(l.detailW-2), l.detailW, l.detailH, frameOpts{title: title, focused: m.focus == focusMembers})
	if l.leftW == width {
		return left + "\n" + right
	}
	return joinPanes(left, right)
}

// listsLayout is how the Lists tab divides the screen: the lists, and the
// selected one's details beside them or, on a narrow terminal, below.
// A terminal too small for frames has room for the lists alone, and the
// details get nothing.
type listsLayout struct {
	leftW, leftH     int
	detailW, detailH int
}

func (v *listsView) layout(width, height int) listsLayout {
	if !framed(width, height) {
		return listsLayout{leftW: width, leftH: height}
	}
	if width < 72 {
		leftH := min(max(len(v.items)+2, 5), max(height/2, 5))
		return listsLayout{leftW: width, leftH: leftH, detailW: width, detailH: height - leftH}
	}
	leftW := min(max(width*2/5, 32), 52)
	return listsLayout{leftW: leftW, leftH: height, detailW: width - leftW, detailH: height}
}

// membersInView reports whether any of the members shown in the details
// would be drawn at this size. A short pane cuts them off the bottom.
func (v *listsView) membersInView(width, height int) bool {
	l := v.layout(width, height)
	if l.detailW <= 0 || l.detailH <= 0 {
		return false
	}
	// frame draws a border only when there is room for one.
	rows, inner := l.detailH, l.detailW
	if l.detailW >= 6 && l.detailH >= 3 {
		rows, inner = l.detailH-2, l.detailW-2
	}
	_, at := v.detailLines(inner)
	return at >= 0 && at < rows
}

func (v *listsView) row(i int, sel bool, width int) string {
	m := v.m
	l := v.items[i]
	base := lipgloss.NewStyle()
	bar := " "
	if sel {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	dot := base.Foreground(tagColor(l.ID)).Render("●")
	role := "member"
	switch {
	case l.ReadOnly:
		dot = base.Foreground(colFaint).Render("⊘")
		role = "frozen"
	case v.unsynced[l.ID]:
		role = "unsynced"
	case l.IsPersonal:
		role = "personal"
	case l.Role == "owner":
		role = "owner"
	}
	count := ""
	if n := m.todos.st.perList[l.ID]; n > 0 {
		count = fmt.Sprintf("%d open", n)
	}
	right := base.Foreground(colMuted).Render(count) + base.Render("  ") + base.Foreground(colFaint).Render(padTo(role, 8))
	nameW := max(width-3-visibleWidth(right)-1, 4)
	name := truncate(l.Name, nameW)
	text := base.Foreground(colFg)
	if sel {
		text = text.Bold(true)
	}
	row := bar + dot + base.Render(" ") + text.Render(name) +
		base.Render(strings.Repeat(" ", max(nameW-visibleWidth(name), 0)+1)) + right
	return row + base.Render(strings.Repeat(" ", max(width-visibleWidth(row), 0)))
}

// detailBody is everything known about the selected list: who it belongs
// to, how much is in it, its members, and the invite link just made.
func (v *listsView) detailBody(width int) string {
	if v.selected() == nil {
		return "\n " + styFaint.Render("no lists yet · n makes one")
	}
	lines, _ := v.detailLines(width)
	return strings.Join(lines, "\n")
}

// detailLines is the selected list's details a line at a time, and the
// line its member rows start on, or -1 when none are shown.
func (v *listsView) detailLines(width int) ([]string, int) {
	m := v.m
	l := v.selected()
	membersAt := -1
	if l == nil {
		return nil, membersAt
	}
	tw := max(width-2, 8)
	var b []string
	add := func(s ...string) { b = append(b, s...) }

	kind := "shared list"
	if l.IsPersonal {
		kind = "your personal list"
	}
	add(" "+lipgloss.NewStyle().Foreground(tagColor(l.ID)).Render("● ")+styBold.Render(truncate(l.Name, tw-2)),
		" "+styFaint.Render(fmt.Sprintf("%s · you are %s · created %s", kind, l.Role, relative(&l.CreatedAt, m.now))))
	if l.CreatedAt.IsZero() {
		b[len(b)-1] = " " + styFaint.Render(fmt.Sprintf("%s · you are %s", kind, l.Role))
	}
	if l.DeletesAt != nil {
		add(" " + styOver.Render("scheduled for deletion "+l.DeletesAt.In(m.loc).Format("2 Jan 2006")))
	}
	if v.unsynced[l.ID] {
		for _, line := range wrapText("Not synced yet. It reaches your account at the next sync, and it can be shared once it has.", tw) {
			add(" " + styWarn.Render(line))
		}
	}
	if l.ReadOnly {
		for _, line := range wrapText("The owner deleted their account. You can still complete todos and tick steps.", tw) {
			add(" " + styWarn.Render(line))
		}
	}

	open, done := 0, 0
	for _, t := range m.todos.snapshot {
		if t.ListID != l.ID {
			continue
		}
		if t.Completed() {
			done++
		} else {
			open++
		}
	}
	add("", " "+label("todos"))
	if open+done == 0 {
		add(" " + styFaint.Render("nothing in this list yet"))
	} else {
		add(" " + meter(done, open+done, min(20, tw)) + styMuted.Render(fmt.Sprintf("  %d open · %d done", open, done)))
	}

	if v.showMembers && v.membersFor == l.ID && len(v.members) > 0 {
		active := m.focus == focusMembers
		hint := "M to manage"
		if active {
			hint = "d leaves or removes"
		}
		add("", " "+label("members")+styFaint.Render("  "+hint))
		membersAt = len(b)
		v.memberRows.setItems(len(v.members))
		add(v.memberRows.render(width, func(i int, sel bool) string {
			return v.memberRow(v.members[i], sel && active, width-1)
		}))
	} else {
		add("", " "+label("members"), " "+styFaint.Render("m shows who is in this list"))
	}

	if v.invite != nil {
		add("", " "+label("invite link · shown once"),
			" "+styOK.Render(truncate(v.invite.InviteURL, tw)),
			" "+styFaint.Render(fmt.Sprintf("valid for %dh · y copies again", v.invite.ExpiresIn/3600)))
	}
	add("", " "+styFaint.Render(shortID(l.ID)))
	return b, membersAt
}

func (v *listsView) memberRow(mem api.Member, sel bool, width int) string {
	base := lipgloss.NewStyle()
	bar := " "
	if sel {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	role := "member"
	switch {
	case mem.Role == "owner":
		role = "owner"
	case mem.UserID == v.m.user.ID:
		role = "you"
	}
	nameW := max(width-10, 8)
	name := truncate(mem.Name(), nameW)
	shown := base.Foreground(colFg).Render(name)
	// Someone with a username is called by it; the masked email after it
	// is how to tell two people with similar names apart.
	if mem.Username != nil && mem.Email != "" && visibleWidth(name)+2 < nameW {
		shown += base.Foreground(colFaint).Render("  " + truncate(mem.Email, nameW-visibleWidth(name)-2))
	}
	row := bar + shown +
		base.Render(strings.Repeat(" ", max(nameW-visibleWidth(shown), 0)+1)) + base.Foreground(colFaint).Render(role)
	return row + base.Render(strings.Repeat(" ", max(width-visibleWidth(row), 0)))
}

func (v *listsView) footer() []helpEntry {
	if v.inbox.open {
		return v.inbox.footer()
	}
	if v.m != nil && v.m.focus == focusMembers {
		return []helpEntry{
			{"j/k", "member"},
			{"d", "leave or remove"},
			{"esc", "back to lists"},
		}
	}
	return []helpEntry{
		{"j/k", "move"},
		{"n", "new list"},
		{"e", "rename"},
		{"d", "delete"},
		{"m", "show members"},
		{"M", "manage members"},
		{"a", "invite"},
		{"i", "invite link"},
		{"I", "invitations"},
		{"J", "join"},
		{"t", "show in todos"},
	}
}
