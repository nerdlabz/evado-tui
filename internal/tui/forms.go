package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/config"
)

// form ids used by applyForm.
const (
	formNewTodo    = "todo.new"
	formEditTodo   = "todo.edit"
	formNewList    = "list.new"
	formRenameList = "list.rename"
	formInvite     = "member.invite"
	formJoin       = "list.join"
	formNewStep    = "step.new"
	formRenameStep = "step.rename"
	formTimezone   = "account.timezone"
	formUsername   = "account.username"
	formExport     = "account.export"
	formImport     = "account.import"
	formDeleteAcct = "account.delete"
	formNewToken   = "token.new"
	formNewChannel = "channel.new"
	formQuickAdd   = "todo.quick"
	formAssignTodo = "todo.assign"
	formMoveTodos  = "todo.move"
	formListFilter = "todo.listfilter"

	formSaveView     = "view.save"
	formRenameView   = "view.rename"
	formViewMenu     = "view.menu"
	formRepairView   = "view.repair"
	formPersonFilter = "todo.personfilter"
)

// cfgPath is the file the token is stored in.
func (m *Model) cfgPath() (string, error) { return config.Path() }

// applyForm turns a submitted form into API calls.
func (m *Model) applyForm(res formResult) (tea.Model, tea.Cmd) {
	v := res.values
	switch res.id {
	case formNewTodo, formQuickAdd:
		return m, m.createTodo(v)
	case formEditTodo:
		return m, m.editTodo(v, res.orig)
	case formNewStep:
		return m, m.addStep(v)
	case formRenameStep:
		return m, m.renameStep(v)
	case formNewList:
		name := strings.TrimSpace(v["name"])
		if name == "" {
			return m, m.warnf("a list needs a name")
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("creating list"), createList(m.ctx, m.client, name))
	case formRenameList:
		l := m.listsV.selected()
		if l == nil {
			return m, m.warnf("select a list first")
		}
		name := strings.TrimSpace(v["name"])
		if name == "" {
			return m, m.warnf("a list needs a name")
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("renaming"), renameList(m.ctx, m.client, l.ID, name))
	case formInvite:
		return m, m.submitInvite(v)
	case formJoin:
		code := strings.TrimSpace(v["code"])
		if code == "" {
			return m, m.warnf("paste the invite code or URL")
		}
		if i := strings.LastIndex(code, "/"); i >= 0 {
			code = code[i+1:]
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("joining"), joinList(m.ctx, m.client, code))
	case formUsername:
		return m, m.acct.submitUsername(m, v["username"])
	case formTimezone:
		tz := strings.TrimSpace(v["tz"])
		if tz == "" {
			return m, m.warnf("timezone cannot be empty")
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return m, m.warnf("%q is not a known IANA timezone", tz)
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("saving timezone"), m.saveTimezone(tz), m.fetchMe())
	case formExport:
		path := strings.TrimSpace(v["path"])
		m.busy = true
		if m.exportsFromComputer() {
			return m, tea.Batch(m.setBusy("exporting"), exportComputer(m.backend, path))
		}
		return m, tea.Batch(m.setBusy("exporting"), exportAccount(m.ctx, directClient(m.remote()), path))
	case formImport:
		path := strings.TrimSpace(v["path"])
		if path == "" {
			return m, m.warnf("choose an export file")
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("previewing import"), importFile(m, path, nil, true))
	case formDeleteAcct:
		confirm := strings.TrimSpace(v["confirmEmail"])
		if !strings.EqualFold(confirm, m.user.Email) {
			return m, m.warnf("that email does not match; type %q exactly", m.user.Email)
		}
		return m, m.ask("delete-account", "Permanently delete the account?",
			fmt.Sprintf("%s and its personal list will be gone. Shared lists you own stay read-only for their members.", m.user.Email),
			true,
			func() tea.Cmd { return deleteAccount(m.ctx, m.client, m.user.Email) })
	case formNewToken:
		return m, m.warnf("minting tokens needs a browser session; use the web app")
	case formAssignTodo:
		return m, m.assignTodo(v)
	case formMoveTodos:
		return m, m.todos.bulkMove(m, v["list"])
	case formListFilter:
		return m, m.todos.applyListFilter(m, v["list"])
	case formPersonFilter:
		return m, m.todos.applyPersonFilter(m, v["person"])
	case formSaveView:
		return m, m.todos.saveView(m, v["name"])
	case formRenameView:
		return m, m.todos.renameView(m, res.orig["name"], v["name"])
	case formViewMenu:
		return m, m.todos.applyViewMenu(m, v["view"], v["do"])
	case formRepairView:
		return m, m.todos.applyRepair(m, res.orig["name"], v)
	case formNewChannel:
		return m, m.warnf("channels need a browser session; use the web app")
	}
	return m, nil
}
