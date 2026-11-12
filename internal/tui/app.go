package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/auth"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// tab identifies the top-level views.
type tab int

const (
	tabTodos tab = iota
	tabLists
	tabSync
	tabAccount
	numTabs
)

var tabNames = [numTabs]string{"Todos", "Lists", "Sync", "Account"}

// focus is which pane owns the keyboard.
type focus int

const (
	focusList focus = iota
	focusDetail
	focusChecklist
	focusMembers
	focusSidebar
)

// layer is the top-most interactive element.
type layer int

const (
	layerNone layer = iota
	layerForm
	layerConfirm
	layerHelp
	layerSearch
	layerLogin
	layerPalette
	layerThemes
)

// Model is the root TUI model.
type Model struct {
	ctx    context.Context
	client *api.Client
	cfg    config.Config

	// backend keeps the todos on this computer and syncs them. It is nil
	// when the config asks for nothing to be kept here, and the client
	// then talks straight to the server.
	backend *local.Backend
	// startupErr is why the todos kept here could not be opened.
	startupErr error
	// session is the sign-in an emailed code started, when that is how
	// this computer is signed in, and sessionErr why the kept one could
	// not be read. joining is a sign-in from the sign-in screen still
	// under way.
	session    *auth.Session
	sessionErr error
	joining    *joining
	// syncQueued says a sync is waiting or running, and syncRetry holds
	// the next one back after a failure, so a dead network is not hammered.
	syncQueued bool
	// signingIn says the sign-in under way came from the sign-in screen,
	// rather than from a token that was already saved.
	signingIn   bool
	syncRetry   time.Time
	syncBackoff time.Duration
	// diskSeen is how many of the todos file's changes by other copies of
	// evado the views have caught up with.
	diskSeen uint64

	width  int
	height int

	tab    tab
	focus  focus
	layer  layer
	now    time.Time
	loc    *time.Location
	ready  bool
	quited bool

	user  api.User
	lists []api.List
	// meSeq numbers the requests for the account, so an answer that took
	// longer than a newer one is not shown over it. Answers numbered below
	// meFloor were asked for under a sign-in that has since ended.
	meSeq   int
	meFloor int

	// invite is the invitation being written, kept so a refusal can bring
	// its form back with what was typed.
	invite *inviteDraft

	busy      bool
	busyLabel string

	// previewFor is the title the new-todo form last asked the server to
	// read, and previewSeq numbers those requests.
	previewFor     string
	previewSeq     int
	captureSmart   *bool
	capturePreview *api.ParsePreview

	// frame counts animation ticks, and framing says one is on its way, so
	// the animation clock only runs while something is moving.
	frame   int
	framing bool

	toast    *toast
	toastAge int
	// toastQueue holds the warnings of a sync that are waiting their turn
	// behind the one on screen.
	toastQueue []toast

	// undos are the actions z can take back, in the order they were taken,
	// newest last. undoing is the one being taken back while its request
	// runs.
	undos   []undoEntry
	undoing int
	// muts sends the changes to each todo one at a time, in order.
	muts mutations
	// lastClick is the previous mouse click, to tell a double click.
	lastClick clickAt

	form    *form
	conf    *confirm
	pending func() tea.Cmd
	// declined runs when a confirmation is turned down, for the few
	// questions where saying no is itself a decision to act on.
	declined func() tea.Cmd
	help     *help
	palette  *palette
	themes   *themePicker
	search   textinput.Model
	login    *loginView

	// Per-view state.
	todos  todosView
	listsV listsView
	syncV  syncView
	acct   accountView
}

