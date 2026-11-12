package tui

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// accountView shows the profile, portable todos export, and endpoints that need a
// browser session rather than an API token.
type accountView struct {
	m *Model

	sel int

	tokens       []api.APIToken
	channels     []api.Channel
	exported     *api.Export
	exportedPath string
	// exportedHere means the last export was made from the todos on this
	// computer, which hold no channels or tokens to count.
	exportedHere  bool
	exportRefused bool
	imported      *api.ImportResult

	memberCache map[string][]api.Member
}

type accountRow struct {
	key   string
	label string
	// group is a heading drawn above this row. Rows after it belong to it
	// until the next heading, so the menu says where each thing lives.
	group string
	// sessionOnly means the API rejects token auth for this endpoint.
	sessionOnly bool
}

func newAccountView(m *Model) accountView {
	return accountView{m: m, memberCache: map[string][]api.Member{}}
}

// menu is the list of sections; the detail pane renders the selected one.
// It is worked out each time because where Export and Delete account live
// depends on the session, which can change while the tab is open.
func (v *accountView) menu() []accountRow {
	rows := v.baseMenu()
	if v.m.user.SmartParsingAvailable && !v.m.guest() {
		rows = append(rows[:2], append([]accountRow{{key: "smart-dates", label: "Smarter dates"}}, rows[2:]...)...)
	}
	return rows
}

func (v *accountView) baseMenu() []accountRow {
	if v.m.lacksAccountScope() {
		// Certain to fail here, so they sit with the other things only the
		// web app can do rather than offering a button that cannot work.
		return []accountRow{
			{key: "profile", group: "your account", label: "Profile"},
			{key: "timezone", label: "Timezone"},
			{key: "lists", label: "Lists"},
			{key: "export", label: "Export todos"},
			{key: "import", label: "Import todos"},
			{key: "tokens", group: "web app only", label: "API tokens", sessionOnly: true},
			{key: "channels", label: "Notification channels", sessionOnly: true},
			{key: "delete", label: "Delete account", sessionOnly: true},
			{key: "computer", group: "this computer", label: "Connection"},
			{key: "signout", label: "Sign out"},
		}
	}
	return []accountRow{
		{key: "profile", group: "your account", label: "Profile"},
		{key: "timezone", label: "Timezone"},
		{key: "lists", label: "Lists"},
		{key: "export", label: "Export todos"},
		{key: "import", label: "Import todos"},
		{key: "tokens", group: "web app only", label: "API tokens", sessionOnly: true},
		{key: "channels", label: "Notification channels", sessionOnly: true},
		{key: "computer", group: "this computer", label: "Connection"},
		{key: "signout", label: "Sign out"},
		{key: "delete", group: "danger zone", label: "Delete account"},
	}
}

// webAppForAccount is what to tell someone whose session cannot delete
// the account. Only browser sessions and first-party apps hold the
// account scope, and a code sign-in from here is neither.
const webAppForAccount = "delete account needs the web app: this sign-in lacks the account scope"

// exportsFromComputer reports that Export writes the todos kept on this
// computer rather than asking the service: there is no account to ask, or
// the one there is refuses an export to this sign-in.
func (m *Model) exportsFromComputer() bool {
	return m.backend != nil && (!m.backend.Status().SignedIn || m.acct.exportRefused)
}

// lacksAccountScope reports that the session is known to be without the
// account scope. A pasted API token, or a session saved before the scope
// was kept, says nothing, so those still try and fail gracefully.
func (m *Model) lacksAccountScope() bool {
	if m.session == nil {
		return false
	}
	scope := m.session.Scope()
	return scope != "" && !api.HasScope(scope, api.ScopeAccount)
}

func (v *accountView) load() tea.Cmd { return nil }

// selectRow moves the menu to the row with this key, so a shortcut that
// only means something on one row, like the palette's, lands on it.
func (v *accountView) selectRow(key string) {
	for i, r := range v.menu() {
		if r.key == key {
			v.sel = i
			return
		}
	}
}

func (v *accountView) selected() accountRow {
	if v.sel < 0 || v.sel >= len(v.menu()) {
		return v.menu()[0]
	}
	return v.menu()[v.sel]
}

// membersFor returns cached members for a list, if loaded.
func (v *accountView) membersFor(listID string) []api.Member {
	return v.memberCache[listID]
}

