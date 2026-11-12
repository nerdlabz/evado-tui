package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Answers come back in whatever order the network or the store gives them,
// not the order the requests were made in. Every reply that can arrive after
// the user has moved on carries what it was asked for, so it can be matched
// against what is on screen now instead of being applied blindly.

// paneReq names a request for what the detail pane shows: the todo it is
// for, and which opening of the pane asked. A todo opened twice gets a new
// gen each time, so an answer to the first opening is recognised as old.
type paneReq struct {
	id  string
	gen int
}

// openDetail shows a todo in the detail pane and asks for it. Its steps are
// asked for when the todo arrives, as they are after any change to it, so
// asking for them here as well would read them twice.
// A different todo from the last one starts from a clean pane, with only
// what the list already knows about it, so nothing of the last todo can be
// shown or acted on under the new one's name while the answers are coming.
func (v *todosView) openDetail(m *Model, id string) tea.Cmd {
	v.pane = paneReq{id: id, gen: v.pane.gen + 1}
	if v.detail.ID != id {
		v.detail = api.Todo{}
		for _, t := range v.all {
			if t.ID == id {
				v.detail = t
			}
		}
		v.setSteps(nil)
	}
	m.focus = focusDetail
	return fetchTodo(m.ctx, m.client, v.pane)
}

// paneWants reports whether an answer is for the pane as it is now. The
// pane can be closed from many places, so being closed is read from the
// focus rather than recorded at each of them.
func (m *Model) paneWants(r paneReq) bool {
	return m.inDetail() && r == m.todos.pane
}

// mutations sends the changes to any one todo one at a time, in the order
// they were made. Two requests sent a moment apart can be carried out the
// other way round, by the server or by the store on this computer, and a
// completion that lands after the reopening meant to follow it leaves the
// todo done. Changes to different todos still go out together.
//
// A bulk action is one change to several todos. It takes its turn on each
// of them: it waits until every one is free and holds them all until it is
// answered, so it can neither overtake nor be overtaken by a change made to
// one of them alone. Every todo's line is in the one order changes were
// made in, so two changes can never each be waiting for the other.
type mutations struct {
	// n numbers every change, to any todo, in the order it was made.
	n int
	// lines holds, for each todo with a change on its way, its changes in
	// the order they were made. The first is the one sent, or the next to
	// go once the other todos it names are free as well.
	lines map[string][]*mutation
	// sent finds a change on its way by its number, when it is answered.
	sent map[int]*mutation
}

// mutation is one change waiting its turn, or on its way.
type mutation struct {
	order int
	ids   []string
	cmd   tea.Cmd
}

// mutationMsg is the answer to a change sent through mutations.
type mutationMsg struct {
	order int
	msg   tea.Msg
}

// issue takes the next number in the order changes were made.
func (q *mutations) issue() int {
	q.n++
	return q.n
}

// send numbers a change to the todos in ids and sends it, or holds it while
// an earlier change to any of them is still unanswered.
func (q *mutations) send(ids []string, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	order := q.issue()
	// A todo named twice would stand in its own way.
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	mu := &mutation{order: order, ids: ids}
	mu.cmd = func() tea.Msg { return mutationMsg{order: order, msg: cmd()} }
	if q.lines == nil {
		q.lines, q.sent = map[string][]*mutation{}, map[int]*mutation{}
	}
	for _, id := range ids {
		q.lines[id] = append(q.lines[id], mu)
	}
	return q.start(mu)
}

// start sends a change that is first in line for every todo it names.
func (q *mutations) start(mu *mutation) tea.Cmd {
	if q.sent[mu.order] != nil {
		return nil
	}
	for _, id := range mu.ids {
		if q.lines[id][0] != mu {
			return nil
		}
	}
	q.sent[mu.order] = mu
	return mu.cmd
}

// finish is called with each answer. It hands back the changes held behind
// this one that can go now, and reports whether any todo it named has a
// later change: if so, the answer has been overtaken by a change the user
// has already made.
func (q *mutations) finish(order int) (next tea.Cmd, overtaken bool) {
	mu := q.sent[order]
	if mu == nil {
		return nil, false
	}
	delete(q.sent, order)
	var cmds []tea.Cmd
	for _, id := range mu.ids {
		held := q.lines[id][1:]
		if len(held) == 0 {
			delete(q.lines, id)
			continue
		}
		q.lines[id] = held
		overtaken = true
		cmds = append(cmds, q.start(held[0]))
	}
	return tea.Batch(cmds...), overtaken
}

// mutate sends a change to one todo through the queue.
func (m *Model) mutate(id string, cmd tea.Cmd) tea.Cmd {
	return m.muts.send([]string{id}, cmd)
}

// mutateAll sends one change to several todos through the queue.
func (m *Model) mutateAll(ids []string, cmd tea.Cmd) tea.Cmd {
	return m.muts.send(ids, cmd)
}

// onMutation handles the answer to a queued change, and sends whatever was
// held behind it.
func (m *Model) onMutation(msg mutationMsg) tea.Cmd {
	next, overtaken := m.muts.finish(msg.order)
	if b, ok := msg.msg.(bulkMsg); ok {
		return tea.Batch(next, m.onBulk(b, overtaken))
	}
	r, ok := msg.msg.(todoMsg)
	if !overtaken {
		if ok {
			r.order = msg.order
			msg.msg = r
		}
		_, cmd := m.onResult(msg.msg)
		return tea.Batch(next, cmd)
	}
	// A later change to this todo is on its way. The list already shows
	// what it will do and its answer brings the server's final word, so
	// this one must not paint over it. It still takes its place in the
	// history, or says that it failed.
	if !ok {
		// Create and delete report failure as a failed load.
		if l, isLoad := msg.msg.(todosMsg); isLoad && l.err != nil {
			return tea.Batch(next, m.failf(l.err, "todo"))
		}
		return next
	}
	r.order = msg.order
	m.settleUndo(r)
	if r.err != nil {
		return tea.Batch(next, m.failf(r.err, "todo"))
	}
	m.recordUndo(r)
	return next
}