// New builds the root model. Passing an empty token sends the user through
// the token prompt first.
func New(ctx context.Context, cfg config.Config) *Model {
	applyTheme(cfg.Theme)
	ti := newInput()
	ti.Prompt = "search: "
	ti.Placeholder = "title or notes"
	ti.CharLimit = 200
	ti.Width = 32
	ti.Cursor.BlinkSpeed = blinkSpeed
	ti.Focus()

	loc, err := time.LoadLocation("UTC")
	if err != nil {
		loc = time.UTC
	}

	m := &Model{
		ctx:    ctx,
		cfg:    cfg,
		now:    time.Now(),
		loc:    loc,
		search: ti,
		help:   newHelp(keyReference()),
	}
	m.openSession()
	m.client = directClient(m.remote())
	if !cfg.NoCache {
		m.openBackend()
	}
	m.todos = newTodosView(m)
	m.todos.restore(cfg)
	m.listsV = newListsView(m)
	m.syncV = newSyncView(m)
	m.acct = newAccountView(m)
	if m.needsLogin() {
		m.layer = layerLogin
		m.login = m.newLogin(false)
	}
	return m
}

// openBackend opens the todos kept on this computer. If the file cannot be
// read the TUI carries on against the server alone and says why, rather
// than guess at a damaged file.
func (m *Model) openBackend() {
	path, err := local.Path()
	if err == nil {
		m.backend, err = local.OpenRemote(path, m.remote())
	}
	if err != nil {
		m.backend, m.startupErr = nil, err
		return
	}
	m.client = m.backend.Client()
	removeLegacyCache()
}

// needsLogin reports whether there is nothing to show until the user
// signs in or chooses to go without an account.
func (m *Model) needsLogin() bool {
	if m.backend != nil {
		return m.cfg.Token == "" && m.session == nil && !m.cfg.Local
	}
	return !m.hasAccount()
}

// newLogin builds the sign-in screen. inApp means it was opened from a
// running session, so esc goes back instead of quitting.
func (m *Model) newLogin(inApp bool) *loginView {
	l := newLoginView(api.DefaultBaseURL, m.cfg.BaseURL)
	l.allowLocal = m.backend != nil && !inApp
	l.inApp = inApp
	return l
}

// serverURL is the Evado server this session belongs to.
func (m *Model) serverURL() string {
	if m.backend == nil {
		return m.client.BaseURL()
	}
	if m.cfg.BaseURL != "" {
		return m.cfg.BaseURL
	}
	return api.DefaultBaseURL
}

// removeLegacyCache deletes the snapshot older versions kept next to the
// config. It held todo titles, and nothing reads it any more.
func removeLegacyCache() {
	if p, err := config.Path(); err == nil {
		_ = os.Remove(filepath.Join(filepath.Dir(p), "cache.json"))
	}
}

// Init loads the account and the first view.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), pollCmd(m.cfg.PollInterval())}
	if m.startupErr != nil {
		cmds = append(cmds, m.warnf("the todos kept on this computer could not be opened: %s", errText(m.startupErr)))
	}
	if m.sessionErr != nil {
		cmds = append(cmds, m.warnf("the saved sign-in could not be read: %s", errText(m.sessionErr)))
	}
	if m.needsLogin() {
		return tea.Batch(cmds...)
	}
	m.busy = true
	if m.backend != nil && m.backend.Status().NeedsSignIn {
		// A token with no todos tied to it yet: a first run, or an upgrade.
		return tea.Batch(append(cmds, m.setBusy("signing in"), m.signIn(false), m.animate())...)
	}
	return tea.Batch(append(cmds, m.refreshAll(false), m.syncNow(), m.animate())...)
}

// inDetail reports whether a todo is open, in its detail or its checklist.
func (m *Model) inDetail() bool {
	return m.focus == focusDetail || m.focus == focusChecklist
}

// frameInterval paces the animations. Tests set it to zero, which stops
// the animation clock from running at all.
var frameInterval = 50 * time.Millisecond

// frameMsg is one tick of the animation clock.
type frameMsg time.Time