func (v *accountView) onKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "up", "k":
		v.sel = max(v.sel-1, 0)
		return m, nil
	case "down", "j":
		v.sel = min(v.sel+1, len(v.menu())-1)
		return m, nil
	case "g", "home":
		v.sel = 0
		return m, nil
	case "G", "end":
		v.sel = len(v.menu()) - 1
		return m, nil
	case "o":
		// The help lists o as signing out on this tab, so it works from any
		// section; it still asks first.
		return m, v.confirmSignOut(m)
	}

	row := v.selected()
	switch row.key {
	case "profile":
		// The profile is where the username lives: it is the one part of
		// it that can be changed here.
		switch k {
		case "enter", "e":
			return m, v.openUsername(m, "", "")
		case "x":
			return m, v.askClearUsername(m)
		}
	case "smart-dates":
		if k == "enter" || k == "space" {
			m.busy = true
			return m, tea.Batch(m.setBusy("saving smarter dates"), saveSmartParsing(m.ctx, m.client, !m.user.SmartParsing))
		}
	case "timezone":
		if k == "enter" || k == "e" {
			return m, v.openTimezone(m)
		}
	case "export":
		switch k {
		case "enter", "e":
			return m, v.openExport(m)
		case "y":
			if v.exported != nil {
				return m, m.copyToClipboard(v.exportedPath, "export path copied")
			}
		}
	case "tokens":
		switch k {
		case "enter":
			m.busy = true
			return m, tea.Batch(m.setBusy("listing tokens"), fetchTokens(m.ctx, m.client))
		case "n":
			return m, m.warnf("minting tokens needs a browser session; the API rejects token auth with 403")
		}
	case "channels":
		switch k {
		case "enter":
			m.busy = true
			return m, tea.Batch(m.setBusy("listing channels"), fetchChannels(m.ctx, m.client))
		case "n":
			return m, m.warnf("adding channels needs a browser session; the API rejects token auth with 403")
		}
	case "delete":
		if k == "enter" || k == "d" {
			if m.lacksAccountScope() {
				// Before the email is asked for, which would only
				// lead to the same refusal.
				return m, m.warnf("%s", webAppForAccount)
			}
			return m, v.openDeleteAccount(m)
		}
	case "signout":
		if k == "enter" {
			return m, v.confirmSignOut(m)
		}
	case "computer":
		switch k {
		case "y":
			cfg, err := m.cfgPath()
			if err != nil {
				return m, nil
			}
			return m, m.copyToClipboard(cfg, "config path copied")
		case "c":
			return m, v.openChangeToken(m)
		}
	case "import":
		if k == "enter" {
			f := newForm(formImport, "Import todos", m.width)
			f.note("hint", "Preview an Evado export before importing. Existing todos and pending deletions are kept. Sharing, API tokens and notification settings are not restored.")
			if m.backend == nil || m.backend.Status().SignedIn {
				f.note("sync", "The preview and import go directly to this account.")
			}
			f.field("path", "export file", "", "path to an Evado JSON export")
			return m, m.openForm(f)
		}
	case "lists":
		if k == "enter" {
			m.tab = tabLists
			m.busy = true
			return m, tea.Batch(m.setBusy(""), m.listsV.load())
		}
	}
	return m, nil
}

// usernameRules is what the service accepts, said where it is typed.
const usernameRules = "How people find you to share a list, written @name. 3 to 20 letters, numbers and single " +
	"underscores between them, with at least one letter. Capitals and a leading @ are dropped for you. " +
	"It is never a login. Leave it empty to remove it."

// openUsername asks for a username. typed and problem bring the form back
// after a refusal, with what was typed and why it was turned down.
func (v *accountView) openUsername(m *Model, typed, problem string) tea.Cmd {
	if m.guest() {
		return m.warnf("a username needs an Evado account; sign in from Account, Connection first")
	}
	if m.user.ID == "" {
		return m.warnf("load the account first by pressing r")
	}
	f := newForm(formUsername, "Username", m.width)
	if problem != "" {
		f.alert(problem)
	}
	f.note("hint", usernameRules)
	if typed == "" && problem == "" && m.user.Username != nil {
		typed = *m.user.Username
	}
	f.field("username", "username", typed, "sam")
	return m.openForm(f)
}

