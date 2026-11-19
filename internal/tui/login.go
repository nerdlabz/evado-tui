package tui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/auth"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// loginView is the sign-in screen. It opens on a short list of ways to
// start, one of them lit, and asks for an email, a token or a server only
// once that is what was chosen, so no choice is hidden behind a field.
type loginView struct {
	// tokenInput takes either an email or an API token; which it is
	// decides how signing in goes.
	tokenInput textinput.Model
	baseInput  textinput.Model
	codeInput  textinput.Model
	// choice is the lit row of the list, and focus the lit control while
	// the screen asks for a code.
	choice, focus int
	// entry says the email or token field is open, and editServer the
	// server's, with wasServer what esc puts back.
	entry, editServer bool
	wasServer         string
	// otherServer says the program was pointed at a server that is not
	// the ordinary one, which is the only time its address is shown.
	otherServer bool
	err         string
	signupURL   string
	browser     *auth.BrowserLogin

	// allowLocal offers going without an account, and choseLocal reports
	// that it was picked.
	allowLocal, choseLocal bool
	// inApp means a session is already running behind this screen, so esc
	// goes back to it instead of quitting, which back reports.
	inApp, back bool

	// want is what the screen asks for when it reports done with neither
	// back nor choseLocal: a token sign-in, a code, or a code checked.
	want loginWant

	// email is where the last code went, and server the address it was
	// asked of. While email is set the screen asks for the code.
	email, server string
	// note says what just happened, such as a code being sent.
	note string
	// waiting says a request is out, and seq numbers them so an answer
	// to one the person has moved on from is dropped.
	waiting bool
	seq     int
	// retryAt is when the server will send another code, after it said
	// how long to wait.
	retryAt time.Time
	now     func() time.Time
}

type loginWant int

const (
	wantToken loginWant = iota
	wantCode
	wantVerify
	wantBrowser
)

// loginRow is one way to start, with what is said of it while it is lit.
type loginRow struct {
	key, title, about string
}

const (
	rowBrowser = "browser"
	rowEntry   = "entry"
	rowLocal   = "local"
	rowServer  = "server"
)

// rows are the ways to start, in order. Going without an account is only
// offered where it is possible, and the server only when it was set to
// another one, as with -server.
func (l *loginView) rows() []loginRow {
	rows := []loginRow{
		{rowBrowser, "Sign in with your browser", "Sign in or create an account on the web, then come back here."},
		{rowEntry, "Use an email code or an API token", "For a machine with no browser, or a token from Settings, API & apps."},
	}
	if l.allowLocal {
		rows = append(rows, loginRow{rowLocal, "Use without an account", "Your todos are kept on this computer, and come with you if you sign in later."})
	}
	if l.otherServer {
		rows = append(rows, loginRow{rowServer, "Server", "Where your sign-in is sent. This is not the ordinary Evado server."})
	}
	return rows
}

// entering reports whether the email or token field is open.
func (l *loginView) entering() bool { return l.entry && !l.editServer }

// fill opens the email or token field with a value already in it, as when
// a session ends and its address is offered to sign in again. With nothing
// to offer the list stays.
func (l *loginView) fill(value string) {
	l.tokenInput.SetValue(value)
	l.tokenInput.CursorEnd()
	l.entry = value != ""
	l.applyFocus()
}

// The things the cursor can rest on while asking for the code.
const (
	loginCode = iota
	loginResend
	loginChange
)

func newLoginView(defBase, cfgBase string) *loginView {
	if cfgBase == "" {
		cfgBase = defBase
	}
	token := newInput()
	token.Placeholder = "you@example.com, or evt_…"
	token.CharLimit = 200
	token.Width = 56

	base := newInput()
	base.Placeholder = api.DefaultBaseURL
	base.SetValue(cfgBase)
	base.CharLimit = 200
	base.Width = 56

	code := newInput()
	code.Placeholder = "123456"
	code.CharLimit = 12
	code.Width = 12

	l := &loginView{tokenInput: token, baseInput: base, codeInput: code, now: time.Now}
	l.otherServer = strings.TrimSuffix(cfgBase, "/") != strings.TrimSuffix(defBase, "/")
	// The first field is where the user starts typing, so it owns the
	// cursor from the first frame.
	l.applyFocus()
	return l
}

