package tui

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
)

func strp(s string) *string { return &s }

// formAlert is what the open form says went wrong, unwrapped.
func formAlert(h *harness) string {
	h.t.Helper()
	if h.model.form == nil {
		h.t.Fatal("no form is open")
	}
	for _, fl := range h.model.form.fields {
		if fl.alert {
			return fl.value
		}
	}
	return ""
}

// formHint is the guidance note of the open form.
func formHint(h *harness) string {
	h.t.Helper()
	if h.model.form == nil {
		h.t.Fatal("no form is open")
	}
	for _, fl := range h.model.form.fields {
		if fl.kind == fieldNote && fl.label == "hint" {
			return fl.value
		}
	}
	return ""
}

// openInvite goes to Household, which the account owns, and opens the
// invite form.
func openInvite(t *testing.T, f *fakeAPI) *harness {
	t.Helper()
	h := newHarness(t, f)
	h.press("2", "j", "a")
	if h.model.layer != layerForm || h.model.form.id != formInvite {
		t.Fatalf("layer = %v, want the invite form", h.model.layer)
	}
	return h
}

func TestInviteByUsernameSendsOneIdentifier(t *testing.T) {
	f := sampleAPI()
	h := openInvite(t, f)
	h.mustContain("Invite to Household")
	h.typeText("@sam")
	h.press("enter")

	if len(f.invites) != 1 || len(f.invites[0]) != 1 || f.invites[0]["username"] != "@sam" {
		t.Fatalf("invitations sent = %v, want exactly {username: @sam}", f.invites)
	}
	if h.model.layer != layerNone {
		t.Error("the form should close once the invitation is sent")
	}
	// Membership starts on acceptance, so the words must not say it has.
	h.mustContain("invitation sent to @sam")
	h.mustContain("once they accept")
	if strings.Contains(h.model.toastText(), "added") {
		t.Errorf("toast = %q, nobody has been added yet", h.model.toastText())
	}
}

func TestInviteByEmail(t *testing.T) {
	f := sampleAPI()
	h := openInvite(t, f)
	h.typeText("sam@example.com")
	h.press("enter")
	if len(f.invites) != 1 || len(f.invites[0]) != 1 || f.invites[0]["email"] != "sam@example.com" {
		t.Fatalf("invitations sent = %v, want exactly {email}", f.invites)
	}
	h.mustContain("invitation sent to sam@example.com")
}