// animate starts the animation clock when something on screen is moving
// and the clock is not already running. It stops by itself: a tick that
// finds nothing moving does not schedule another.
func (m *Model) animate() tea.Cmd {
	moving := m.busy || len(m.todos.anims) > 0 || m.syncQueued
	if m.framing || !moving || frameInterval <= 0 {
		return nil
	}
	m.framing = true
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// -- helpers ----------------------------------------------------------------

// setBusy marks a request in flight. It does not start a tick of its own:
// Init starts the one clock that runs for the life of the program, and a
// second chain would double the tick rate every time it was called.
func (m *Model) setBusy(label string) tea.Cmd {
	m.busy = true
	m.busyLabel = label
	return nil
}

func (m *Model) done() {
	m.busy = false
	m.busyLabel = ""
}

// warnEach shows the warnings of one sync: the first now, and the rest in
// turn as each expires. They are not replaced by one another the way a
// reply to a key press is, because a warning may be the only notice that an
// edit was lost.
func (m *Model) warnEach(texts []string) {
	for i, text := range texts {
		t := toast{kind: toastWarn, text: text, lasting: true}
		if i == 0 {
			m.toast, m.toastAge = &t, 0
			continue
		}
		m.toastQueue = append(m.toastQueue, t)
	}
}

// nextToast is the message that follows an expired one: the oldest of those
// that waited, or none.
func (m *Model) nextToast() *toast {
	if len(m.toastQueue) == 0 {
		return nil
	}
	t := m.toastQueue[0]
	m.toastQueue = m.toastQueue[1:]
	m.toastAge = 0
	return &t
}

// holdToasts puts a sync warning on screen back at the head of the queue
// while the sign-in screen is up, as that screen shows no toasts.
func (m *Model) holdToasts() {
	if m.layer != layerLogin || m.toast == nil || !m.toast.lasting {
		return
	}
	m.toastQueue = append([]toast{*m.toast}, m.toastQueue...)
	m.toast = nil
}

// okf shows a success toast.
func (m *Model) okf(format string, args ...any) tea.Cmd {
	return toastCmd(toastOK, format, args...)
}

// warnf shows a warning toast.
func (m *Model) warnf(format string, args ...any) tea.Cmd {
	return toastCmd(toastWarn, format, args...)
}

// failf shows an error toast, and explains the ones users hit often.
func (m *Model) failf(err error, context string) tea.Cmd {
	switch api.CodeOf(err) {
	case api.CodeUnauthorized:
		m.reauthenticate()
		return nil
	case api.CodeRateLimited:
		return toastCmd(toastError, "rate limited by the API; slow down for a moment")
	case api.CodeInsufficientScope:
		// The one place that says what to do about it, for any call that
		// needs a scope this sign-in was not given.
		if context != "" {
			return toastCmd(toastError, "%s: use the web app, this sign-in lacks the account scope", context)
		}
		return toastCmd(toastError, "use the web app, this sign-in lacks the account scope")
	}
	if context != "" {
		return toastCmd(toastError, "%s: %s", context, err)
	}
	return toastCmd(toastError, "%s", err)
}

// reauthenticate forgets a rejected token or an ended session and sends
// the user back to the sign-in screen, instead of leaving them on a screen
// where every request fails. Several requests can be rejected at once, so
// a prompt that is already open is left alone rather than reset under the
// user's typing.
func (m *Model) reauthenticate() {
	if m.layer == layerLogin {
		return
	}
	email := ""
	if m.session != nil {
		email = m.session.Email()
	}
	m.forgetSession()
	stale := m.cfg.Token != ""
	m.cfg.Token = ""
	saveErr := m.cfg.Save()
	m.outdateMe(m.meSeq)
	m.done()
	m.form, m.conf, m.pending, m.declined = nil, nil, nil, nil
	m.focus = focusList
	// The todos kept here stay; only the link to the server is dropped,
	// until a token that works is pasted.
	inApp := m.backend != nil
	if inApp {
		m.backend.Disconnect()
	}
	m.login = m.newLogin(inApp)
	m.login.err = "That token was rejected. Paste a new one."
	if email != "" {
		m.login.fill(email)
		m.login.err = "Your session has ended. Sign in again."
	}
	if stale && saveErr != nil {
		// Otherwise the next start signs in with the same token, and is
		// turned down again, with no word of why it came back.
		m.login.err += " The config file could not be saved, so the rejected token is still in it: " + errText(saveErr)
	}
	m.layer = layerLogin
}

// errText shortens an error for a status line.
func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", " ")
	return truncate(msg, 100)
}

// refreshAll refreshes the account, lists and the current view. With full
// set it reloads everything from the server; without, the todos catch up
// through the change feed when they can.
func (m *Model) refreshAll(full bool) tea.Cmd {
	return tea.Batch(m.fetchMe(), m.refreshCurrentTab(full))
}