// looksLikeEmail tells an address from a pasted token. Tokens never hold
// an @, so there is no guessing between the two.
func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "@") && !strings.ContainsAny(s, " \t") && !strings.HasPrefix(s, "evt_")
}

// asking reports whether the screen is asking for an emailed code.
func (l *loginView) asking() bool { return l.email != "" }

// code is the typed code without the spaces or dashes people copy with it.
func (l *loginView) code() string {
	return strings.NewReplacer(" ", "", "-", "").Replace(l.codeInput.Value())
}

// update handles keys. It returns any command plus a done flag, set when
// the screen has an answer: something to sign in with, going without an
// account, or going back.
func (l *loginView) update(msg tea.KeyMsg) (tea.Cmd, bool) {
	l.want = wantToken
	k := msg.String()
	switch k {
	case "ctrl+o":
		address := l.signupURL
		if l.browser != nil {
			address = l.browser.URL
		}
		if address != "" {
			seq := l.seq
			return func() tea.Msg { return browserOpenedMsg{seq: seq, err: openWebAddress(address)} }, false
		}
	case "ctrl+b":
		if !l.waiting {
			l.want = wantBrowser
			return nil, true
		}
	case "tab", "down", "shift+tab", "up":
		step := 1
		if k == "shift+tab" || k == "up" {
			step = -1
		}
		switch {
		case l.browser != nil:
		case l.asking():
			l.focus = (l.focus + step + 3) % 3
		case !l.entering() && !l.editServer:
			n := len(l.rows())
			l.choice = (l.choice + step + n) % n
		}
		l.applyFocus()
		return textinput.Blink, false
	case "esc":
		switch {
		case l.browser != nil:
			l.cancelBrowser()
			l.err = "Browser sign-in cancelled."
		case l.asking():
			l.changeEmail()
			return textinput.Blink, false
		case l.editServer:
			l.baseInput.SetValue(l.wasServer)
			l.editServer = false
		case l.entering():
			// What was typed is kept for coming back to.
			l.entry, l.err, l.signupURL = false, "", ""
		case l.inApp:
			l.back = true
			return nil, true
		default:
			return tea.Quit, false
		}
		l.applyFocus()
		return nil, false
	case "ctrl+c":
		l.cancelBrowser()
		return tea.Quit, false
	case "enter":
		switch {
		case l.waiting:
		case l.asking():
			return l.enterCode()
		case l.editServer:
			l.editServer = false
			l.applyFocus()
		case l.entering():
			if strings.TrimSpace(l.tokenInput.Value()) == "" {
				l.err = "Type your account email, or paste an API token."
				return nil, false
			}
			if looksLikeEmail(l.tokenInput.Value()) {
				l.want = wantCode
			}
			return nil, true
		default:
			switch l.rows()[l.choice].key {
			case rowBrowser:
				l.want = wantBrowser
				return nil, true
			case rowLocal:
				l.choseLocal = true
				return nil, true
			case rowEntry:
				l.entry, l.err = true, ""
			case rowServer:
				l.editServer, l.wasServer = true, l.baseInput.Value()
				l.baseInput.CursorEnd()
			}
			l.applyFocus()
			return textinput.Blink, false
		}
		return nil, false
	}
	if l.browser != nil {
		return nil, false
	}
	// Typing or pasting on the list goes straight to the field, so a token
	// on the clipboard needs no row chosen first.
	if !l.asking() && !l.editServer && !l.entering() {
		if msg.Type != tea.KeyRunes || strings.TrimSpace(string(msg.Runes)) == "" {
			return nil, false
		}
		l.entry = true
	}
	l.applyFocus()
	var cmd tea.Cmd
	switch {
	case l.asking() && l.focus == loginCode:
		l.codeInput, cmd = l.codeInput.Update(msg)
	case l.asking():
	case l.editServer:
		before := l.baseInput.Value()
		l.baseInput, cmd = l.baseInput.Update(msg)
		if before != l.baseInput.Value() {
			l.signupURL = ""
		}
	default:
		before := l.tokenInput.Value()
		l.tokenInput, cmd = l.tokenInput.Update(msg)
		if before != l.tokenInput.Value() {
			l.signupURL = ""
		}
		if l.tokenInput.Value() != "" {
			l.err = ""
		}
	}
	return cmd, false
}

