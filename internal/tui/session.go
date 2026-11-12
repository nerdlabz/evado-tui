package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/auth"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// Signing in with an emailed code starts a session that renews itself,
// kept in its own file by the auth package. A pasted API token never
// starts one: the two are separate ways in, and whichever went through
// last is the one this computer keeps.

// joining is a sign-in started from the sign-in screen that has not gone
// through yet: how it reaches the server, and the session to keep once it
// has. Until then the sign-in this computer already had stays as it was.
type joining struct {
	remote  local.Remote
	session *auth.Session
}

type (
	browserVerifiedMsg struct{ codeVerifiedMsg }
	// codeSentMsg is the answer to asking for a code.
	codeSentMsg struct {
		seq           int
		email, server string
		err           error
	}
	// codeVerifiedMsg is the answer to a code being checked.
	codeVerifiedMsg struct {
		seq    int
		server string
		res    api.Verified
		err    error
	}
	// sessionSavedMsg reports keeping a new session on disk.
	sessionSavedMsg struct{ err error }
)

func (m *Model) beginBrowser() tea.Cmd {
	l := m.login
	server := auth.Server(strings.TrimSpace(l.baseInput.Value()))
	flow, err := auth.BeginBrowser(m.ctx, server)
	if err != nil {
		l.err = loginErrText(err)
		return nil
	}
	l.email, l.note = "", ""
	seq := l.start()
	l.browser = flow
	return tea.Batch(
		func() tea.Msg { return browserOpenedMsg{seq: seq, err: openWebAddress(flow.URL)} },
		func() tea.Msg {
			res, err := flow.Wait()
			return browserVerifiedMsg{codeVerifiedMsg{seq: seq, server: server, res: res, err: err}}
		},
	)
}

func (m *Model) onBrowserVerified(msg browserVerifiedMsg) tea.Cmd {
	if m.login == nil || m.layer != layerLogin || msg.seq != m.login.seq {
		return nil
	}
	m.login.browser = nil
	m.login.waiting = false
	if msg.err != nil {
		m.login.err = loginErrText(msg.err)
		return nil
	}
	return m.onCodeVerified(msg.codeVerifiedMsg)
}

// openSession resumes the session kept for the configured server. A token
// takes precedence, so a -token or EVADO_TOKEN run never touches it.
func (m *Model) openSession() {
	if m.cfg.Token != "" || m.cfg.Demo {
		return
	}
	store, err := auth.DefaultStore()
	if err == nil {
		m.session, err = auth.Open(store, m.cfg.BaseURL)
	}
	if err != nil {
		m.session, m.sessionErr = nil, err
	}
}

// remote is how this computer reaches the account: the sign-in under way,
// else the kept session, else the token.
func (m *Model) remote() local.Remote {
	switch {
	case m.joining != nil:
		return m.joining.remote
	case m.session != nil:
		return local.Remote{BaseURL: m.session.BaseURL(), HTTP: m.session.Client()}
	}
	return local.Remote{BaseURL: m.cfg.BaseURL, Token: m.cfg.Token}
}

// directClient talks straight to the server through r.
func directClient(r local.Remote) *api.Client {
	opts := []api.Option{api.WithBaseURL(r.BaseURL)}
	if r.HTTP != nil {
		opts = append(opts, api.WithHTTPClient(r.HTTP))
	}
	return api.New(r.Token, opts...)
}

// hasAccount reports whether there is a token or a session to use.
func (m *Model) hasAccount() bool { return m.client.HasToken() || m.session != nil }

// requestCode asks the server to email a code, to the address typed or,
// for a resend, to the one the last code went to.
func (m *Model) requestCode() tea.Cmd {
	l := m.login
	email, server := strings.TrimSpace(l.tokenInput.Value()), auth.Server(strings.TrimSpace(l.baseInput.Value()))
	if l.asking() {
		email, server = l.email, l.server
	}
	seq, ctx := l.start(), m.ctx
	return func() tea.Msg {
		err := api.New("", api.WithBaseURL(server)).RequestCode(ctx, email)
		return codeSentMsg{seq: seq, email: email, server: server, err: err}
	}
}