// fetchMe reads the account and its lists.
func (m *Model) fetchMe() tea.Cmd {
	m.meSeq++
	return fetchMe(m.ctx, m.client, m.meSeq)
}

// saveTimezone changes the account's timezone and reads the account back.
func (m *Model) saveTimezone(tz string) tea.Cmd {
	m.meSeq++
	return setTimezone(m.ctx, m.client, tz, m.meSeq)
}

// outdateMe sets aside the answers to every account request numbered up to
// seq, which were made under a sign-in that is over.
func (m *Model) outdateMe(seq int) {
	m.meSeq = seq + 1
	m.meFloor = m.meSeq
}

func (m *Model) refreshCurrentTab(full bool) tea.Cmd {
	switch m.tab {
	case tabTodos:
		if full {
			return m.todos.load()
		}
		return m.todos.refresh()
	case tabLists:
		return m.listsV.load()
	case tabSync:
		return m.syncV.load()
	case tabAccount:
		return m.acct.load()
	}
	return nil
}

func (m *Model) listByID(id string) (api.List, bool) {
	for _, l := range m.lists {
		if l.ID == id {
			return l, true
		}
	}
	return api.List{}, false
}

// listName maps an id to a display name, falling back to a short id.
func (m *Model) listName(id string) string {
	if l, ok := m.listByID(id); ok {
		return l.Name
	}
	if id == "" {
		return ""
	}
	return shortID(id)
}

// memberLabel names a user for display: "you", their username or else their
// email when the list's members are loaded, or a short id until then.
func (m *Model) memberLabel(listID, userID string) string {
	if userID == m.user.ID && userID != "" {
		return "you"
	}
	for _, mem := range m.acct.memberCache[listID] {
		if mem.UserID == userID {
			return mem.Name()
		}
	}
	return shortID(userID)
}

// pendingInvitations is how many invitations are waiting, as far as is
// known without asking: the local copy's count after its last sync, or what
// the inbox last read.
func (m *Model) pendingInvitations() int {
	if m.backend != nil {
		return m.backend.Status().Invitations
	}
	if m.listsV.inbox.loaded {
		return len(m.listsV.inbox.items)
	}
	return 0
}

// personalList is the account's default list id.
func (m *Model) personalList() string {
	for _, l := range m.lists {
		if l.IsPersonal {
			return l.ID
		}
	}
	return ""
}

func (m *Model) setLoc(tz string) {
	if tz == "" {
		return
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		m.loc = loc
	}
}