func (l *loginView) cancelBrowser() {
	if l.browser == nil {
		return
	}
	l.browser.Close()
	l.browser = nil
	l.waiting = false
	l.seq++
}

// enterCode acts on enter while the screen asks for the code.
func (l *loginView) enterCode() (tea.Cmd, bool) {
	switch l.focus {
	case loginResend:
		if wait := l.retryAt.Sub(l.now()); wait > 0 {
			l.err = "The server will send another code in " + waitText(wait) + ". The last one still works until it expires."
			return nil, false
		}
		l.want = wantCode
		return nil, true
	case loginChange:
		l.changeEmail()
		return textinput.Blink, false
	}
	if n := len(l.code()); n != 6 || strings.Trim(l.code(), "0123456789") != "" {
		l.err = "The code is the 6 digits in the email."
		return nil, false
	}
	l.want = wantVerify
	return nil, true
}

// changeEmail goes back to asking who, with the address kept to correct.
func (l *loginView) changeEmail() {
	email := l.email
	l.email, l.note, l.err = "", "", ""
	l.codeInput.SetValue("")
	l.waiting = false
	l.seq++
	l.fill(email)
}

// start marks a request as sent and returns its number.
func (l *loginView) start() int {
	l.waiting = true
	l.signupURL = ""
	l.err = ""
	l.seq++
	return l.seq
}

// onCodeSent takes in the answer to asking for a code.
func (l *loginView) onCodeSent(msg codeSentMsg) {
	l.waiting = false
	if l.offerSignup(msg.err, msg.email) {
		return
	}
	var apiErr *api.Error
	limited := errors.As(msg.err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
	if msg.err != nil && !limited {
		// Nothing was sent, so the address stays where it can be fixed.
		l.err = loginErrText(msg.err)
		return
	}
	first := !l.asking()
	l.email, l.server = msg.email, msg.server
	if first {
		l.codeInput.SetValue("")
		l.focus = loginCode
		l.applyFocus()
	}
	if limited {
		wait := apiErr.RetryAfter
		if wait <= 0 {
			wait = time.Minute
		}
		l.retryAt = l.now().Add(wait)
		l.note = ""
		l.err = "Too many codes were asked for this email. Use the last one sent, or ask again in " + waitText(wait) + "."
		return
	}
	l.note = "We sent a 6-digit code to " + msg.email + "."
	if !first {
		l.note = "We sent a new code to " + msg.email + ". Earlier ones no longer matter."
	}
}

// onVerifyFailed says why a code was not accepted, keeping what was typed
// so a slip can be fixed rather than retyped.
func (l *loginView) onVerifyFailed(err error) {
	l.waiting = false
	if l.offerSignup(err, l.email) {
		return
	}
	l.note = ""
	var apiErr *api.Error
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest:
		l.err = "That code is wrong or has expired. Check it, or ask for a new one."
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		wait := max(apiErr.RetryAfter, time.Second)
		l.err = "Too many tries. Wait " + waitText(wait) + ", then try again."
	default:
		l.err = loginErrText(err)
	}
	l.focus = loginCode
	l.applyFocus()
}

// loginErrText explains a failed sign-in request.
func loginErrText(err error) string {
	var browser auth.BrowserError
	if errors.As(err, &browser) {
		return browser.Error()
	}
	if api.CodeOf(err) == api.CodeSignupRequired {
		return "No account uses this address yet. Create one in your browser, then sign in here."
	}
	if local.Offline(err) {
		return "Could not reach the server. Check the address and your connection."
	}
	return errText(err)
}

