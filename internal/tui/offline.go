package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// The todos live on this computer and sync with the account in the
// background: at start, a moment after anything changes here, on every
// poll, and on r. None of it blocks the interface.

// syncDelay is how long after a change the sync that sends it waits, so a
// run of edits goes up together. Tests set it to zero, which turns the
// automatic sync off.
var syncDelay = 2 * time.Second

// Sync backs off after a failure, from minBackoff up to maxBackoff.
const (
	minBackoff = 30 * time.Second
	maxBackoff = 10 * time.Minute
)

type (
	// signedInMsg is the outcome of tying the todos here to an account.
	signedInMsg struct {
		merge local.Merge
		err   error
	}
	// syncedMsg is the outcome of one sync.
	syncedMsg struct {
		res local.Result
		err error
	}
	// syncDueMsg fires when the wait after a change is over.
	syncDueMsg struct{}
	// diskMsg says how often the todos here have been read again because
	// another copy of evado changed the file.
	diskMsg struct{ reloads uint64 }
)

// signIn ties the todos on this computer to the account the token or
// session belongs to. merge says the person has agreed to add them to an
// account that already has todos of its own.
func (m *Model) signIn(merge bool) tea.Cmd {
	b, ctx, r := m.backend, m.ctx, m.remote()
	return func() tea.Msg {
		mg, err := b.SignInRemote(ctx, r, merge)
		return signedInMsg{merge: mg, err: err}
	}
}

// syncNow syncs with the account, if there is one and no sync is waiting
// or running already. The store runs one sync at a time and answers a
// second at once with nothing done, and taking that answer in would end
// the backoff while the sync still running may be about to fail.
func (m *Model) syncNow() tea.Cmd {
	if m.backend == nil || m.syncQueued || !m.backend.Status().SignedIn {
		return nil
	}
	m.syncQueued = true
	b, ctx := m.backend, m.ctx
	return func() tea.Msg {
		res, err := b.Sync(ctx)
		return syncedMsg{res: res, err: err}
	}
}

// autoSync schedules a sync when something here is waiting to be sent and
// none is on its way already.
func (m *Model) autoSync() tea.Cmd {
	if m.backend == nil || m.syncQueued || syncDelay <= 0 || m.layer == layerLogin {
		return nil
	}
	st := m.backend.Status()
	if !st.SignedIn || st.Pending == 0 || time.Now().Before(m.syncRetry) {
		return nil
	}
	m.syncQueued = true
	return tea.Tick(syncDelay, func(time.Time) tea.Msg { return syncDueMsg{} })
}

// onSignedIn finishes signing in, or asks before merging, or says why it
// could not.
func (m *Model) onSignedIn(msg signedInMsg) tea.Cmd {
	m.done()
	fromLogin := m.signingIn
	m.signingIn = false
	switch {
	case msg.err != nil && !fromLogin && api.CodeOf(msg.err) == api.CodeUnauthorized:
		m.reauthenticate()
		return nil
	case msg.err != nil && !fromLogin:
		// A saved token and no connection: carry on with what is here, and
		// the next poll tries again.
		return tea.Batch(m.warnf("could not sign in yet: %s", errText(msg.err)), m.refreshAll(false))
	case msg.err != nil:
		// Nothing was saved, so the session goes back to how it was. A
		// session the code started is revoked, and its address kept to try
		// again with.
		email := ""
		if m.joining != nil && m.joining.session != nil {
			email = m.joining.session.Email()
		}
		dropped := m.dropJoin()
		m.restoreSavedAccount()
		m.login = m.newLogin(m.cfg.Token != "" || m.cfg.Local || m.session != nil)
		m.layer = layerLogin
		m.login.fill(email)
		switch {
		case api.CodeOf(msg.err) == api.CodeUnauthorized && email != "":
			m.login.err = "The server did not accept the new session. Sign in again."
		case api.CodeOf(msg.err) == api.CodeUnauthorized:
			m.login.err = "That token was rejected. Paste a new one."
		case local.Offline(msg.err):
			m.login.err = "Could not reach the server. Check the address and your connection."
		default:
			m.login.err = errText(msg.err)
		}
		return dropped
	case msg.merge.Needed:
		m.signingIn = fromLogin
		m.declined = m.declineMerge
		return m.ask("merge", "Add this computer's todos to your account?",
			fmt.Sprintf("This computer has %s and the account has %s. Yes adds the ones here to the account, "+
				"and nothing is deleted on either side. No leaves this computer as it is, not signed in.",
				plural(msg.merge.Local, "todo"), plural(msg.merge.Server, "todo")),
			false, func() tea.Cmd { return m.signIn(true) })
	}
	// Whatever the inbox held was the last account's, and so is who is
	// on record until the account is read again.
	m.listsV.inbox = inboxView{m: m}
	if fromLogin {
		m.user = api.User{}
		m.outdateMe(m.meSeq)
	}
	cmds := []tea.Cmd{m.refreshAll(true), m.syncNow()}
	if fromLogin {
		cmds = append(cmds, m.finishJoin())
		m.cfg.Local = false
		if err := m.cfg.Save(); err != nil {
			cmds = append(cmds, m.warnf("signed in, but the sign-in could not be saved: %s", errText(err)))
		} else if msg.merge.Local > 0 {
			cmds = append(cmds, m.okf("signed in; sending %s to your account", plural(msg.merge.Local, "todo")))
		} else {
			cmds = append(cmds, m.okf("signed in"))
		}
	}
	m.busy = true
	return tea.Batch(cmds...)
}