func TestInviteByPickingAPerson(t *testing.T) {
	f := sampleAPI()
	f.people = []api.Person{
		{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", SharedListIDs: []string{"l1"}},
		{UserID: "u3", Username: nil, Email: "j***@example.com", SharedListIDs: []string{"l1"}},
		// Already in Household, so there is nothing to invite them to.
		{UserID: "u4", Username: strp("already"), Email: "a***@example.com", SharedListIDs: []string{"l2"}},
	}
	h := openInvite(t, f)
	h.mustContain("(type below)")
	h.mustNotContain("already")

	h.press("up", "right") // the picker sits above the text, and the first person
	h.mustContain("@sam")
	h.press("enter")
	if len(f.invites) != 1 || len(f.invites[0]) != 1 || f.invites[0]["userId"] != "u2" {
		t.Fatalf("invitations sent = %v, want exactly {userId: u2}", f.invites)
	}
	h.mustContain("invitation sent to @sam")

	// Someone with no username is offered by the masked email.
	h.press("a", "up", "right", "right")
	h.mustContain("j***@example.com")
}

func TestInviteRefusesTypingAndPickingAtOnce(t *testing.T) {
	f := sampleAPI()
	f.people = []api.Person{{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", SharedListIDs: []string{"l1"}}}
	h := openInvite(t, f)
	h.press("up", "right", "down")
	h.typeText("@kim")
	h.press("enter")

	if len(f.invites) != 0 {
		t.Fatalf("sent %v, want nothing while two people are named", f.invites)
	}
	if h.model.layer != layerForm {
		t.Fatal("the form should come back")
	}
	if got := formAlert(h); !strings.Contains(got, "not both") {
		t.Errorf("alert = %q", got)
	}
	if got := h.model.form.valueOf("who"); got != "@kim" {
		t.Errorf("form holds %q, want the text kept", got)
	}
}

func TestInviteNeedsSomeone(t *testing.T) {
	f := sampleAPI()
	h := openInvite(t, f)
	h.press("enter")
	if len(f.invites) != 0 || h.model.layer != layerForm {
		t.Fatalf("sent %v on layer %v, want the form back and nothing sent", f.invites, h.model.layer)
	}
	if got := formAlert(h); !strings.Contains(got, "Type a username or email") {
		t.Errorf("alert = %q", got)
	}
}

func TestInviteRefusalsKeepTheTypedText(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail apiFailure
		want string
	}{
		{"unknown", apiFailure{status: 404, code: "user_not_found", msg: "no such user"}, "No Evado account matches"},
		{"member", apiFailure{status: 409, code: "already_member", msg: "already"}, "already in this list"},
		{"self", apiFailure{status: 400, code: "invite_self", msg: "self"}, "your own account"},
		{"frozen", apiFailure{status: 403, code: "list_read_only", msg: "ro"}, "frozen"},
		{"personal", apiFailure{status: 400, code: "personal_list", msg: "p"}, "personal list cannot be shared"},
		{"permission", apiFailure{status: 403, code: "forbidden", msg: "only the owner can invite"}, "only the owner can invite"},
		{"limit", apiFailure{status: 429, code: "rate_limited", msg: "slow", retryAfter: "7200"}, "limit of 30 invitations a day. Try again in 2 hours."},
		{"gone", apiFailure{status: 404, code: "not_found", msg: "nope"}, "list was not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleAPI()
			fail := tc.fail
			f.inviteErr = &fail
			h := openInvite(t, f)
			h.typeText("@sam")
			h.press("enter")

			if h.model.layer != layerForm || h.model.form.id != formInvite {
				t.Fatalf("layer = %v, want the form back after a refusal", h.model.layer)
			}
			if got := formAlert(h); !strings.Contains(got, tc.want) {
				t.Errorf("alert = %q, want it to say %q", got, tc.want)
			}
			if got := h.model.form.valueOf("who"); got != "@sam" {
				t.Errorf("the form holds %q, want the typed text kept", got)
			}
			if strings.Contains(h.model.toastText(), "invitation sent") {
				t.Error("a refusal must not claim the invitation went out")
			}

			// Fixing the cause and sending again works without retyping.
			f.inviteErr = nil
			h.press("enter")
			if len(f.invites) != 2 || f.invites[1]["username"] != "@sam" {
				t.Fatalf("invitations = %v, want the same text sent again", f.invites)
			}
			h.mustContain("invitation sent to @sam")
		})
	}
}

func TestInviteRepeatSaysNothingAboutDeclines(t *testing.T) {
	f := sampleAPI()
	h := openInvite(t, f)
	h.typeText("@sam")
	h.press("enter")
	first := h.model.toastText()

	// The service answers the same 201 for a repeat, whatever became of
	// the first one, and the words must be the same too.
	h.press("a")
	h.typeText("@sam")
	h.press("enter")
	if h.model.toastText() != first {
		t.Errorf("repeat said %q, first said %q; they must not differ", h.model.toastText(), first)
	}
	for _, word := range []string{"declin", "already", "again", "ignored"} {
		if strings.Contains(strings.ToLower(first), word) {
			t.Errorf("toast %q hints at %q", first, word)
		}
	}
}

func TestInviteStillWorksWhenPeopleCannotBeLoaded(t *testing.T) {
	f := sampleAPI()
	f.peopleErr = &apiFailure{status: 500, code: "internal", msg: "boom"}
	h := openInvite(t, f)
	h.mustContain("Could not load the people")
	h.typeText("@sam")
	h.press("enter")
	if len(f.invites) != 1 {
		t.Fatalf("invitations = %v, want the typed invitation to go out", f.invites)
	}
}

func TestInviteOfflineSaysWhy(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("2", "j")
	h.model.invite = &inviteDraft{listID: "l2", listName: "Household"}
	h.send(peopleMsg{listID: "l2", err: errors.New("dial tcp: connection refused")})
	h.run(h.cmds...)
	if h.model.layer == layerForm {
		t.Error("the form should not open when there is no connection")
	}
	h.mustContain("needs a connection")
}

func TestInviteOfflineAfterTypingKeepsTheText(t *testing.T) {
	f := sampleAPI()
	h := openInvite(t, f)
	h.typeText("@sam")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlS})
	h.cmds = nil // the request is not made: the connection drops first
	h.send(invitedMsg{listID: "l2", label: "@sam", err: &api.Error{StatusCode: 503, Code: api.CodeOffline, Message: "could not reach Evado"}})
	h.run(h.cmds...)
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the form back", h.model.layer)
	}
	if got := formAlert(h); !strings.Contains(got, "needs a connection") {
		t.Errorf("alert = %q", got)
	}
	if got := h.model.form.valueOf("who"); got != "@sam" {
		t.Errorf("form holds %q, want the text kept", got)
	}
}

