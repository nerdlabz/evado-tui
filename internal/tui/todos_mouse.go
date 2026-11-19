package tui

import tea "github.com/charmbracelet/bubbletea"

func (v *todosView) clickSide(i int) tea.Cmd {
	items := v.sideItems()
	if i >= len(items) {
		return nil
	}
	v.side = i
	v.m.focus = focusList
	return v.applySide(v.m, items[i])
}

func (v *todosView) clickRow(i int, double bool) tea.Cmd {
	if i >= len(v.lines) || v.lines[i].header {
		return nil
	}
	v.m.focus = focusList
	v.rows.cursor = i
	v.rows.clamp()
	if double {
		return v.m.pressKey("enter")
	}
	return nil
}

func (v *todosView) clickCard(id string, double bool) tea.Cmd {
	if !v.boardFind(id) {
		return nil
	}
	v.m.focus = focusList
	if double {
		return v.m.pressKey("enter")
	}
	return nil
}

func (v *todosView) selectTodo(id string) bool {
	if v.filter.board {
		return v.boardFind(id)
	}
	for i, l := range v.lines {
		if !l.header && v.items[l.index].ID == id {
			v.rows.cursor = i
			v.rows.clamp()
			return true
		}
	}
	return false
}

func (v *todosView) clickMark(id string) tea.Cmd {
	m := v.m
	if m.inDetail() && v.detail.ID == id {
		m.focus = focusDetail
		return m.pressKey("x")
	}
	if !v.selectTodo(id) {
		return nil
	}
	m.focus = focusList
	return v.toggleSelected(m)
}

func (v *todosView) wheelMain(delta int) tea.Cmd {
	v.m.focus = focusList
	return v.m.wheelKeys(delta)
}

func (v *todosView) clickDetail(bool) tea.Cmd {
	m := v.m
	if m.inDetail() {
		m.focus = focusDetail
		return nil
	}
	if v.selected() == nil {
		return nil
	}
	m.focus = focusList
	return m.pressKey("enter")
}

func (v *todosView) clickStep(i int, double bool) tea.Cmd {
	if i >= len(v.steps) {
		return nil
	}
	v.m.focus = focusChecklist
	v.checklist.cursor = i
	v.checklist.clamp()
	if double {
		return v.m.pressKey("e")
	}
	return nil
}

func (v *todosView) clickStepMark(i int) tea.Cmd {
	if i >= len(v.steps) {
		return nil
	}
	v.clickStep(i, false)
	return v.m.pressKey("x")
}
