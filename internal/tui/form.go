package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/nerdlabz/evado-tui/internal/local"
)

// fieldKind selects the editor a form field uses.
type fieldKind int

const (
	// fieldText is a single-line input.
	fieldText fieldKind = iota
	// fieldArea is a multi-line input for notes.
	fieldArea
	// fieldSelect cycles through fixed options with left/right or space.
	fieldSelect
	// fieldToggle flips between yes and no.
	fieldToggle
	// fieldNote is read-only guidance.
	fieldNote
	// fieldPreview is a read-only line the app fills in while the user
	// types, such as how the server reads the dates in a title.
	fieldPreview
)

// formField is one row of a form.
type formField struct {
	key     string
	kind    fieldKind
	label   string
	value   string
	options []string
	hint    string
	input   textinput.Model
	// area is the multi-line editor used by fieldArea. A textarea is a
	// separate component because notes and quick capture need real
	// newlines, which a single-line input silently drops.
	area textarea.Model
	rows int
	// multiline means Enter inserts a newline here instead of saving.
	// It is set on fields that hold prose or a list of items; a todo
	// title is the common case where Enter should just save.
	multiline bool
	// optional marks a field that may be left empty.
	optional bool
	// alert marks a note as a problem rather than guidance.
	alert bool
	// titles marks a field holding todo or step titles, one per line.
	titles bool
}

// formResult is what a submitted form hands back to the app. orig holds
// the values the form was opened with, so an edit can send only the
// fields that changed.
type formResult struct {
	id     string
	values map[string]string
	orig   map[string]string
	cancel bool
}

// form is a modal, keyboard-driven editor used for every create and
// update in the app.
type form struct {
	id     string
	title  string
	fields []*formField
	focus  int
	width  int
	offset int
	// orig holds the field values as they were when the form opened, so
	// an edit can send only what changed.
	orig map[string]string
	// maxH is the tallest the dialog may grow, set from the terminal size.
	maxH int
	// height of the longest multi-line field, in rows.
	maxRows int
}

// blinkSpeed is the cursor blink rate. Tests shorten it so the suite does
// not wait on the animation.
var blinkSpeed = 530 * time.Millisecond

// newInput builds a text input with the house prompt and blink settings.
func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Cursor.BlinkSpeed = blinkSpeed
	ti.TextStyle = lipgloss.NewStyle().Foreground(colFg)
	ti.PlaceholderStyle = styFaint
	ti.Cursor.Style = styAccent
	return ti
}

func newForm(id, title string, width int) *form {
	return &form{id: id, title: title, width: width}
}

// field appends a single-line field and returns the form for chaining.
func (f *form) field(key, label, value, hint string) *form {
	ti := newInput()
	ti.SetValue(value)
	ti.Placeholder = hint
	ti.CharLimit = 4000
	ti.Width = max(20, f.innerWidth()-len(label)-4)
	f.fields = append(f.fields, &formField{key: key, kind: fieldText, label: label, value: value, hint: hint, input: ti})
	return f
}

// optionalField appends a single-line field that may be left empty.
func (f *form) optionalField(key, label, value, hint string) *form {
	f.field(key, label, value, hint)
	f.fields[len(f.fields)-1].optional = true
	return f
}

// area appends a boxed field. Enter saves, as it does everywhere else, so
// the caret never has to leave the field to commit.
func (f *form) area(key, label, value, placeholder string, rows int) *form {
	return f.boxed(key, label, value, placeholder, rows, false)
}

// prose appends a boxed field for text that really is several lines long.
// Enter adds a line here, so saving moves to ctrl+s.
func (f *form) prose(key, label, value, placeholder string, rows int) *form {
	return f.boxed(key, label, value, placeholder, rows, true)
}