// -- update -----------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(*Model); ok {
		cmd = tea.Batch(cmd, nm.todos.watchCursor())
	}
	// Signing out swaps in a fresh model, so the clocks are started on
	// whichever model is carrying on.
	if nm, ok := next.(*Model); ok {
		nm.holdToasts()
		return nm, tea.Batch(cmd, nm.animate(), nm.autoSync())
	}
	return next, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case frameMsg:
		m.framing = false
		m.frame++
		m.todos.reapAnims()
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		if m.toast != nil {
			m.toastAge++
			if m.toastAge > 2 {
				m.toast = nil
			}
		}
		// Warnings held while the sign-in screen was up come once it has
		// closed, after whatever signing in had to say.
		if m.toast == nil && m.layer != layerLogin {
			m.toast = m.nextToast()
		}
		// The clock keeps running while idle: sections and counts depend on
		// today's date, and a toast only expires on a tick.
		m.todos.computeStats()
		return m, tea.Batch(tickCmd(), m.watchDisk())

	case diskMsg:
		return m, m.onDisk(msg)

	case quitMsg:
		m.quited = true
		return m, tea.Quit

	case signedOutMsg:
		if msg.err != nil {
			m.done()
			if msg.deleted {
				return m, m.warnf("account deleted, but this computer is still signed in: %s", errText(msg.err))
			}
			return m, m.warnf("could not sign out: %s", errText(msg.err))
		}
		// A fresh model, not a reset one: every view holds the old account's
		// todos, lists and undo history, and none of it should survive.
		fresh := New(m.ctx, msg.cfg)
		// The old account's requests still on their way answer to this
		// model now, and must not sign it back in.
		fresh.outdateMe(m.meSeq)
		fresh.width, fresh.height = m.width, m.height
		fresh.resize()
		switch {
		case msg.deleted && msg.leftover != nil:
			return fresh, fresh.warnf("account deleted, but the todos file stayed: %s", errText(msg.leftover))
		case msg.deleted:
			// The sign-in screen shows no toasts, and this is the only
			// word the person gets that the account is gone.
			if fresh.login != nil {
				fresh.login.note = "Account deleted."
				if msg.frozen > 0 {
					fresh.login.note += " " + plural(msg.frozen, "shared list") + " left read-only for their members."
				}
			}
			return fresh, nil
		case msg.leftover != nil:
			return fresh, fresh.warnf("signed out, but the todos file stayed: %s", errText(msg.leftover))
		case msg.unrevoked != nil:
			return fresh, fresh.warnf("signed out here, but the server could not be told to end the session: %s", errText(msg.unrevoked))
		}
		return fresh, fresh.okf("signed out")

	case pollMsg:
		return m, m.onPoll()

	case previewDueMsg, previewMsg:
		return m, m.onPreview(msg)

	case peekDueMsg:
		return m, m.todos.onPeekDue(msg)
	case peekMsg:
		m.todos.onPeek(msg)
		return m, nil

	case signedInMsg:
		return m, m.onSignedIn(msg)
	case codeSentMsg:
		return m, m.onCodeSent(msg)
	case browserOpenedMsg:
		if m.login != nil && msg.seq == m.login.seq && msg.err != nil {
			m.login.err = "Could not open the browser. Open the link shown here, or use an emailed code."
		}
		return m, nil
	case browserVerifiedMsg:
		return m, m.onBrowserVerified(msg)
	case codeVerifiedMsg:
		return m, m.onCodeVerified(msg)
	case sessionSavedMsg:
		if msg.err != nil {
			return m, m.warnf("signed in, but the session could not be saved, so the next start asks again: %s", errText(msg.err))
		}
		return m, nil
	case syncedMsg:
		return m, m.onSynced(msg)
	case syncDueMsg:
		m.syncQueued = false
		return m, m.syncNow()

	case tea.MouseMsg:
		return m.onMouse(msg)

	case tea.KeyMsg:
		return m.onKey(msg)
	}

	var cmd tea.Cmd
	m, cmd = m.onResult(msg)
	return m, cmd
}

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C always quits, even inside a modal.
	if msg.Type == tea.KeyCtrlC {
		m.quited = true
		return m, tea.Quit
	}

	switch m.layer {
	case layerLogin:
		return m, m.onLoginKey(msg)
	case layerForm:
		return m.onFormKey(msg)
	case layerConfirm:
		return m.onConfirmKey(msg)
	case layerHelp:
		if k := msg.String(); k == "esc" || k == "?" || k == "q" {
			m.layer = layerNone
			return m, nil
		}
		m.help.update(msg)
		return m, nil
	case layerSearch:
		return m.onSearchKey(msg)
	case layerPalette:
		return m.onPaletteKey(msg)
	case layerThemes:
		return m, m.onThemeKey(msg)
	}

	return m.onViewKey(msg)
}

func (m *Model) onViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.dispatchKey(remapKey(m.cfg.Keys, msg))
}