// verifyCode sends the typed code to be checked.
func (m *Model) verifyCode() tea.Cmd {
	l := m.login
	email, server, code := l.email, l.server, l.code()
	seq, ctx := l.start(), m.ctx
	return func() tea.Msg {
		res, err := api.New("", api.WithBaseURL(server)).VerifyCode(ctx, email, code)
		if err == nil && (res.AccessToken == "" || res.RefreshToken == "" || res.User.ID == "") {
			err = errors.New("the server accepted the code but sent no session back")
		}
		return codeVerifiedMsg{seq: seq, server: server, res: res, err: err}
	}
}

// onCodeSent hands the answer to the screen that asked, if it still waits.
func (m *Model) onCodeSent(msg codeSentMsg) tea.Cmd {
	if m.login == nil || m.layer != layerLogin || msg.seq != m.login.seq {
		return nil
	}
	m.login.onCodeSent(msg)
	return nil
}

// onCodeVerified signs in with the session a correct code started. It goes
// the same way a token does: through the guarded step that ties the todos
// here to the account, and only kept once that has gone through.
func (m *Model) onCodeVerified(msg codeVerifiedMsg) tea.Cmd {
	if m.login == nil || m.layer != layerLogin || msg.seq != m.login.seq {
		return nil
	}
	store, err := auth.DefaultStore()
	if msg.err == nil && err != nil {
		msg.err = err
	}
	if msg.err != nil {
		m.login.onVerifyFailed(msg.err)
		return nil
	}
	sess := auth.Start(store, msg.server, msg.res)
	m.joining = &joining{remote: local.Remote{BaseURL: sess.BaseURL(), HTTP: sess.Client()}, session: sess}
	m.cfg.BaseURL = sess.BaseURL()
	m.login, m.layer = nil, layerNone
	m.busy = true
	if m.backend != nil {
		m.signingIn = true
		return tea.Batch(m.setBusy("signing in"), m.signIn(false))
	}
	// Straight to the server there is nothing here to tie to the account.
	m.client = directClient(m.joining.remote)
	m.user, m.lists = msg.res.User, msg.res.Lists
	m.outdateMe(m.meSeq)
	cmds := []tea.Cmd{m.finishJoin(), m.setBusy(""), m.refreshAll(true)}
	if err := m.cfg.Save(); err != nil {
		cmds = append(cmds, m.warnf("signed in, but the server choice could not be saved: %s", errText(err)))
	} else {
		cmds = append(cmds, m.okf("signed in as %s", msg.res.User.Email))
	}
	return tea.Batch(cmds...)
}

// finishJoin makes the sign-in that just went through the one this
// computer keeps. A new session is saved, and a session it replaces is
// removed and revoked, in that order so the new one is never the one
// removed. The config is saved by the caller.
func (m *Model) finishJoin() tea.Cmd {
	j := m.joining
	m.joining = nil
	if j == nil {
		return nil
	}
	old := m.session
	m.session = j.session
	m.cfg.Local = false
	if j.session != nil {
		m.cfg.Token = ""
	}
	if old == nil && j.session == nil {
		return nil
	}
	ctx, next := m.ctx, j.session
	return func() tea.Msg {
		revoke := func(context.Context) error { return nil }
		if old != nil {
			revoke, _ = old.Forget(ctx)
		}
		var err error
		if next != nil {
			err = next.Save(ctx)
		}
		// The old session is no use to anyone now. A failure to say so to
		// the server leaves it to expire.
		_ = revoke(ctx)
		return sessionSavedMsg{err: err}
	}
}

// dropJoin abandons a sign-in that did not go through. A session it
// started is revoked, since nothing will ever use it.
func (m *Model) dropJoin() tea.Cmd {
	j := m.joining
	m.joining = nil
	if j == nil || j.session == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		_, _ = j.session.SignOut(ctx)
		return nil
	}
}

// forgetSession drops a session the server no longer accepts.
func (m *Model) forgetSession() {
	if m.session == nil {
		return
	}
	_, _ = m.session.Forget(m.ctx)
	m.session = nil
}

// signInRow says how this computer is signed in, for the Connection pane.
func (m *Model) signInRow() kv {
	if m.session != nil {
		return kv{"signed in", m.session.Email() + ", by emailed code"}
	}
	return kv{"token", maskToken(m.cfg.Token)}
}