// boxed builds the field both area and prose share.
func (f *form) boxed(key, label, value, placeholder string, rows int, multiline bool) *form {
	ta := textarea.New()
	ta.SetValue(value)
	ta.Placeholder = placeholder
	ta.CharLimit = 8000
	ta.ShowLineNumbers = false
	ta.MaxHeight = rows
	// The default prompt is a vertical rule, which reads as a gutter
	// rather than as part of the field. The form draws its own label.
	// The stock decorations are turned off: the default prompt is a thick
	// left rule and the default cursor line is a reverse-video bar, both of
	// which fight with the form's own label and colours.
	ta.Prompt = ""
	focused, blurred := textarea.DefaultStyles()
	focused.Prompt = lipgloss.NewStyle()
	focused.CursorLine = lipgloss.NewStyle()
	focused.CursorLineNumber = lipgloss.NewStyle()
	focused.Text = lipgloss.NewStyle().Foreground(colFg)
	focused.EndOfBuffer = lipgloss.NewStyle().Foreground(colFaint)
	blurred.Prompt = lipgloss.NewStyle()
	blurred.CursorLine = lipgloss.NewStyle()
	blurred.Text = lipgloss.NewStyle().Foreground(colMuted)
	ta.FocusedStyle = focused
	ta.BlurredStyle = blurred
	ta.Cursor.BlinkSpeed = blinkSpeed
	ta.SetWidth(max(f.innerWidth()-16, 20))
	f.maxRows = max(f.maxRows, rows)
	f.fields = append(f.fields, &formField{
		key: key, kind: fieldArea, label: label, value: value,
		area: ta, rows: rows, multiline: multiline,
	})
	return f
}

// titles marks the last field as holding titles, one per line, so no line
// grows past what the service keeps of a title.
func (f *form) titles() *form {
	f.fields[len(f.fields)-1].titles = true
	return f
}

// fitTitles cuts a line of a titles field that has grown past the longest
// title the service keeps, by the rule it stores titles with. Otherwise a
// pasted title would be shown, and its dates read, in full, and come back
// from the next sync cut short.
func (fl *formField) fitTitles() {
	if !fl.titles {
		return
	}
	value := fl.input.Value()
	if fl.kind == fieldArea {
		value = fl.area.Value()
	}
	lines := strings.Split(value, "\n")
	cut := false
	for i, line := range lines {
		if local.TextUnits(line) > local.MaxTitle {
			lines[i], cut = local.StoredTitle(line), true
		}
	}
	if !cut {
		return
	}
	if fl.kind == fieldArea {
		fl.area.SetValue(strings.Join(lines, "\n"))
		return
	}
	fl.input.SetValue(strings.Join(lines, "\n"))
}

// select appends a cycling field. The first option is the default.
func (f *form) selectField(key, label string, options []string) *form {
	opts := append([]string(nil), options...)
	f.fields = append(f.fields, &formField{key: key, kind: fieldSelect, label: label, options: opts, value: opts[0]})
	return f
}

// selectValue appends a cycling field pre-set to value when present.
func (f *form) selectValue(key, label string, options []string, value string) *form {
	f.selectField(key, label, options)
	fl := f.fields[len(f.fields)-1]
	for i, o := range options {
		if o == value {
			fl.value = o
			fl.options = append(options[i:], options[:i]...)
			break
		}
	}
	return f
}

// toggle appends a yes/no field.
func (f *form) toggle(key, label string, value bool) *form {
	v := "no"
	if value {
		v = "yes"
	}
	f.fields = append(f.fields, &formField{key: key, kind: fieldToggle, label: label, value: v, options: []string{"no", "yes"}})
	return f
}

// preview appends a line the app updates as the user types.
func (f *form) preview(text string) *form {
	f.fields = append(f.fields, &formField{kind: fieldPreview, value: text})
	return f
}

// setPreview replaces the text of the preview line, if the form has one.
func (f *form) setPreview(text string) {
	for _, fl := range f.fields {
		if fl.kind == fieldPreview {
			fl.value = text
		}
	}
}

// valueOf is what a field holds right now.
func (f *form) valueOf(key string) string {
	f.syncValues()
	for _, fl := range f.fields {
		if fl.key == key {
			return fl.value
		}
	}
	return ""
}

// note appends read-only guidance.
func (f *form) note(label, text string) *form {
	f.fields = append(f.fields, &formField{kind: fieldNote, label: label, value: text})
	return f
}

// alert appends a note that says what went wrong, for a form brought back
// after the server refused what it held.
func (f *form) alert(text string) *form {
	f.fields = append(f.fields, &formField{kind: fieldNote, label: "error", value: text, alert: true})
	return f
}

func (f *form) innerWidth() int { return max(f.width-8, 24) }

// noteWidth is how wide guidance text is wrapped.
func (f *form) noteWidth() int { return max(f.innerWidth()-2, 20) }

// static reports whether a field is only there to be read.
func (fl *formField) static() bool { return fl.kind == fieldNote || fl.kind == fieldPreview }

