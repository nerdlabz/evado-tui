package tui

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// -- results ----------------------------------------------------------------

func (m *Model) onResult(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case toastMsg:
		m.toast = &toast{kind: msg.kind, text: msg.text}
		m.toastAge = 0
		return m, nil

	case smartParsingMsg:
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "saving smarter dates")
		}
		m.user.SmartParsing = msg.user.SmartParsing
		return m, m.okf("smarter dates saved")

	case meMsg:
		// An answer from before a sign-in ended, or a read that a newer
		// one has overtaken, would put back what has since replaced it:
		// another account, or the timezone from before a change. A newer
		// read is still on its way and clears the busy state.
		if msg.seq < m.meFloor || (!msg.write && msg.seq != m.meSeq) {
			return m, nil
		}
		if msg.err != nil {
			m.done()
			return m, m.failf(msg.err, "")
		}
		if msg.user.ID != m.user.ID {
			// The views on screen were the last account's, or nobody's.
			m.todos.dropViewState()
		}
		if msg.write {
			// Reads sent while the change was on its way may have looked
			// before it landed.
			m.meSeq++
		}
		m.user = msg.user
		if msg.lists != nil {
			m.lists = msg.lists
		}
		m.setLoc(msg.user.TZ)
		m.ready = true
		m.done()
		return m, m.todos.dropMissingList(m)

	case todosMsg:
		if msg.snap {
			if msg.seq == m.todos.snapSeq {
				m.todos.onSnapshot(msg)
				m.todos.keepSteps(msg.steps)
			}
			return m, nil
		}
		// A reply to a load that has since been replaced is stale: the
		// newer one is still on its way and will clear the busy state.
		if msg.seq != 0 && msg.seq != m.todos.seq {
			return m, nil
		}
		m.done()
		if msg.unfiltered {
			m.todos.tooBig = errors.Is(msg.err, errAccountTooBig)
		}
		if errors.Is(msg.err, errAccountTooBig) {
			// The narrowed views read a page at a time, so they still work.
			return m, toastCmd(toastError, "%s; F shows one list, / searches", msg.err)
		}
		if msg.err != nil {
			return m, m.failf(msg.err, "load todos")
		}
		if m.layer != layerSearch {
			m.todos.live = ""
		}
		m.todos.keepSteps(msg.steps)
		if msg.more {
			// The cursor was for the filters it was asked under; under any
			// other, the page does not belong after what is on screen.
			if msg.of != m.todos.loadedFor {
				return m, nil
			}
			m.todos.addPage(msg.todos, msg.next)
			return m, m.todos.settleReveal(m)
		}
		m.todos.setItems(msg.todos)
		m.todos.next, m.todos.loadedFor = msg.next, msg.of
		m.todos.whole = msg.unfiltered
		m.todos.cursor = ""
		if msg.unfiltered {
			m.todos.cursor = msg.cursor
			m.todos.snapCursor = msg.cursor
		}
		return m, m.todos.settleReveal(m)

	case feedMsg:
		if msg.snap {
			if msg.seq == m.todos.snapSeq {
				m.todos.onSnapshotFeed(msg)
				m.todos.keepFeedSteps(msg.changes)
			}
			return m, nil
		}
		if msg.seq != m.todos.seq {
			return m, nil
		}
		m.done()
		if msg.err != nil {
			if api.CodeOf(msg.err) == api.CodeUnauthorized {
				return m, m.failf(msg.err, "")
			}
			// The feed could not answer, so start over from a full load
			// rather than leave the list quietly out of date.
			m.todos.cursor = ""
			return m, m.todos.load()
		}
		if m.layer != layerSearch {
			m.todos.live = ""
		}
		m.todos.applyFeed(msg.changes)
		m.todos.keepFeedSteps(msg.changes)
		if msg.cursor == "" {
			// Without a cursor the feed cannot continue, so the next
			// refresh reads the account again.
			m.todos.cursor = ""
			return m, m.todos.settleReveal(m)
		}
		m.todos.cursor = msg.cursor
		m.todos.snapCursor = msg.cursor
		return m, m.todos.settleReveal(m)

	case mutationMsg:
		return m, m.onMutation(msg)

	case membersForMsg:
		if msg.name == "" {
			return m, m.todos.onPeople(msg)
		}
		return m, m.todos.onMembers(msg)

	case assigneesMsg:
		return m, m.todos.onAssignees(msg)

	case settledMsg:
		m.done()
		return m, msg.toast

	case memberCacheMsg:
		m.acct.memberCache[msg.listID] = msg.members
		return m, nil

	case editorMsg:
		return m, m.onEditor(msg)

	case todoMsg:
		m.settleUndo(msg)
		m.done()
		if msg.err != nil {
			// The list may be showing the outcome ahead of the server, so
			// reload to put it back the way it really is.
			return m, tea.Batch(m.failf(msg.err, msg.action.failed()), m.todos.load())
		}
		m.recordUndo(msg)
		if msg.action == actionDeleted {
			// The detail pane has nothing left to show.
			m.todos.detail = api.Todo{}
			m.todos.setSteps(nil)
			m.focus = focusList
			return m, tea.Batch(
				m.todos.refreshAfter(m, msg),
				toastCmd(toastOK, "deleted %q. Press z to undo.", truncate(msg.title, 40)),
			)
		}
		// Bring the list up to date, then put the cursor on what changed:
		// an action whose result you cannot see reads as a failed action.
		m.todos.stage(msg)
		cmds := []tea.Cmd{m.todos.refreshAfter(m, msg)}
		if msg.todo.ID != "" && m.inDetail() && msg.todo.ID == m.todos.pane.id {
			// The detail pane is showing this todo, so refresh it too.
			cmds = append(cmds, fetchTodo(m.ctx, m.client, m.todos.pane))
		}
		return m, tea.Batch(cmds...)

	case detailMsg:
		// The pane has moved to another todo, or closed, since this was
		// asked for; neither the todo nor a failure to fetch it matters now.
		if !m.paneWants(msg.req) {
			return m, nil
		}
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "load todo")
		}
		m.todos.detail = msg.todo
		cmds := []tea.Cmd{fetchChecklist(m.ctx, m.client, msg.req)}
		if msg.todo.AssigneeID != nil {
			if _, ok := m.acct.memberCache[msg.todo.ListID]; !ok {
				// Needed to show who the todo is assigned to by name.
				cmds = append(cmds, fetchMemberCache(m.ctx, m.client, msg.todo.ListID))
			}
		}
		return m, tea.Batch(cmds...)

	case checklistsMsg:
		if !msg.edited && !m.paneWants(msg.req) {
			return m, nil
		}
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "checklist")
		}
		// A step changed on a todo that is no longer open must not fill
		// the checklist of the one that is.
		if msg.req.id == m.todos.pane.id {
			m.todos.setSteps(msg.items)
		}
		if msg.err == nil {
			m.todos.keepStepsOf(msg.req.id, msg.items)
		}
		return m, nil

	case listsMsg:
		m.done()
		if msg.left != "" {
			// The list is gone from here whether or not the lists could be
			// read again, and so are its members.
			delete(m.acct.memberCache, msg.left)
			if m.listsV.membersFor == msg.left {
				m.listsV.showMembers, m.listsV.members = false, nil
				if m.focus == focusMembers {
					m.focus = focusList
				}
			}
		}
		if msg.err != nil && msg.left != "" && api.CodeOf(msg.err) != api.CodeUnauthorized {
			return m, m.warnf("left the list, but the lists could not be read again: %s", errText(msg.err))
		}
		if msg.err != nil {
			return m, m.failf(msg.err, "lists")
		}
		if msg.lists != nil {
			m.lists = msg.lists
		}
		m.listsV.setItems(m.lists)
		if msg.left != "" {
			return m, tea.Batch(m.todos.dropMissingList(m), m.okf("left the list"))
		}
		if msg.joined != nil {
			return m, m.okf("joined %q", msg.joined.Name)
		}
		return m, nil

	case membersMsg:
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "members")
		}
		m.listsV.members = msg.members
		m.listsV.membersFor = msg.listID
		m.listsV.showMembers = true
		m.acct.memberCache[msg.listID] = msg.members
		return m, nil

	case invitationsMsg:
		return m, m.listsV.inbox.onLoaded(msg)

	case answeredMsg:
		return m, m.listsV.inbox.onAnswered(msg)

	case usernameMsg:
		return m, m.acct.onUsername(msg)

	case peopleMsg:
		return m, m.onPeople(msg)

	case invitedMsg:
		return m, m.onInvited(msg)

	case inviteMsg:
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "invite")
		}
		m.listsV.invite = &msg.invite
		return m, m.copyToClipboard(msg.invite.InviteURL, "invite link copied")
	case syncMsg:
		return m, m.syncV.onResult(msg)
	case parseMsg:
		return m, m.todos.onParse(msg)
	case exportMsg:
		m.done()
		return m, m.acct.onExport(msg)
	case importMsg:
		m.done()
		if msg.err != nil {
			return m, tea.Batch(m.restoreImportForm(msg.path), m.failf(msg.err, "import"))
		}
		m.acct.imported = &msg.result
		if msg.preview {
			return m, m.askOr("import", "Import these todos?", importSummary(msg.result)+" Apply this file?", false, func() tea.Cmd {
				m.busy = true
				return tea.Batch(m.setBusy("importing"), importFile(m, msg.path, msg.data, false))
			}, func() tea.Cmd { return m.restoreImportForm(msg.path) })
		}
		if !msg.result.Complete {
			return m, tea.Batch(m.refreshAll(true), m.autoSync(), m.restoreImportForm(msg.path), m.warnf("Import incomplete: %d remaining. Rerun this file explicitly to continue.", msg.result.Remaining))
		}
		return m, tea.Batch(m.refreshAll(true), m.autoSync(), m.okf("%s", importSummary(msg.result)))
	case accountDeletedMsg:
		m.done()
		if msg.err != nil {
			return m, m.failf(msg.err, "delete account")
		}
		m.busy = true
		return m, tea.Batch(m.setBusy("signing out"), forgetDeleted(m.ctx, m.cfg, m.backend, m.session, msg.frozen))
	case tokensMsg:
		m.done()
		return m, m.acct.onTokens(msg)
	case channelsMsg:
		m.done()
		return m, m.acct.onChannels(msg)
	case copyMsg:
		return m, toastCmd(toastOK, "sent to the terminal clipboard: %s", truncate(msg.text, 48))
	}
	return m, nil
}