// restoreSavedAccount puts the token and server back to what is saved,
// after a sign-in that did not go through.
func (m *Model) restoreSavedAccount() {
	disk, err := config.Load()
	if err != nil {
		disk = config.Config{}
	}
	m.cfg.Token, m.cfg.BaseURL = disk.Token, disk.BaseURL
}

// declineMerge leaves the todos here out of the account: this computer
// stays as it was, not signed in.
func (m *Model) declineMerge() tea.Cmd {
	m.signingIn = false
	dropped := m.dropJoin()
	m.restoreSavedAccount()
	if m.backend.Status().NeedsSignIn {
		m.backend.Disconnect()
		m.cfg.Token = ""
	}
	return tea.Batch(dropped, m.okf("left as it was: the todos here are still only on this computer"), m.refreshAll(false))
}

// onSynced takes in what a sync did.
func (m *Model) onSynced(msg syncedMsg) tea.Cmd {
	m.syncQueued = false
	if msg.err != nil {
		if api.CodeOf(msg.err) == api.CodeUnauthorized {
			m.reauthenticate()
			return nil
		}
		m.syncBackoff = min(max(m.syncBackoff*2, minBackoff), maxBackoff)
		m.syncRetry = time.Now().Add(m.syncBackoff)
		if local.Offline(msg.err) {
			// Being offline is normal here; the header says so, and the
			// changes wait.
			return nil
		}
		return m.warnf("sync failed: %s", errText(msg.err))
	}
	m.syncBackoff, m.syncRetry = 0, time.Time{}
	var cmds []tea.Cmd
	if msg.res.Changed {
		cmds = append(cmds, m.refreshAll(true))
		// A person's username may be what changed, and the names already
		// on screen were read before.
		for id := range m.acct.memberCache {
			cmds = append(cmds, fetchMemberCache(m.ctx, m.client, id))
		}
		if m.listsV.showMembers {
			cmds = append(cmds, fetchMembers(m.ctx, m.client, m.listsV.membersFor))
		}
	}
	if msg.res.InvitationsChanged && m.listsV.inbox.open {
		cmds = append(cmds, m.listsV.inbox.load())
	}
	var warnings []string
	if n := len(msg.res.Refused); n > 0 {
		warnings = append(warnings, fmt.Sprintf("the server refused %s: %s. Edit to try again.",
			plural(n, "change"), truncate(strings.Join(msg.res.Refused, ", "), 60)))
	}
	// An edit made here that a newer change on the service replaced is gone
	// from the screen by now, so this says which, once.
	if n := len(msg.res.Replaced); n > 0 {
		what := make([]string, n)
		for i, r := range msg.res.Replaced {
			what[i] = r.String()
		}
		verb := "was replaced by a newer change"
		if n > 1 {
			verb = "were replaced by newer changes"
		}
		warnings = append(warnings, fmt.Sprintf("%s %s: %s", plural(n, "edit"), verb, truncate(strings.Join(what, ", "), 90)))
	}
	// A list that left the account takes its todos along, so this says what
	// became of the ones that had not been sent.
	if said := goneText(msg.res.ListsGone); said != "" {
		warnings = append(warnings, said)
	}
	// A list change made with no connection and refused later has already
	// been undone here, so this says what went back and why.
	if n := len(msg.res.ListsRefused); n > 0 {
		warnings = append(warnings, fmt.Sprintf("the server refused %s, now undone: %s",
			plural(n, "list change"), truncate(strings.Join(msg.res.ListsRefused, "; "), 90)))
	}
	warnings = append(warnings, msg.res.ListsWaiting...)
	// One toast holds one line and a later one replaces it, so a sync with
	// several things to say shows them one after another.
	m.warnEach(warnings)
	return tea.Batch(cmds...)
}

