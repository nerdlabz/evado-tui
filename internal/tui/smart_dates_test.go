package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

func TestSmarterDatesAccountSetting(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	for _, row := range h.model.acct.menu() {
		if row.key == "smart-dates" {
			t.Fatal("unavailable setting shown")
		}
	}
	f.smartAvailable = true
	h.run(h.model.fetchMe())
	h.model.tab = tabAccount
	h.model.acct.selectRow("smart-dates")
	h.mustContain("Smarter dates: off")
	h.mustContain("Nothing else about you is sent")
	h.press("enter")
	if !f.smartParsing || !h.model.user.SmartParsing {
		t.Fatal("enable did not round-trip")
	}
	if got := f.meBodies[len(f.meBodies)-1]; got != `{"smartParsing":true}` {
		t.Fatalf("patch = %s", got)
	}
	h.press("enter")
	if f.smartParsing || h.model.user.SmartParsing {
		t.Fatal("disable did not round-trip")
	}
	f.smartAvailable = false
	h.press("enter")
	if h.model.user.SmartParsing {
		t.Fatal("failed toggle changed the displayed choice")
	}
	h.mustContain("Smarter dates are unavailable")
}

func TestCaptureCanReadAKeptNameAsADate(t *testing.T) {
	f := sampleAPI()
	var plainRequests int
	f.parseAnswer = func(title string, smart *bool) api.ParsePreview {
		if smart != nil && !*smart {
			plainRequests++
			day := "2026-10-04"
			return api.ParsePreview{Title: "Visit Land", ScheduledDay: &day}
		}
		return api.ParsePreview{Title: title, KeptAsName: "Tomorrow"}
	}
	h := newHarness(t, f)
	h.press("n")
	setCaptureTitle(h.model, "Visit Tomorrow Land")
	h.model.previewFor = "Visit Tomorrow Land"
	h.run(h.model.onPreview(previewDueMsg{seq: h.model.previewSeq}))
	h.mustContain("kept as a name")
	h.mustContain("ctrl+d reads as a date")
	h.press("ctrl+d")
	if plainRequests != 1 || h.model.captureSmart == nil || *h.model.captureSmart {
		t.Fatal("plain preview was not requested")
	}
	seq := h.model.previewSeq
	h.model.onPreview(previewMsg{seq: seq - 1, preview: api.ParsePreview{KeptAsName: "stale"}})
	if h.model.capturePreview.KeptAsName != "" {
		t.Fatal("stale reply replaced the plain preview")
	}
	h.press("enter")
	if len(f.creates) != 1 || f.creates[0].Smart == nil || *f.creates[0].Smart {
		t.Fatalf("capture lost the plain override: %+v", f.creates)
	}
}

func TestCaptureFlipsAnAssumedTime(t *testing.T) {
	f := sampleAPI()
	f.parseAnswer = func(title string, smart *bool) api.ParsePreview {
		return api.ParsePreview{Title: title}
	}
	h := newHarness(t, f)
	h.press("n")
	setCaptureTitle(h.model, "Dinner at 7")
	h.model.previewFor = "Dinner at 7"
	h.model.onPreview(previewMsg{seq: h.model.previewSeq, preview: api.ParsePreview{
		Title: "Dinner", DoAt: timePtr(time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC)), HasTime: true,
		AssumedTime: &api.AssumedTime{Text: "at 7", Assumed: "pm", AlternativeText: "at 7am"},
	}})
	h.mustContain("ctrl+t flips am/pm")
	h.press("ctrl+t")
	if got := h.model.form.valueOf("title"); got != "Dinner at 7am" {
		t.Fatalf("flipped title = %q", got)
	}
	if h.model.capturePreview == nil || h.model.capturePreview.Title != "Dinner at 7am" {
		t.Fatal("alternative was not previewed")
	}
}

func TestEditingCaptureResetsThePlainOverride(t *testing.T) {
	old := parseDebounce
	parseDebounce = time.Millisecond
	t.Cleanup(func() { parseDebounce = old })
	h := newHarness(t, sampleAPI())
	h.press("n")
	setCaptureTitle(h.model, "Visit Tomorrow Land")
	h.model.previewFor = "Visit Tomorrow Land"
	plain := false
	h.model.captureSmart = &plain
	h.model.capturePreview = &api.ParsePreview{KeptAsName: "Tomorrow"}
	setCaptureTitle(h.model, "Dentist at 9")
	h.model.schedulePreview()
	if h.model.captureSmart != nil || h.model.capturePreview != nil {
		t.Fatal("old override survived a new title")
	}
	if strings.Contains(h.model.form.view(), "ctrl+d") {
		t.Fatal("old preview control survived editing")
	}
}

func setCaptureTitle(m *Model, title string) {
	for _, field := range m.form.fields {
		if field.key == "title" {
			field.area.SetValue(title)
		}
	}
}

func timePtr(t time.Time) *time.Time { return &t }