// submitUsername saves what the form holds. Nothing in it touches the
// timezone: only the username is sent.
func (v *accountView) submitUsername(m *Model, typed string) tea.Cmd {
	name := strings.TrimSpace(typed)
	switch {
	case name == "":
		return v.askClearUsername(m)
	case m.user.Username != nil && name == *m.user.Username:
		return m.okf("your username is still %s", api.Handle(m.user.Username))
	}
	m.busy = true
	return tea.Batch(m.setBusy("saving username"), saveUsername(m.ctx, m.client, &name))
}

func (v *accountView) askClearUsername(m *Model) tea.Cmd {
	switch {
	case m.guest():
		return m.warnf("a username needs an Evado account; sign in from Account, Connection first")
	case m.user.Username == nil:
		return m.warnf("you have no username to remove")
	}
	return m.ask("clear-username", "Remove your username?",
		fmt.Sprintf("Nobody can invite you by %s any more. Your email, your lists and your todos stay as they are. "+
			"You can choose a username again later, and it may be taken by then.", api.Handle(m.user.Username)),
		false,
		func() tea.Cmd { return saveUsername(m.ctx, m.client, nil) })
}

// onUsername takes in the outcome of setting or clearing the username. A
// refusal brings the form back with what was typed.
func (v *accountView) onUsername(msg usernameMsg) tea.Cmd {
	m := v.m
	m.done()
	if msg.err != nil {
		if api.CodeOf(msg.err) == api.CodeUnauthorized {
			return m.failf(msg.err, "")
		}
		problem := usernameProblem(msg.err)
		if msg.username == nil || m.layer != layerNone {
			return toastCmd(toastError, "%s", problem)
		}
		return v.openUsername(m, *msg.username, problem)
	}
	m.user.Username = msg.user.Username
	// Wherever this account is named, it is now called something else.
	v.renameSelf()
	var said tea.Cmd
	if msg.user.Username == nil {
		said = m.okf("username removed")
	} else {
		said = m.okf("your username is now %s", api.Handle(msg.user.Username))
	}
	// The service is asked again, so every view shows what it now holds
	// rather than what this edit assumed.
	return tea.Batch(said, m.fetchMe())
}

// renameSelf puts the account's username on its own entry in every member
// list the views keep.
func (v *accountView) renameSelf() {
	m := v.m
	for _, ms := range v.memberCache {
		for i := range ms {
			if ms[i].UserID == m.user.ID {
				ms[i].Username = m.user.Username
			}
		}
	}
	for i := range m.listsV.members {
		if m.listsV.members[i].UserID == m.user.ID {
			m.listsV.members[i].Username = m.user.Username
		}
	}
}

// usernameProblem says why the username was refused, in the service's
// words, which know the rule that was broken.
func usernameProblem(err error) string {
	var apiErr *api.Error
	switch {
	case unreachable(err):
		return "Changing your username needs a connection. Your text is kept; try again when you are online."
	case !errors.As(err, &apiErr):
		return errText(err)
	case apiErr.Message != "" && apiErr.Message != http.StatusText(apiErr.StatusCode):
		return apiErr.Message
	}
	switch apiErr.Code {
	case api.CodeUsernameInvalid:
		return "That username breaks the rules: 3 to 20 letters, numbers and single underscores, with at least one letter."
	case api.CodeUsernameReserved:
		return "That username is reserved. Choose another."
	case api.CodeUsernameTaken:
		return "Somebody already has that username."
	}
	return errText(err)
}

func (v *accountView) openTimezone(m *Model) tea.Cmd {
	f := newForm(formTimezone, "Timezone", m.width)
	f.note("hint", "The server parses natural-language dates in this zone. Use an IANA name such as Europe/Madrid.")
	f.field("tz", "timezone", m.user.TZ, "")
	return m.openForm(f)
}

func (v *accountView) openExport(m *Model) tea.Cmd {
	f := newForm(formExport, "Export todos", m.width)
	if m.exportsFromComputer() {
		f.title = "Export this computer's todos"
		f.note("hint", "Saves portable lists, todos and checklists from this computer. Full account data, including channels and token metadata, comes from the web app.")
	} else {
		f.note("hint", "Saves portable lists, todos and checklists from your account. Needs todos.read. Full account data comes from the web app.")
	}
	dir, _ := dataDir()
	f.field("path", "save to", filepath.Join(dir, "evado-export.json"), "leave empty to preview only")
	return m.openForm(f)
}