// waitText says how long to wait in words.
func waitText(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return plural(max(int((d+time.Second-1)/time.Second), 1), "second")
	case d < 90*time.Minute:
		return plural(int((d+time.Minute-1)/time.Minute), "minute")
	}
	return plural(int((d+time.Hour-1)/time.Hour), "hour")
}

// applyFocus keeps the cursor in at most one field.
func (l *loginView) applyFocus() {
	l.tokenInput.Blur()
	l.baseInput.Blur()
	l.codeInput.Blur()
	switch {
	case l.asking():
		if l.focus == loginCode {
			l.codeInput.Focus()
		}
	case l.editServer:
		l.baseInput.Focus()
	case l.entering():
		l.tokenInput.Focus()
	}
}

// button draws one of the screen's buttons, lit when the cursor is on it.
func button(text string, on bool) string {
	if on {
		return styPill.Render("  " + text + "  ")
	}
	return lipgloss.NewStyle().Foreground(colMuted).Background(colPanel).Render("  " + text + "  ")
}

func (l *loginView) view(w, h int) string {
	tw := min(w-8, 64)
	l.tokenInput.Width = tw
	l.baseInput.Width = tw

	var lines []string
	if l.asking() {
		lines = l.codeLines(tw)
	} else {
		lines = l.whoLines(tw)
	}
	body := lipgloss.NewStyle().Width(tw + 4).Render(strings.Join(lines, "\n"))
	box := styBox.Padding(1, 2).Render(body)
	// The mascot is a greeting, not a requirement, so it gives way on a
	// short terminal instead of pushing the fields off screen.
	if lipgloss.Height(box)+catchyRows+1 <= h {
		box = lipgloss.JoinVertical(lipgloss.Center, renderCatchy(), "", box)
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// loginKeys is the line of keys under a step, each with what it does.
func loginKeys(pairs ...string) string {
	var out []string
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, keyed(pairs[i], styKey.Render(pairs[i])+styFaint.Render(" "+pairs[i+1])))
	}
	return strings.Join(out, "   ")
}

// whoLines is the screen before a code is asked for: the list of ways to
// start, or the one step that was chosen from it.
func (l *loginView) whoLines(tw int) []string {
	lines := []string{
		brandGradient("◆ evado") + styFaint.Render("  your todos, in the terminal"),
		"",
	}
	server := strings.TrimSuffix(l.baseInput.Value(), "/")
	quit := "quit"
	if l.inApp {
		quit = "back"
	}
	var problem []string
	if l.err != "" {
		problem = append(problem, styErr.Render(wrapBlock(l.err, tw)), "")
	}
	switch {
	case l.browser != nil:
		return append(lines,
			wrapBlock("Finish signing in in your browser. This link expires in 5 minutes.", tw),
			"",
			webLinkText(l.browser.URL, wrapBlock(l.browser.URL, tw)),
			"",
			"  "+loginKeys("ctrl+o", "open the link again", "esc", "cancel"),
		)
	case l.editServer:
		return append(lines,
			"  Server",
			"",
			styAccent.Render("▌ ")+l.baseInput.View(),
			"",
			styFaint.Render(wrapBlock("The address your sign-in is sent to. The ordinary one is "+api.DefaultBaseURL+".", tw)),
			"",
			"  "+loginKeys("enter", "save", "esc", "cancel"),
		)
	case l.entering():
		lines = append(lines, "  Sign in with an email code or an API token", "")
		lines = append(lines, problem...)
		if l.signupURL != "" {
			lines = append(lines, "  "+webLink(l.signupURL), "  "+loginKeys("ctrl+o", "open the sign-up page", "ctrl+b", "sign in with browser"), "")
		}
		lines = append(lines, styAccent.Render("▌ ")+l.tokenInput.View(), "")
		keys := loginKeys("esc", "back")
		switch {
		case l.waiting:
			keys = styFaint.Render("sending the code…")
		case looksLikeEmail(l.tokenInput.Value()):
			keys = keyed("enter", button("send code", true)) + " " + styKey.Render("enter") + "   " + keys
		case strings.TrimSpace(l.tokenInput.Value()) != "":
			keys = keyed("enter", button("sign in", true)) + " " + styKey.Render("enter") + "   " + keys
		}
		lines = append(lines,
			styFaint.Render(wrapBlock("An email gets a 6-digit code. A token starts with evt_ and is stored in a 0600 file in your config directory.", tw)),
			"",
			"  "+keys,
		)
		if l.otherServer {
			lines = append(lines, "", styFaint.Render("  on "+server))
		}
		return lines
	}
	if l.note != "" {
		lines = append(lines, wrapBlock(l.note, tw), "")
	}
	lines = append(lines, problem...)
	press := pressing()
	for i, row := range l.rows() {
		value := ""
		if row.key == rowServer {
			value = "  " + styFaint.Render(server)
		}
		choose := func(bool) tea.Cmd {
			l.choice = i
			l.applyFocus()
			return press("enter")
		}
		if i != l.choice {
			lines = append(lines, clickable("  "+styMuted.Render(row.title)+value, choose))
			continue
		}
		lines = append(lines, clickable(styAccent.Render("▌ ")+styKey.Render(row.title)+value, choose))
		for _, line := range strings.Split(wrapBlock(row.about, tw-4), "\n") {
			lines = append(lines, "    "+styFaint.Render(strings.TrimLeft(line, " ")))
		}
	}
	return append(lines, "", "  "+loginKeys("↑↓", "move", "enter", "choose", "esc", quit))
}