// focusField moves the text cursor into the focused field. Bubbletea's
// editors start unfocused, so exactly one field must be focused for typing
// to land anywhere.
func (f *form) focusField() {
	for i, fl := range f.fields {
		switch fl.kind {
		case fieldText:
			if i == f.focus {
				fl.input.Focus()
			} else {
				fl.input.Blur()
			}
		case fieldArea:
			if i == f.focus {
				fl.area.Focus()
			} else {
				fl.area.Blur()
			}
		}
	}
}

// focusFirst lands on the first field the user can actually type into, so
// opening a form and typing just works. A form of choices has none, and
// lands on the first choice rather than on the guidance above it.
func (f *form) focusFirst() {
	for i, fl := range f.fields {
		if fl.kind == fieldText || fl.kind == fieldArea {
			f.focus = i
			f.focusField()
			return
		}
	}
	for i, fl := range f.fields {
		if !fl.static() {
			f.focus = i
			break
		}
	}
	f.focusField()
}

// fieldHeight is how many terminal rows a field occupies.
func (f *form) fieldHeight(fl *formField) int {
	if fl.kind == fieldPreview {
		return len(wrapText(fl.value, max(f.innerWidth()-f.labelWidth()-3, 8)))
	}
	if fl.kind == fieldNote {
		return len(wrapText(fl.value, f.noteWidth())) + 1
	}
	if fl.kind == fieldArea {
		return fl.rows + 1
	}
	return 1
}

func (f *form) totalHeight() int {
	return formChrome + f.fieldsHeight()
}

// syncValues copies what the editors hold back into the field values, so
// result() can hand a plain map to the caller.
func (f *form) syncValues() {
	for _, fl := range f.fields {
		switch fl.kind {
		case fieldText:
			fl.value = fl.input.Value()
		case fieldArea:
			fl.value = fl.area.Value()
		}
	}
}

func (f *form) update(msg tea.Msg) (*form, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil
	}
	if f.focus < 0 || f.focus >= len(f.fields) {
		return f, nil
	}
	fl := f.fields[f.focus]
	if f.fields[f.focus].kind != fieldArea {
		// Arrows move between fields, except inside a boxed field where
		// they belong to the editor.
		switch key.Type {
		case tea.KeyDown, tea.KeyCtrlN:
			return f.move(1), nil
		case tea.KeyUp, tea.KeyCtrlP:
			return f.move(-1), nil
		}
	}
	switch key.Type {
	case tea.KeyTab:
		return f.move(1), nil
	case tea.KeyShiftTab:
		return f.move(-1), nil
	}
	switch fl.kind {
	case fieldSelect, fieldToggle:
		switch key.String() {
		case " ", "right", "l":
			return f.cycle(1), nil
		case "left", "h":
			return f.cycle(-1), nil
		}
		return f, nil
	case fieldNote, fieldPreview:
		return f.move(sign(key.String())), nil
	case fieldArea:
		// Arrows stay with the editor so the caret can move between lines;
		// tab and ctrl+j/k move between fields.
		switch key.Type {
		case tea.KeyCtrlJ, tea.KeyTab:
			return f.move(1), nil
		case tea.KeyCtrlK, tea.KeyShiftTab:
			return f.move(-1), nil
		case tea.KeyEnter:
			if !fl.multiline {
				// Not a multi-line field: Enter is the save key, which
				// onFormKey handles before this point.
				return f, nil
			}
		}
		var cmd tea.Cmd
		fl.area, cmd = fl.area.Update(msg)
		fl.fitTitles()
		return f, cmd
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(msg)
	fl.fitTitles()
	return f, cmd
}

func sign(s string) int {
	switch s {
	case "up", "k", "-":
		return -1
	case "down", "j", "+":
		return 1
	}
	return 0
}

func (f *form) move(d int) *form {
	if len(f.fields) == 0 {
		return f
	}
	// Guidance is skipped: the cursor only ever rests on something that
	// can be changed.
	for i := 0; i < len(f.fields); i++ {
		f.focus = (f.focus + d + len(f.fields)) % len(f.fields)
		if !f.fields[f.focus].static() || d == 0 {
			break
		}
	}
	f.focusField()
	f.scrollToFocus()
	return f
}

// formChrome is how many rows the dialog spends on everything that is not
// a field: title, two spacers, the key hint, and the button row. The
// optional "more below" line is counted separately.
const formChrome = 5

// contentHeight is how many rows of fields the dialog can show.
func (f *form) contentHeight() int {
	budget := f.height() - formChrome
	if f.scrollable() {
		budget--
	}
	return max(budget, 1)
}