func (v *accountView) openChangeToken(m *Model) tea.Cmd {
	if m.cfg.Demo {
		return m.warnf("demo stays offline; restart without -demo to sign in")
	}
	m.layer = layerLogin
	m.login = m.newLogin(true)
	return textinput.Blink
}

// confirmSignOut asks first, then forgets this computer's copy of the
// account. Nothing on the server changes.
func (v *accountView) confirmSignOut(m *Model) tea.Cmd {
	if m.cfg.Demo {
		return m.warnf("quit to discard this demo; restart with -demo for fresh samples")
	}
	body := "This removes the saved sign-in, the todos kept here and your last view from this computer, " +
		"and ends the session on the server if you signed in with an emailed code. " +
		"Your account and todos on the server are not touched, and neither are your settings and saved views. " +
		"The sign-in screen opens next."
	danger := false
	if m.backend != nil {
		// What is only on this computer goes for good, so say how much.
		switch st := m.backend.Status(); {
		case !st.SignedIn && !st.Detached && st.Todos > 0:
			body = fmt.Sprintf("This computer is not signed in, so its %s exist nowhere else. "+
				"Starting over deletes them for good. To keep them, sign in first from Connection.", plural(st.Todos, "todo"))
			danger = true
		case st.Pending+st.Refused > 0:
			body = fmt.Sprintf("%s here have not reached Evado yet. Signing out tries to send them first; "+
				"if the server cannot be reached they are lost. ", plural(st.Pending+st.Refused, "change")) + body
			danger = true
		}
	}
	return m.ask("sign-out", "Sign out of this computer?", body, danger,
		func() tea.Cmd { return signOut(m.ctx, m.cfg, m.backend, m.session) })
}

func (v *accountView) openDeleteAccount(m *Model) tea.Cmd {
	if m.user.Email == "" {
		return m.warnf("load the account first by pressing r")
	}
	f := newForm(formDeleteAcct, "Delete account", m.width)
	f.note("warning", "This permanently deletes the account, its personal list, and every list it is the only member of. Shared lists it owns stay for their members as read-only.")
	f.field("confirmEmail", "type "+m.user.Email, "", "must match exactly")
	return m.openForm(f)
}

func (v *accountView) onExport(msg exportMsg) tea.Cmd {
	m := v.m
	if msg.err != nil {
		if api.CodeOf(msg.err) == api.CodeInsufficientScope && m.backend != nil {
			v.exportRefused = true
			cmd := v.openExport(m)
			m.form.alert("The server refused a todos export. You can export the lists and todos kept on this computer instead.")
			return cmd
		}
		return m.failf(msg.err, "export")
	}
	v.exported = &msg.export
	v.exportedHere = msg.local
	if msg.path != "" {
		v.exportedPath = msg.path
		// No automatic copy: its toast, which differs by clipboard tool,
		// would replace the receipt. y copies the path when wanted.
		return m.okf("export written to %s: %s, %s (y copies the path)",
			msg.path, plural(len(msg.export.Todos), "todo"), plural(len(msg.export.Lists), "list"))
	}
	return m.okf("export preview: %d todos, %d lists", len(msg.export.Todos), len(msg.export.Lists))
}

func (v *accountView) onTokens(msg tokensMsg) tea.Cmd {
	if msg.err != nil {
		return v.m.warnf("%s", errText(msg.err))
	}
	v.tokens = msg.tokens
	if len(msg.tokens) == 0 {
		return v.m.warnf("no active tokens, or this endpoint needs a browser session")
	}
	return v.m.okf("%d token(s)", len(msg.tokens))
}

func (v *accountView) onChannels(msg channelsMsg) tea.Cmd {
	if msg.err != nil {
		return v.m.warnf("%s", errText(msg.err))
	}
	v.channels = msg.channels
	if len(msg.channels) == 0 {
		return v.m.warnf("no channels, or this endpoint needs a browser session")
	}
	return v.m.okf("%d channel(s)", len(msg.channels))
}