// dispatchKey handles a key in the main views, after any remapping. The
// palette calls it directly, so an entry does what its key does whatever
// the user has rebound.
func (m *Model) dispatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()

	// Global bindings.
	switch k {
	case "q":
		m.quited = true
		return m, tea.Quit
	case "?":
		m.help.height = max(m.height-6, 3)
		m.help.width = max(m.width-8, 20)
		m.layer = layerHelp
		return m, nil
	case "r":
		m.busy = true
		m.syncRetry = time.Time{}
		return m, tea.Batch(m.setBusy("refreshing"), m.refreshAll(true), m.syncNow())
	case "1", "2", "3", "4":
		return m, m.switchTab(tab(int(k[0] - '1')))
	case "tab":
		// Inside a todo, tab moves to the checklist instead of the next
		// tab, so the panes of one todo are reachable without leaving it.
		if m.focus != focusList {
			break
		}
		return m, m.switchTab(m.tab + 1)
	case "shift+tab":
		if m.focus != focusList {
			break
		}
		return m, m.switchTab(m.tab - 1)
	case "/":
		return m, m.openSearch()
	case ":", "ctrl+p":
		return m, m.openPalette()
	case "ctrl+t":
		m.openThemes()
		return m, nil
	}

	switch m.tab {
	case tabTodos:
		return m.todos.onKey(m, msg)
	case tabLists:
		return m.listsV.onKey(m, msg)
	case tabSync:
		return m.syncV.onKey(m, msg)
	case tabAccount:
		return m.acct.onKey(m, msg)
	}
	return m, nil
}

func (m *Model) switchTab(t tab) tea.Cmd {
	t = ((t % numTabs) + numTabs) % numTabs
	if t == m.tab {
		return nil
	}
	if m.tab == tabLists {
		// The inbox belongs to the Lists tab; coming back finds the lists.
		m.listsV.inbox.close()
	}
	m.tab = t
	m.focus = focusList
	if t == tabSync {
		return m.syncV.onEnter(m)
	}
	// A tab with nothing to fetch must not be left looking busy.
	cmd := m.refreshCurrentTab(false)
	if cmd == nil {
		return nil
	}
	m.busy = true
	return tea.Batch(m.setBusy(""), cmd)
}

// -- search -----------------------------------------------------------------

// openSearch starts a search. Search only means something for todos, so it
// brings the Todos tab up instead of filtering something out of sight.
func (m *Model) openSearch() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.tab != tabTodos {
		cmds = append(cmds, m.switchTab(tabTodos))
	}
	m.focus = focusList
	// A search is of the list on screen, not of a view that needs repair.
	m.todos.repair = nil
	m.layer = layerSearch
	m.search.SetValue(m.todos.filter.query)
	m.search.CursorEnd()
	m.search.Focus()
	if m.todos.filter.query != "" {
		// Start again from the whole list, so editing the words can widen
		// the results as well as narrow them.
		m.todos.filter.query = ""
		m.todos.live = m.search.Value()
		cmds = append(cmds, m.todos.load())
	}
	return tea.Batch(cmds...)
}

func (m *Model) onSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.layer = layerNone
		m.search.Blur()
		m.search.SetValue("")
		m.todos.filter.query = ""
		m.todos.live = ""
		m.busy = true
		return m, tea.Batch(m.setBusy(""), m.todos.load())
	case "enter":
		// The list keeps its typed-in narrowing until the server's answer
		// arrives, so committing a search never flashes the whole list.
		m.layer = layerNone
		m.search.Blur()
		m.todos.filter.query = strings.TrimSpace(m.search.Value())
		m.busy = true
		return m, tea.Batch(m.setBusy(""), m.todos.load())
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.todos.setLive(m.search.Value())
	return m, cmd
}

// -- forms ------------------------------------------------------------------

func (m *Model) openForm(f *form) tea.Cmd {
	m.previewSeq++
	m.previewFor = ""
	m.captureSmart = nil
	m.capturePreview = nil
	f.width = min(m.width-8, 78)
	f.maxH = max(m.height-6, 10)
	f.focusFirst()
	m.form = f
	m.layer = layerForm
	return textinput.Blink
}

func (m *Model) onFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	if f == nil {
		m.layer = layerNone
		return m, nil
	}
	if f.id == formNewTodo && (msg.String() == "ctrl+d" || msg.String() == "ctrl+t") {
		return m, m.changeCaptureReading(msg.String())
	}
	switch msg.String() {
	case "esc":
		m.layer = layerNone
		m.form = nil
		return m, nil
	case "ctrl+s":
		return m.submitForm()
	case "ctrl+e":
		return m, m.openEditor()
	case "enter":
		// Enter saves, except in a box that is collecting several lines:
		// there it belongs to the editor, which the form handles below.
		if m.form.focusedMultiline() {
			break
		}
		return m.submitForm()
	}
	var cmd tea.Cmd
	f, cmd = f.update(msg)
	m.form = f
	return m, tea.Batch(cmd, m.schedulePreview())
}

