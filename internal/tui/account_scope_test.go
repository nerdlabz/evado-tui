package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/auth"
)

// withScope makes the running session one the server granted scope to.
func withScope(t *testing.T, h *harness, scope string) {
	t.Helper()
	store := auth.NewStore(filepath.Join(t.TempDir(), "session.json"))
	h.model.session = auth.Start(store, h.model.cfg.BaseURL, api.Verified{
		TokenPair: api.TokenPair{AccessToken: "a", RefreshToken: "r", Scope: scope, User: api.User{ID: "u1"}},
	})
}

func TestCodeSessionWithoutAccountScopeExportsTodosAndSendsDeleteToTheWebApp(t *testing.T) {
	f := sampleAPI()
	f.noAccountScope = true
	h := newHarness(t, f)
	withScope(t, h, "todos.read todos.write")
	h.press("4")

	h.mustNotContain("DANGER ZONE")
	h.model.acct.selectRow("export")
	h.mustContain("todos.read")
	h.press("enter")
	if h.model.layer != layerForm {
		t.Fatal("portable export form missing")
	}
	h.press("esc")

	h.model.acct.selectRow("delete")
	h.mustContain("account scope")
	h.press("enter")
	if h.model.layer == layerForm {
		t.Fatal("delete asked for the email first")
	}
	if got := h.model.toastText(); !strings.Contains(got, "web app") {
		t.Errorf("toast = %q, want the web app named", got)
	}
	if f.accountCalls != 0 {
		t.Errorf("%d requests were sent that are certain to be refused", f.accountCalls)
	}
}

func TestCodeSessionWithAccountScopeStillExportsAndDeletes(t *testing.T) {
	h := newHarness(t, sampleAPI())
	withScope(t, h, "todos.read todos.write account")
	h.press("4")
	h.mustContain("DANGER ZONE")

	h.model.acct.selectRow("export")
	h.press("enter")
	if h.model.layer != layerForm || h.model.form.id != formExport {
		t.Fatalf("export should open its form, layer = %v", h.model.layer)
	}
	h.press("esc")
	h.model.acct.selectRow("delete")
	h.press("enter")
	if h.model.layer != layerForm || h.model.form.id != formDeleteAcct {
		t.Fatalf("delete should ask for the email, layer = %v", h.model.layer)
	}
}

// A pasted token or an older saved session says nothing about its scope, so
// the actions stay and a refusal reads as advice rather than a raw 403.
func TestUnknownScopeRefusedByTheServerSaysToUseTheWebApp(t *testing.T) {
	for _, name := range []string{"pasted token", "older session"} {
		t.Run(name, func(t *testing.T) {
			f := sampleAPI()
			f.noAccountScope = true
			h := newHarness(t, f)
			if name == "older session" {
				withScope(t, h, "")
			}
			h.press("4")
			h.mustContain("DANGER ZONE")

			h.model.acct.selectRow("export")
			h.press("enter", "enter")
			if f.accountCalls != 0 {
				t.Fatal("portable export used account endpoint")
			}

			h.model.acct.selectRow("delete")
			h.press("enter")
			h.press("demo@example.com", "enter", "enter", "enter")
			if f.accountCalls != 1 {
				t.Fatalf("delete requests = %d, want the attempt to be made", f.accountCalls-1)
			}
			if got := h.model.toastText(); !strings.Contains(got, "web app") {
				t.Errorf("delete toast = %q", got)
			}
		})
	}
}

// The profile has no join date to show until the account has been read,
// and leaves the row out rather than showing the zero time.
func TestProfileLeavesOutTheJoinDateUntilItIsKnown(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.model.tab = tabAccount
	h.model.acct.selectRow("profile")
	h.mustContain("joined")
	h.mustContain("2026")

	h.model.user.CreatedAt = time.Time{}
	h.mustNotContain("joined")
	h.mustNotContain("0001")
}