func TestInviteGatesStayInPlace(t *testing.T) {
	f := sampleAPI()
	f.lists = append(f.lists, api.List{ID: "l4", Name: "Club", Role: "member"})
	h := newHarness(t, f)

	// The personal list cannot be shared at all.
	h.press("2", "a")
	if h.model.layer == layerForm {
		t.Error("the personal list must not offer an invitation")
	}
	h.mustContain("personal list cannot be shared")

	// A list you only belong to keeps the owner-only rule: the spec does
	// not say who may invite, so the client does not guess.
	h.press("G", "k", "a")
	if h.model.layer == layerForm {
		t.Error("a plain member should not get the invite form")
	}
	h.mustContain("only the owner can invite")

	// A frozen list takes nobody new.
	h.press("G", "a")
	if h.model.layer == layerForm {
		t.Error("a frozen list must not offer an invitation")
	}
	h.mustContain("frozen")
	if len(f.invites) != 0 {
		t.Errorf("sent %v, want nothing", f.invites)
	}
}

func TestGuestCannotInvite(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.press("2", "n")
	h.typeText("Club")
	h.press("enter")
	h.press("G", "a")
	if h.model.layer == layerForm {
		t.Fatal("a computer with no account must not open the invite form")
	}
	h.mustContain("needs an Evado account")
}

func TestMembersAreNamedByUsernameWithEmailFallback(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Username: strp("demo"), Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", Role: "member"},
		{UserID: "u3", Username: nil, Email: "j***@example.com", Role: "member"},
	}
	h := newHarness(t, f)
	h.press("2", "j", "m")
	h.mustContain("@sam")
	h.mustContain("@demo")
	h.mustContain("j***@example.com")

	// The assignee picker and the todo's label use the same names.
	h.press("M", "j", "d")
	h.mustContain("Remove @sam?")
	h.press("esc")
	h.press("esc", "1", "j", "enter", "A")
	h.mustContain("@sam")
	h.mustContain("j***@example.com")
	h.mustNotContain("s***@example.com ")
}

func TestAssigningBySelectedUsername(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Username: strp("demo"), Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", Role: "member"},
	}
	h := newHarness(t, f)
	h.press("2", "j", "m")
	h.press("1", "j", "enter", "A")
	// Anyone, then the owner, then sam.
	h.press("tab", "right", "right", "enter")
	if len(f.patches) != 1 || string(f.patches[0].body["assigneeId"]) != `"u2"` {
		t.Fatalf("patches = %+v, want the todo assigned to u2", f.patches)
	}
}

func TestAssignPickerReadsMembersNothingHasLoaded(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Username: strp("demo"), Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", Role: "member"},
	}
	h := newHarness(t, f)
	// Straight into the unassigned Household todo, with no other screen
	// having read who is in the list.
	h.press("j", "enter", "A")
	if h.model.layer != layerForm || h.model.form.id != formAssignTodo {
		t.Fatalf("layer = %v, want the assignee picker", h.model.layer)
	}
	h.press("tab", "right", "right")
	h.mustContain("@sam")
	h.press("enter")
	if len(f.patches) != 1 || string(f.patches[0].body["assigneeId"]) != `"u2"` {
		t.Fatalf("patches = %+v, want the todo assigned to u2", f.patches)
	}
}

func TestAssignPickerSaysWhenMembersCannotBeRead(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Username: strp("demo"), Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", Role: "member"},
	}
	f.refuse = func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/members") }
	h := newHarness(t, f)
	h.press("j", "enter", "A")
	if got := formHint(h); !strings.Contains(got, "could not be read") {
		t.Errorf("hint = %q, want it to say the members could not be read", got)
	}
	h.mustNotContain("@sam")

	// Once the server answers again, the next try reads them.
	f.refuse = nil
	h.press("esc", "A")
	if got := formHint(h); strings.Contains(got, "could not be read") {
		t.Errorf("hint = %q after the members were read", got)
	}
	h.press("tab", "right", "right")
	h.mustContain("@sam")
}

func TestUsernameShowsAndCanBeSet(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("4")
	h.mustContain("not set")

	h.press("enter")
	if h.model.layer != layerForm || h.model.form.id != formUsername {
		t.Fatalf("layer = %v, want the username form", h.model.layer)
	}
	h.typeText("@Sam")
	reads := f.meReads
	h.press("enter")

	// Only the username is sent, so the timezone cannot change with it.
	if len(f.meBodies) != 1 || f.meBodies[0] != `{"username":"@Sam"}` {
		t.Fatalf("PATCH /me bodies = %v, want only the username", f.meBodies)
	}
	if h.model.user.Username == nil || *h.model.user.Username != "sam" {
		t.Fatalf("user.Username = %v, want the normalised name the service returned", h.model.user.Username)
	}
	h.mustContain("@sam")
	h.mustContain("your username is now @sam")
	if h.model.user.TZ != "UTC" {
		t.Errorf("timezone = %q, want it left alone", h.model.user.TZ)
	}
	if f.meReads <= reads {
		t.Error("the account was not read again after the change")
	}
}