// parseDebounce is how long typing must pause before the server is asked
// how it reads a title. Tests set it to zero, which turns the preview off.
var parseDebounce = 280 * time.Millisecond

// previewHint is what the preview line says before there is anything to
// read.
func previewHint() string {
	return styFaint.Render("dates in the text are understood as you type")
}

// previewDueMsg fires when typing has paused, and previewMsg carries the
// server's reading back. Both name the keystroke they belong to, so an
// answer to text that has since changed is dropped.
type (
	previewDueMsg struct{ seq int }
	previewMsg    struct {
		seq     int
		preview api.ParsePreview
		err     error
	}
)

// schedulePreview asks for a fresh reading of the new todo's title once
// typing pauses.
func (m *Model) schedulePreview() tea.Cmd {
	if m.form == nil || m.form.id != formNewTodo || parseDebounce <= 0 {
		return nil
	}
	title := strings.TrimSpace(m.form.valueOf("title"))
	if title == m.previewFor {
		return nil
	}
	m.previewFor = title
	m.captureSmart = nil
	m.capturePreview = nil
	m.form.setPreview(previewHint())
	m.previewSeq++
	if title == "" {
		m.form.setPreview(previewHint())
		return nil
	}
	seq := m.previewSeq
	return tea.Tick(parseDebounce, func(time.Time) tea.Msg { return previewDueMsg{seq: seq} })
}

// onPreview handles both halves of the live reading.
func (m *Model) onPreview(msg tea.Msg) tea.Cmd {
	if m.form == nil || m.form.id != formNewTodo {
		return nil
	}
	switch msg := msg.(type) {
	case previewDueMsg:
		if msg.seq != m.previewSeq {
			return nil
		}
		ctx, c, title, smart := m.ctx, m.client, m.previewFor, m.captureSmart
		return func() tea.Msg {
			p, err := c.ParseWithSmart(ctx, title, smart)
			return previewMsg{seq: msg.seq, preview: p, err: err}
		}
	case previewMsg:
		// A failed reading is not worth interrupting typing for; the line
		// just keeps what it said.
		if msg.seq == m.previewSeq && msg.err == nil {
			m.capturePreview = &msg.preview
			m.form.setPreview(styAccent.Render("→ ") + m.captureSummary(&msg.preview))
		}
	}
	return nil
}

func (m *Model) submitForm() (tea.Model, tea.Cmd) {
	res := m.form.result(false)
	m.layer = layerNone
	m.form = nil
	if res.cancel {
		return m, nil
	}
	return m.applyForm(res)
}

// -- confirmations ----------------------------------------------------------

func (m *Model) ask(id, title, body string, danger bool, onYes func() tea.Cmd) tea.Cmd {
	return m.askOr(id, title, body, danger, onYes, nil)
}

// askOr is ask for a question where saying no is acted on too. Both
// answers are set together, as a question can replace one that was never
// answered, and the new one must not carry the old one's no.
func (m *Model) askOr(id, title, body string, danger bool, onYes, onNo func() tea.Cmd) tea.Cmd {
	m.conf = newConfirm(id, title, body, danger)
	m.conf.width = m.width
	// "yes" is preselected so enter works, but a destructive prompt needs
	// two presses, which is what stops a stray enter from deleting data.
	m.conf.yes = true
	m.pending, m.declined = onYes, onNo
	m.layer = layerConfirm
	return nil
}

func (m *Model) onConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.conf
	if c == nil {
		m.layer = layerNone
		return m, nil
	}
	k := msg.String()
	if k == "esc" || k == "q" || (k == "enter" && !c.yes) {
		onNo := m.declined
		m.layer = layerNone
		m.conf, m.pending, m.declined = nil, nil, nil
		if onNo != nil {
			return m, onNo()
		}
		return m, nil
	}
	if c.danger && !c.armed && (k == "enter" || k == "y") {
		// First confirm arms; the dialog stays open.
		c.armed = true
		return m, nil
	}
	if (k == "enter" || k == "y") && c.yes {
		onYes := m.pending
		m.layer = layerNone
		m.conf = nil
		m.pending, m.declined = nil, nil
		if onYes != nil {
			m.busy = true
			return m, tea.Batch(m.setBusy(""), onYes())
		}
		return m, nil
	}
	c, _ = c.update(msg)
	m.conf = c
	return m, nil
}