// scrollable reports whether fields do not all fit, which both sizes the
// budget and shows the "more below" hint.
func (f *form) scrollable() bool { return f.fieldsHeight() > f.height()-formChrome }

// fieldsHeight is the total height of every field.
func (f *form) fieldsHeight() int {
	h := 0
	for _, fl := range f.fields {
		h += f.fieldHeight(fl)
	}
	return h
}

// focusedMultiline reports whether the focused field treats Enter as a
// newline rather than the save key.
func (f *form) focusedMultiline() bool {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return false
	}
	return f.fields[f.focus].multiline
}

// scrollToFocus keeps the focused field inside the modal viewport.
func (f *form) scrollToFocus() {
	if len(f.fields) == 0 {
		return
	}
	avail := f.contentHeight()
	starts := make([]int, len(f.fields))
	pos := 0
	for i, fl := range f.fields {
		starts[i] = pos
		pos += f.fieldHeight(fl)
	}
	start := starts[f.focus]
	end := start + f.fieldHeight(f.fields[f.focus])
	if f.lastFocusable() {
		// Tab never lands on the notes after the last field, so bring
		// them in with it.
		end = pos
	}
	if start < f.offset {
		f.offset = start
	}
	// The offset stays on a field's first row, since view draws whole
	// fields only, and moves just far enough for the focused field to end
	// inside the window. One taller than the window starts it.
	if end-f.offset > avail {
		f.offset = start
		for i := 0; i < f.focus; i++ {
			if end-starts[i] <= avail {
				f.offset = starts[i]
				break
			}
		}
	}
}

// lastFocusable reports whether only static fields follow the focused one.
func (f *form) lastFocusable() bool {
	for _, fl := range f.fields[f.focus+1:] {
		if !fl.static() {
			return false
		}
	}
	return true
}

func (f *form) cycle(d int) *form {
	fl := f.fields[f.focus]
	if len(fl.options) == 0 {
		return f
	}
	idx := 0
	for i, o := range fl.options {
		if o == fl.value {
			idx = i
			break
		}
	}
	idx = (idx + d + len(fl.options)) % len(fl.options)
	fl.value = fl.options[idx]
	return f
}

func (f *form) height() int {
	limit := f.maxH
	if limit <= 0 {
		limit = 22
	}
	return min(f.totalHeight(), limit)
}

func (f *form) view() string {
	f.syncValues()
	// A resize, or a field whose text wraps onto more rows, can leave the
	// focused field outside the window it was scrolled into.
	f.scrollToFocus()
	avail := f.contentHeight()
	var b strings.Builder
	b.WriteString(brandGradient(f.title))
	b.WriteString("\n\n")

	// Render the fields from the scroll offset down, stopping at the first
	// one that does not fit so the window has no gaps. The first one is
	// drawn even when it is too tall, so the window is never empty.
	pos, shown := 0, 0
	for _, fl := range f.fields {
		h := f.fieldHeight(fl)
		if pos >= f.offset {
			if shown > 0 && shown+h > avail+1 {
				break
			}
			b.WriteString(f.fieldView(fl))
			shown += h
		}
		pos += h
	}
	if f.scrollable() {
		// Say so, rather than leaving the user to guess why the last field
		// seems to be missing. Once the window reaches the end, say where
		// the rest went instead.
		b.WriteString("\n")
		if pos < f.fieldsHeight() {
			b.WriteString(styWarn.Render("▾ more fields below, keep pressing tab"))
		} else {
			b.WriteString(styWarn.Render("▴ more fields above, shift+tab goes back"))
		}
	}
	b.WriteString("\n")
	saveKey, saveHint := "enter", "enter save"
	if f.focusedMultiline() {
		// The focused box takes Enter for itself, so say what saves.
		saveKey, saveHint = "ctrl+s", "ctrl+s save · enter adds a line · ctrl+e editor"
	}
	b.WriteString(styFaint.Render(saveHint + " · esc cancel · tab next field"))
	b.WriteString("\n")
	b.WriteString(styPill.Render("  save  ") + " " + styFaint.Render(saveKey))
	b.WriteString("     ")
	b.WriteString(lipgloss.NewStyle().Foreground(colMuted).Background(colPanel).Render("  cancel  ") + " " + styFaint.Render("esc"))

	w := max(lipgloss.Width(b.String()), min(f.innerWidth(), 56))
	body := lipgloss.NewStyle().Width(w).Render(b.String())
	return styBox.Render(body)
}