func TestUsernameIsPrefilledAndUnchangedIsNotSent(t *testing.T) {
	f := sampleAPI()
	f.username = strp("sam")
	h := newHarness(t, f)
	h.press("4", "enter")
	if got := h.model.form.valueOf("username"); got != "sam" {
		t.Fatalf("form holds %q, want the current username", got)
	}
	h.press("enter")
	if len(f.meBodies) != 0 {
		t.Errorf("sent %v for an unchanged username", f.meBodies)
	}
}

func TestUsernameCanBeCleared(t *testing.T) {
	f := sampleAPI()
	f.username = strp("sam")
	h := newHarness(t, f)
	h.press("4")
	h.mustContain("@sam")

	h.press("x")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, removing a username should ask first", h.model.layer)
	}
	h.press("enter")
	if len(f.meBodies) != 1 || f.meBodies[0] != `{"username":null}` {
		t.Fatalf("PATCH /me bodies = %v, want an explicit null", f.meBodies)
	}
	if h.model.user.Username != nil {
		t.Errorf("user.Username = %q after clearing", *h.model.user.Username)
	}
	h.mustContain("not set")
	h.mustContain("username removed")

	h.press("x")
	h.mustContain("no username to remove")
}

func TestEmptyUsernameFieldAsksToClear(t *testing.T) {
	f := sampleAPI()
	f.username = strp("sam")
	h := newHarness(t, f)
	h.press("4", "enter")
	for range "sam" {
		h.press("bs")
	}
	h.press("enter")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, an empty username should ask before removing", h.model.layer)
	}
}

func TestUsernameRefusalsComeFromTheServerAndKeepTheText(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{
		{"taken", "That username is already taken"},
		{"no", "Usernames are 3 to 20 characters"},
	} {
		f := sampleAPI()
		h := newHarness(t, f)
		h.press("4", "enter")
		h.typeText(tc.typed)
		h.press("enter")

		if h.model.layer != layerForm || h.model.form.id != formUsername {
			t.Fatalf("%s: layer = %v, want the form back", tc.typed, h.model.layer)
		}
		if got := formAlert(h); !strings.Contains(got, tc.want) {
			t.Errorf("%s: alert = %q, want the server's words %q", tc.typed, got, tc.want)
		}
		if got := h.model.form.valueOf("username"); got != tc.typed {
			t.Errorf("%s: form holds %q, want the text kept", tc.typed, got)
		}
		if f.username != nil || h.model.user.Username != nil {
			t.Errorf("%s: a refused username must change nothing", tc.typed)
		}
	}
}

func TestTimezoneEditLeavesTheUsernameAlone(t *testing.T) {
	f := sampleAPI()
	f.username = strp("sam")
	h := newHarness(t, f)
	h.press("4", "j")
	h.press("enter")
	if h.model.form == nil || h.model.form.id != formTimezone {
		t.Fatalf("layer = %v, want the timezone form", h.model.layer)
	}
	h.press("ctrl+u")
	h.typeText("Europe/Madrid")
	h.press("enter")
	if len(f.meBodies) != 1 || f.meBodies[0] != `{"tz":"Europe/Madrid"}` {
		t.Errorf("PATCH /me bodies = %v, want only the timezone", f.meBodies)
	}
	if f.username == nil || *f.username != "sam" {
		t.Error("the username changed with the timezone")
	}
}

func TestUsernameNeedsAConnection(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("4", "enter")
	h.typeText("sam")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlS})
	h.cmds = nil
	h.send(usernameMsg{username: strp("sam"), err: &api.Error{StatusCode: http.StatusServiceUnavailable, Code: api.CodeOffline}})
	h.run(h.cmds...)
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the form back", h.model.layer)
	}
	if got := formAlert(h); !strings.Contains(got, "needs a connection") {
		t.Errorf("alert = %q", got)
	}
	if got := h.model.form.valueOf("username"); got != "sam" {
		t.Errorf("form holds %q, want the text kept", got)
	}
}

func TestGuestCannotSetAUsername(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.press("4", "enter")
	if h.model.layer == layerForm {
		t.Fatal("a computer with no account must not open the username form")
	}
	h.mustContain("needs an Evado account")
}

func TestPaletteFindsInviteAndUsername(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("2", "j", "1", ":")
	h.typeText("invite someone")
	h.press("enter")
	if h.model.tab != tabLists || h.model.layer != layerForm || h.model.form.id != formInvite {
		t.Errorf("tab %v layer %v, want the invite form on Lists", h.model.tab, h.model.layer)
	}

	h.press("esc", ":")
	h.typeText("set your username")
	h.press("enter")
	if h.model.layer != layerForm || h.model.form.id != formUsername {
		t.Errorf("layer = %v, want the username form", h.model.layer)
	}
}