// -- onboarding -------------------------------------------------------------

// onLoginKey forwards keys to the sign-in screen and acts on what it ends
// with: an email to send a code to, a code to check, a token to sign in
// with, the choice to go without an account, or going back to a session
// that was already running.
func (m *Model) onLoginKey(msg tea.KeyMsg) tea.Cmd {
	cmd, done := m.login.update(msg)
	if !done {
		return cmd
	}
	switch {
	case m.login.back:
		m.login, m.layer = nil, layerNone
		return nil
	case m.login.choseLocal:
		m.cfg.Local = true
		if err := m.cfg.Save(); err != nil {
			return m.warnf("could not save the choice: %s", errText(err))
		}
		m.login, m.layer = nil, layerNone
		m.busy = true
		return tea.Batch(m.setBusy(""), m.refreshAll(false))
	case m.login.want == wantCode:
		return m.requestCode()
	case m.login.want == wantVerify:
		return m.verifyCode()
	case m.login.want == wantBrowser:
		return m.beginBrowser()
	}
	token := strings.TrimSpace(m.login.tokenInput.Value())
	if token == "" {
		return m.warnf("type your email, or paste the token that starts with evt_")
	}
	base := strings.TrimSpace(m.login.baseInput.Value())
	m.cfg.Token = strings.TrimPrefix(token, "Bearer ")
	m.cfg.BaseURL = base
	m.login = nil
	m.layer = layerNone
	m.busy = true
	m.joining = &joining{remote: local.Remote{BaseURL: base, Token: m.cfg.Token}}
	if m.backend != nil {
		// The token is only saved once the server has accepted it, so a
		// mistyped one never replaces a session that works.
		m.signingIn = true
		return tea.Batch(m.setBusy("signing in"), m.signIn(false))
	}
	m.client = directClient(m.joining.remote)
	m.user = api.User{}
	m.outdateMe(m.meSeq)
	ended := m.finishJoin()
	if err := m.cfg.Save(); err != nil {
		return m.warnf("could not save the token: %s", errText(err))
	}
	return tea.Batch(ended, m.okf("token saved"), m.setBusy("verifying token"), m.fetchMe())
}

// writeExport saves an account export to path.
func writeExport(path string, ex api.Export) error { return local.WriteExport(path, ex) }

// changeCaptureReading keeps an override tied to the title whose preview offered it.
func (m *Model) changeCaptureReading(key string) tea.Cmd {
	p := m.capturePreview
	if p == nil || strings.TrimSpace(m.form.valueOf("title")) != m.previewFor {
		return nil
	}
	switch key {
	case "ctrl+d":
		if p.KeptAsName == "" {
			return nil
		}
		plain := false
		m.captureSmart = &plain
	case "ctrl+t":
		a := p.AssumedTime
		if a == nil || a.Text == "" || a.AlternativeText == "" || !strings.Contains(m.previewFor, a.Text) {
			return nil
		}
		title := strings.Replace(m.previewFor, a.Text, a.AlternativeText, 1)
		for _, field := range m.form.fields {
			if field.key == "title" {
				if field.kind == fieldArea {
					field.area.SetValue(title)
				} else {
					field.input.SetValue(title)
				}
			}
		}
		m.previewFor = title
	}
	m.capturePreview = nil
	m.previewSeq++
	m.form.setPreview(styFaint.Render("reading dates"))
	return m.onPreview(previewDueMsg{seq: m.previewSeq})
}

func (m *Model) captureSummary(p *api.ParsePreview) string {
	text := m.parseSummary(p)
	if p.KeptAsName != "" {
		text += "\nctrl+d reads as a date"
	}
	if p.AssumedTime != nil {
		text += "\nctrl+t flips am/pm"
	}
	return text
}