// codeLines ask for the code that was emailed.
func (l *loginView) codeLines(tw int) []string {
	lines := []string{
		brandGradient("◆ evado") + styFaint.Render("  your todos, in the terminal"),
		"",
	}
	if l.note != "" {
		lines = append(lines, wrapBlock(l.note, tw))
	}
	lines = append(lines,
		wrapBlock("Enter the code below to sign in to "+l.email+".", tw),
		"",
	)
	if l.err != "" {
		lines = append(lines, styErr.Render(wrapBlock(l.err, tw)))
	}
	if l.waiting {
		lines = append(lines, styFaint.Render("one moment…"))
	}
	mark, name := "  ", styMuted.Render("code     ")
	if l.focus == loginCode {
		mark, name = styAccent.Render("▌ "), styKey.Render("code     ")
	}
	lines = append(lines, clickable(mark+name+l.codeInput.View(), func(bool) tea.Cmd {
		l.focus = loginCode
		l.applyFocus()
		return nil
	}), "")
	resend := "resend code"
	if wait := l.retryAt.Sub(l.now()); wait > 0 {
		resend = fmt.Sprintf("resend in %s", waitText(wait))
	}
	press := pressing()
	act := func(focus int, s string) string {
		return clickable(s, func(bool) tea.Cmd {
			l.focus = focus
			l.applyFocus()
			return press("enter")
		})
	}
	lines = append(lines,
		"  "+act(loginCode, button("verify", l.focus == loginCode))+" "+
			act(loginResend, button(resend, l.focus == loginResend))+" "+
			act(loginChange, button("change email", l.focus == loginChange)),
		"",
		"  "+loginKeys("enter", "choose", "tab", "next", "esc", "change email"),
		"",
		styFaint.Render("on "+strings.TrimSuffix(l.server, "/")),
	)
	return lines
}

type browserOpenedMsg struct {
	seq int
	err error
}

func (l *loginView) offerSignup(err error, email string) bool {
	var signup *api.SignupRequired
	if !errors.As(err, &signup) {
		return false
	}
	l.email, l.note = "", ""
	l.codeInput.SetValue("")
	l.signupURL = webAddress(signup.SignupURL)
	l.err = loginErrText(err)
	l.fill(email)
	return true
}