func (v *accountView) view(width, height int) string {
	m := v.m
	menuW := 28
	boxed := framed(width, height) && width >= menuW+24
	innerH := height
	if boxed {
		innerH = height - 2
	}
	var lines []string
	selLine := 0
	for i, row := range v.menu() {
		if row.group != "" {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, " "+label(row.group))
		}
		base := lipgloss.NewStyle().Foreground(colMuted)
		bar := " "
		switch {
		case i == v.sel:
			base = lipgloss.NewStyle().Foreground(colFg).Background(colPanel).Bold(true)
			bar = base.Foreground(colAccent).Render("▌")
			selLine = len(lines)
		case row.sessionOnly:
			base = lipgloss.NewStyle().Foreground(colFaint)
		case row.key == "delete":
			base = lipgloss.NewStyle().Foreground(colRed)
		}
		lines = append(lines, bar+base.Render(padTo(" "+truncate(row.label, menuW-6), menuW-3)))
	}
	// On a short terminal the menu scrolls so the selected row stays in view.
	if innerH > 0 && len(lines) > innerH {
		start := min(max(selLine-innerH/2, 0), len(lines)-innerH)
		lines = lines[start : start+innerH]
	}
	if !boxed {
		rows := lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(menuW).Render(strings.Join(lines, "\n")),
			v.detailView(max(width-menuW-1, 20)),
		)
		return lipgloss.NewStyle().MaxHeight(max(height, 1)).Render(rows)
	}
	who := m.user.Email
	if who == "" {
		who = "not loaded"
	}
	detailW := width - menuW
	detail := lipgloss.NewStyle().Padding(1, 2).Render(v.detailView(max(detailW-6, 20)))
	return joinPanes(
		frame(strings.Join(lines, "\n"), menuW, height, frameOpts{title: "Account", focused: true}),
		frame(detail, detailW, height, frameOpts{title: v.selected().label, foot: styMuted.Render(who)}),
	)
}

func (v *accountView) detailView(width int) string {
	m := v.m
	var b strings.Builder
	switch v.selected().key {
	case "profile":
		b.WriteString(v.profileView(m, width))
	case "smart-dates":
		state := "off"
		if m.user.SmartParsing {
			state = "on"
		}
		b.WriteString("Smarter dates: " + state + "\n\n" + paneNote(smartDatesExplanation, width) + "\n\n" + paneNote(smartDatesDisclosure, width) + "\n\n" + styFaint.Render("enter toggles it"))
	case "timezone":
		b.WriteString(section("timezone", []kv{
			{"current", m.user.TZ},
			{"local", localZoneName(m)},
			{"note", "dates in todos are parsed here"},
		}, width))
		b.WriteString("\n" + styFaint.Render("enter changes it"))
	case "lists":
		b.WriteString(v.listsView(m, width))
	case "export":
		b.WriteString(v.exportView(m, width))
	case "tokens":
		b.WriteString(v.tokensView(m, width))
	case "channels":
		b.WriteString(v.channelsView(m, width))
	case "delete":
		b.WriteString(v.deleteView(width))
	case "signout":
		b.WriteString(v.signOutView(width))
	case "computer":
		b.WriteString(v.computerView(m, width))
	case "import":
		b.WriteString(paneNote("Import an Evado export. Press enter to choose its file and preview. Existing todos and pending deletions are kept.", width))
		if v.imported != nil {
			b.WriteString("\n\n" + paneNote(importSummary(*v.imported), width))
			for _, refusal := range v.imported.Refused {
				b.WriteString("\n" + paneNote(importRefusalName(refusal)+": "+refusal.Reason, width))
			}
		}
	}
	return b.String()
}

type kv struct{ k, v string }

func section(title string, rows []kv, width int) string {
	var b strings.Builder
	b.WriteString(label(title) + "\n\n")
	for _, r := range rows {
		b.WriteString(pad(styFaint.Render(r.k), 10) + lipgloss.NewStyle().Foreground(colFg).Render(truncate(r.v, max(width-10, 10))) + "\n")
	}
	return b.String()
}

func (v *accountView) profileView(m *Model, width int) string {
	username := "not set"
	if h := api.Handle(m.user.Username); h != "" {
		username = h
	}
	rows := section("profile", []kv{
		{"id", m.user.ID},
		{"email", m.user.Email},
		{"username", username},
		{"timezone", m.user.TZ},
		{"joined", formatStamp(&m.user.CreatedAt, m.loc)},
		{"lists", fmt.Sprintf("%d", len(m.lists))},
		{"todos", fmt.Sprintf("%d loaded in the todos view", len(m.todos.items))},
	}, width)
	return rows + "\n" + paneNote("Other people see your username in member lists and when a todo is assigned to you; "+
		"without one they see your email with part of it hidden. "+usernameRules, width) +
		"\n\n" + styFaint.Render("enter sets your username · x removes it")
}

