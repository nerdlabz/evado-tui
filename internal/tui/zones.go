package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The mouse finds what it is over through zones: a view wraps whatever can
// be clicked or scrolled in two invisible markers as it draws it, and once
// the whole frame is composed the markers are found, measured and removed.
// Nothing has to work out where a pane or a dialog ends up on screen, so a
// click lands on what is drawn under it however the layout shifts.

type zone struct {
	x0, y0, x1, y1 int
	placed         bool
	click          func(double bool) tea.Cmd
	wheel          func(delta int) tea.Cmd
}

func (z *zone) contains(x, y int) bool {
	if !z.placed || y < z.y0 || y > z.y1 || x < z.x0 {
		return false
	}
	return x < z.x1 || z.x1 <= z.x0
}

func (z *zone) area() int {
	return (z.y1 - z.y0 + 1) * max(z.x1-z.x0, 1)
}

type zones struct {
	list  []*zone
	press func(name string) tea.Cmd
}

// drawing is nil when the mouse is off, which makes marking free.
var drawing *zones

const zoneLead = "\x1b[<"

func zoneStart(n int) string { return zoneLead + strconv.Itoa(n) + "z" }
func zoneEnd(n int) string   { return zoneLead + strconv.Itoa(n) + ";0z" }

func mark(s string, z *zone) string {
	if drawing == nil || s == "" {
		return s
	}
	n := len(drawing.list)
	drawing.list = append(drawing.list, z)
	return zoneStart(n) + s + zoneEnd(n)
}

func clickable(s string, fn func(double bool) tea.Cmd) string {
	return mark(s, &zone{click: fn})
}

func scrollable(s string, fn func(delta int) tea.Cmd) string {
	return mark(s, &zone{wheel: fn})
}

func keyed(name, s string) string {
	if drawing == nil {
		return s
	}
	if _, ok := parseKey(name); !ok {
		return s
	}
	press := drawing.press
	return clickable(s, func(bool) tea.Cmd { return press(name) })
}

func pressing() func(name string) tea.Cmd {
	if drawing == nil || drawing.press == nil {
		return func(string) tea.Cmd { return nil }
	}
	return drawing.press
}

func zoneMarker(s string) (n, id int, open, ok bool) {
	if !strings.HasPrefix(s, zoneLead) {
		return 0, 0, false, false
	}
	i := len(zoneLead)
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == len(zoneLead) || i >= len(s) {
		return 0, 0, false, false
	}
	id, _ = strconv.Atoi(s[len(zoneLead):i])
	switch {
	case s[i] == 'z':
		return i + 1, id, true, true
	case strings.HasPrefix(s[i:], ";0z"):
		return i + 3, id, false, true
	}
	return 0, 0, false, false
}

// zoneMarks is every zone marker in s, in order. Cutting a line short
// keeps them, so a region still closes where its text was cut.
func zoneMarks(s string) string {
	if !strings.Contains(s, zoneLead) {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if n, _, _, ok := zoneMarker(s[i:]); ok {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		if n, esc := escapeLen(s[i:]); esc {
			i += n
			continue
		}
		i += decodeRune(s[i:])
	}
	return b.String()
}

// place finds where each zone was drawn in the finished screen and returns
// the screen without the markers. A line spliced around a dialog or the
// toast can repeat a marker, so only the first start and the first end
// after it count.
func (zs *zones) place(screen string) string {
	if !strings.Contains(screen, zoneLead) {
		return screen
	}
	var b strings.Builder
	b.Grow(len(screen))
	x, y := 0, 0
	open := map[int]bool{}
	for i := 0; i < len(screen); {
		if n, id, start, ok := zoneMarker(screen[i:]); ok {
			i += n
			if id >= len(zs.list) {
				continue
			}
			z := zs.list[id]
			switch {
			case start && !z.placed && !open[id]:
				z.x0, z.y0 = x, y
				open[id] = true
			case !start && open[id]:
				z.x1, z.y1 = x, y
				z.placed = true
				delete(open, id)
			}
			continue
		}
		if screen[i] == '\n' {
			b.WriteByte('\n')
			x, y = 0, y+1
			i++
			continue
		}
		if n, esc := escapeLen(screen[i:]); esc {
			b.WriteString(screen[i : i+n])
			i += n
			continue
		}
		size := decodeRune(screen[i:])
		b.WriteString(screen[i : i+size])
		x += runeCells(screen[i : i+size])
		i += size
	}
	return b.String()
}

func (zs *zones) at(x, y int) []*zone {
	if zs == nil {
		return nil
	}
	var hit []*zone
	for _, z := range zs.list {
		if z.contains(x, y) {
			hit = append(hit, z)
		}
	}
	for i := 1; i < len(hit); i++ {
		for j := i; j > 0 && hit[j].area() < hit[j-1].area(); j-- {
			hit[j], hit[j-1] = hit[j-1], hit[j]
		}
	}
	return hit
}