// goneText is what became of the unsent todos in lists that left the
// account, as one line.
func goneText(gone []local.GoneList) string {
	was := func(n int) string {
		if n == 1 {
			return "was"
		}
		return "were"
	}
	var said []string
	for _, g := range gone {
		with := fmt.Sprintf("with the list '%s', which is gone from the account", g.Name)
		if n := len(g.Moved); n > 0 {
			said = append(said, fmt.Sprintf("%s made here %s moved to %s: the list '%s' is gone from the account",
				plural(n, "todo"), was(n), g.Into, g.Name))
		}
		if n := len(g.Lost); n > 0 {
			said = append(said, fmt.Sprintf("%s made here %s lost %s: %s",
				plural(n, "todo"), was(n), with, truncate(strings.Join(singleQuoted(g.Lost), ", "), 60)))
		}
		if n := len(g.Edits); n > 0 {
			said = append(said, fmt.Sprintf("%s %s lost %s: %s",
				plural(n, "edit"), was(n), with, truncate(strings.Join(singleQuoted(g.Edits), ", "), 60)))
		}
	}
	return strings.Join(said, "; ")
}

// singleQuoted puts each title in quotes, as the other warnings show them.
func singleQuoted(titles []string) []string {
	out := make([]string, len(titles))
	for i, t := range titles {
		out[i] = "'" + t + "'"
	}
	return out
}

// syncChip says in a word where the todos on screen stand with the
// account.
func (m *Model) syncChip() string {
	if m.backend == nil {
		return ""
	}
	st := m.backend.Status()
	waiting := ""
	if st.Pending > 0 {
		waiting = fmt.Sprintf(" · %d to send", st.Pending)
	}
	switch {
	case st.Syncing:
		return styAccent.Render(m.spinner()) + styMuted.Render(" syncing")
	case st.Detached:
		return styWarn.Render("signed out" + waiting)
	case !st.SignedIn:
		return styFaint.Render("on this computer")
	case st.Err != nil && local.Offline(st.Err):
		return styWarn.Render("offline" + waiting)
	case st.Err != nil:
		return styErr.Render("sync failed" + waiting)
	case st.Refused > 0:
		return styWarn.Render(fmt.Sprintf("%d refused", st.Refused))
	case st.Pending > 0:
		return styMuted.Render(fmt.Sprintf("↑ %d", st.Pending))
	}
	return styOK.Render("✓ synced")
}

// watchDisk looks, on every tick, whether another copy of evado changed
// the todos file: a command such as evado -local add run in another shell.
// The backend reads the file again before anything it does, so nothing
// either side saved is lost, but what is on screen would stay as it was
// until something asked it again.
func (m *Model) watchDisk() tea.Cmd {
	if m.backend == nil {
		return nil
	}
	b := m.backend
	return func() tea.Msg { return diskMsg{reloads: b.Reloads()} }
}

// onDisk shows what another copy of evado changed, and sends it to the
// account if there is one, as it would a change made here.
func (m *Model) onDisk(msg diskMsg) tea.Cmd {
	if msg.reloads == m.diskSeen {
		return nil
	}
	m.diskSeen = msg.reloads
	if m.layer == layerLogin {
		return nil
	}
	return tea.Batch(m.refreshAll(true), m.autoSync())
}