func (v *accountView) listsView(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(label("lists") + "\n\n")
	for _, l := range m.lists {
		mark := "  "
		if l.IsPersonal {
			mark = styFaint.Render("● ")
		}
		b.WriteString("  " + mark + padTo(truncate(l.Name, max(width-26, 10)), max(width-26, 10)) + " " + styFaint.Render(l.Role))
		if l.ReadOnly {
			b.WriteString(" " + styWarn.Render("frozen"))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (v *accountView) exportView(m *Model, width int) string {
	var b strings.Builder
	if m.exportsFromComputer() && v.exported == nil {
		return v.computerExportView(m, width)
	}
	if v.exported == nil {
		b.WriteString(section("export", []kv{
			{"scope", "todos.read"},
			{"includes", "portable todos, notes, checklists and lists"},
			{"full data", "export your data from the web app: Settings"},
		}, width))
		b.WriteString("\n" + styFaint.Render("enter picks a path, esc cancels"))
		return b.String()
	}
	ex := v.exported
	rows := []kv{
		{"taken", formatStamp(&ex.ExportedAt, m.loc)},
		{"todos", fmt.Sprintf("%d", len(ex.Todos))},
		{"lists", fmt.Sprintf("%d", len(ex.Lists))},
	}
	rows = append(rows, kv{"file", v.exportedPath})
	b.WriteString(section("last export", rows, width))
	return b.String()
}

// computerExportView says what Export will write when it comes from this
// computer, and what it leaves out.
func (v *accountView) computerExportView(m *Model, width int) string {
	return section("this computer", []kv{
		{"includes", "lists and todos with their checklists, as kept here"},
		{"omits", "deleted todos, channels and token metadata"},
	}, width) + "\n" + styFaint.Render("enter picks a path, esc cancels")
}

func (v *accountView) tokensView(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(section("api tokens", []kv{
		{"auth", "token auth is rejected here (403 session_required)"},
		{"hint", "manage these in the web app: Settings, API & apps"},
	}, width))
	if len(v.tokens) == 0 {
		return b.String()
	}
	b.WriteString("\n")
	for _, t := range v.tokens {
		last := "never used"
		if t.LastUsedAt != nil {
			last = relative(t.LastUsedAt, m.now)
		}
		b.WriteString("  " + padTo(truncate(t.Name, max(width-22, 10)), max(width-22, 10)) +
			styFaint.Render(shortID(t.ID)+"  "+last) + "\n")
	}
	return b.String()
}

func (v *accountView) channelsView(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(section("notification channels", []kv{
		{"auth", "token auth is rejected here (403 session_required)"},
		{"hint", "manage these in the web app settings"},
	}, width))
	if len(v.channels) == 0 {
		return b.String()
	}
	b.WriteString("\n")
	for _, c := range v.channels {
		state := styFaint.Render("disabled")
		if c.Enabled {
			state = styOK.Render("enabled")
		}
		b.WriteString("  " + padTo(truncate(c.Label, max(width-22, 10)), max(width-22, 10)) +
			styMuted.Render(c.Type) + "  " + state + "\n")
	}
	return b.String()
}

func (v *accountView) computerView(m *Model, width int) string {
	var rows []kv
	note := "This is what the app saved on this computer. c signs in again, with your email or a token, and y copies the settings path."
	if m.backend == nil {
		rows = []kv{{"server", m.serverURL()}, m.signInRow(), {"todos", "kept on the server only"}}
	} else {
		st := m.backend.Status()
		switch {
		case st.SignedIn:
			synced := "not yet"
			if st.LastSync != nil {
				synced = relative(st.LastSync, m.now)
			}
			if st.Err != nil && local.Offline(st.Err) {
				synced += ", offline now"
			}
			rows = []kv{
				{"server", m.serverURL()},
				m.signInRow(),
				{"synced", synced},
				{"to send", plural(st.Pending, "change")},
			}
			if st.Refused > 0 {
				rows = append(rows, kv{"refused", plural(st.Refused, "change") + "; edit them to try again"})
			}
		case st.Detached:
			rows = []kv{{"account", "signed out: the token was rejected"}, {"to send", plural(st.Pending, "change")}}
			note = "The todos are still here. c signs in again and syncing carries on where it stopped."
		default:
			rows = []kv{{"account", "none: the todos are on this computer only"}, {"todos", plural(st.Todos, "todo")}}
			note = "c signs in with your email or an Evado token. The todos here are then added to the account and kept in sync."
		}
		if path, err := local.Path(); err == nil {
			rows = append(rows, kv{"kept in", path})
		}
	}
	if path, err := m.cfgPath(); err == nil {
		rows = append(rows, kv{"settings", path})
	}
	return section("this computer", rows, width) + "\n" + paneNote(note, width)
}

// paneNote wraps a note for a detail pane of the given width. wrapBlock
// indents what it wraps by two cells, so it wraps two short of the pane: a
// line any wider makes the frame clip every row of the pane.
func paneNote(s string, width int) string { return wrapBlock(s, max(width-2, 10)) }

func (v *accountView) signOutView(width int) string {
	return label("sign out") + "\n\n" +
		paneNote("Removes this account from this computer, so you can start fresh or sign in as someone else.", width) + "\n\n" +
		paneNote("Removed: the saved token or session, the todos kept here and your last view. "+
			"A session from an emailed code is ended on the server too.", width) + "\n" +
		paneNote("Kept: your account and todos on the server, and your settings such as theme, keys and saved views.", width) + "\n\n" +
		styFaint.Render("enter signs out after you confirm")
}

func (v *accountView) deleteView(width int) string {
	if v.m.lacksAccountScope() {
		return label("delete account") + "\n\n" +
			paneNote("This sign-in lacks the account scope, so the service refuses to delete the account (403 insufficient_scope).", width) + "\n\n" +
			paneNote("Delete the account from the web app: Settings. To only remove Evado from this computer, choose Sign out.", width)
	}
	return label("delete account") + "\n\n" +
		paneNote("Deletes your Evado account on the server, for every device. It cannot be undone. "+
			"Your personal list goes with it, and shared lists you own stay for their members as read-only.", width) + "\n\n" +
		paneNote("To only remove Evado from this computer, choose Sign out instead.", width) + "\n\n" +
		styErr.Render("enter starts it, then you retype your email")
}

// maskToken shows only enough of a token to recognise it.
func maskToken(t string) string {
	if len(t) <= 10 {
		return strings.Repeat("*", len(t))
	}
	return t[:6] + strings.Repeat("*", 8) + t[len(t)-4:]
}

// localZoneName is the machine's zone, offered as a hint when changing
// the account timezone.
func localZoneName(m *Model) string {
	if name, offset := time.Now().In(m.loc).Zone(); name != "" {
		_ = offset
		return name
	}
	return m.loc.String()
}

func (v *accountView) footer() []helpEntry {
	if k := v.selected().key; k == "delete" && v.m.lacksAccountScope() {
		return []helpEntry{{"j/k", "section"}}
	}
	switch v.selected().key {
	case "tokens", "channels":
		return []helpEntry{
			{"j/k", "section"},
			{"enter", "fetch"},
			{"n", "add"},
		}
	case "export":
		return []helpEntry{{"enter", "export"}, {"y", "copy path"}}
	case "delete":
		return []helpEntry{{"enter", "delete account"}}
	case "signout":
		return []helpEntry{{"enter", "sign out"}}
	case "computer":
		return []helpEntry{{"c", "sign in again"}, {"y", "copy settings path"}}
	case "profile":
		return []helpEntry{{"j/k", "section"}, {"enter", "set username"}, {"x", "remove username"}}
	default:
		return []helpEntry{{"j/k", "section"}, {"enter", "open"}}
	}
}

const smartDatesExplanation = "Helps with dates and times that plain rules miss. \"Visit Tomorrow Land\" keeps its name. \"Dentist at 9\" means morning, and \"dinner at 7\" means evening."

const smartDatesDisclosure = "Off by default. When enabled, todo text containing a date or time goes to a decision model, along with the day and time you wrote it. Nothing else about you is sent. When disabled, nothing is sent to the model."