// labelWidth is the width of the longest label, so the fields line up.
func (f *form) labelWidth() int {
	w := 0
	for _, o := range f.fields {
		if !o.static() {
			w = max(w, len(o.label))
		}
	}
	return w
}

func (f *form) fieldView(fl *formField) string {
	active := f.fields[f.focus] == fl
	labelW := f.labelWidth()
	indent := strings.Repeat(" ", labelW+3)
	name := styMuted.Render(pad(fl.label, labelW+1))
	marker := "  "
	if active {
		marker = styAccent.Render("▌ ")
		name = styKey.Render(pad(fl.label, labelW+1))
	}
	switch fl.kind {
	case fieldNote:
		lines := wrapText(fl.value, f.noteWidth())
		for i, l := range lines {
			if fl.alert {
				lines[i] = "  " + styErr.Render(l)
				continue
			}
			lines[i] = "  " + styFaint.Italic(true).Render(l)
		}
		return strings.Join(lines, "\n") + "\n\n"
	case fieldPreview:
		lines := wrapText(fl.value, max(f.innerWidth()-labelW-3, 8))
		return indent + strings.Join(lines, "\n"+indent) + "\n"
	case fieldSelect, fieldToggle:
		return marker + name + f.choiceView(fl, active, max(f.innerWidth()-labelW-3, 12)) + "\n"
	case fieldArea:
		// The textarea renders its own soft-wrapped rows and cursor; the
		// label sits on the first row. Its width has to track the modal,
		// which can be resized underneath it.
		fl.area.SetWidth(max(f.innerWidth()-labelW-5, 20))
		fl.area.SetHeight(fl.rows)
		body := fl.area.View()
		lines := strings.Split(body, "\n")
		if len(lines) > fl.rows {
			lines = lines[:fl.rows]
		}
		for len(lines) < fl.rows {
			lines = append(lines, "")
		}
		out := marker + name + lines[0]
		for _, l := range lines[1:] {
			out += "\n" + indent + l
		}
		return out + "\n"
	}
	// The width has to track the dialog, as the boxed fields do.
	fl.input.Width = max(f.innerWidth()-labelW-5, 20)
	return marker + name + fl.input.View() + "\n"
}

// choiceView lays the options out in a row with the chosen one filled in.
// When they do not fit, only the chosen one is shown, with its place in
// the list, so a long list of lists cannot push the dialog off screen.
func (f *form) choiceView(fl *formField, active bool, width int) string {
	chosen := styBold
	if active {
		chosen = styPill
	}
	parts := make([]string, 0, len(fl.options))
	total, at := 0, 0
	for i, o := range fl.options {
		if o == fl.value {
			at = i
			parts = append(parts, chosen.Render(" "+o+" "))
		} else {
			parts = append(parts, styFaint.Render(" "+o+" "))
		}
		total += len(o) + 3
	}
	if total <= width {
		return strings.Join(parts, " ")
	}
	return styFaint.Render("‹ ") + chosen.Render(" "+truncate(fl.value, max(width-16, 4))+" ") +
		styFaint.Render(fmt.Sprintf(" ›  %d of %d", at+1, len(fl.options)))
}

// result collects the field values under their labels.
func (f *form) result(cancel bool) formResult {
	f.syncValues()
	values := make(map[string]string, len(f.fields))
	for _, fl := range f.fields {
		if fl.kind == fieldNote || fl.key == "" {
			continue
		}
		values[fl.key] = fl.value
	}
	orig := make(map[string]string, len(f.orig))
	for k, v := range f.orig {
		orig[k] = v
	}
	return formResult{id: f.id, values: values, orig: orig, cancel: cancel}
}

// wrapText breaks s into lines of at most w display cells, on word
// boundaries where possible. A word wider than a line, such as a pasted
// link, is broken across lines, as otherwise its end would be cut off.
func wrapText(s string, w int) []string {
	if w < 1 {
		return []string{""}
	}
	if s == "" {
		return []string{""}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			switch {
			case line == "":
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
				line += " " + word
				continue
			default:
				out = append(out, line)
			}
			pieces := strings.Split(ansi.Hardwrap(word, w, true), "\n")
			out = append(out, pieces[:len(pieces)-1]...)
			line = pieces[len(pieces)-1]
		}
		out = append(out, line)
	}
	return out
}
