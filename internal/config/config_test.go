package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("EVADO_CONFIG", filepath.Join(dir, "config.json"))
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempConfig(t)
	want := Config{
		Token: "evt_abc", BaseURL: "https://x/api/v1", Account: "a@b.c", Status: "today",
		PollSeconds: 300, Theme: "dark", Mouse: true, Keys: map[string]string{"J": "j"},
	}
	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	withTempConfig(t)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load on a fresh machine: %v", err)
	}
	if got.Token != "" {
		t.Errorf("Token = %q, want empty", got.Token)
	}
}

func TestFilePermissionsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on Windows")
	}
	// Exercise the real directory layout rather than the file override, so
	// the permissions of the directory the client creates are checked too.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := (Config{Token: "evt_secret"}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600: a token file must not be world-readable", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config dir mode = %o, want no group or other access", perm)
	}
}

func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := withTempConfig(t)
	if err := (Config{Token: "evt_abc"}).Save(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q was left behind", e.Name())
		}
	}
}

func TestSaveOverwritesAtomically(t *testing.T) {
	withTempConfig(t)
	if err := (Config{Token: "first"}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Token: "second"}).Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "second" {
		t.Errorf("Token = %q, want second", got.Token)
	}
}

func TestCorruptFileReportsAnError(t *testing.T) {
	withTempConfig(t)
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("a corrupt config should be reported, not silently ignored")
	}
}

func TestDirFollowsXDG(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if runtime.GOOS == "windows" {
		t.Skip("XDG is overridden by %AppData% on Windows")
	}
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "evado"); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
}

func TestDirUsesAppDataOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only behaviour")
	}
	base := t.TempDir()
	t.Setenv("AppData", base)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "evado"); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
}

func TestPathHonoursOverride(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "nested", "evado.json")
	t.Setenv("EVADO_CONFIG", custom)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Errorf("Path = %q, want %q", got, custom)
	}
	if err := (Config{Token: "evt_abc"}).Save(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Config
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
}

func TestPollInterval(t *testing.T) {
	cases := []struct {
		seconds int
		want    time.Duration
	}{
		{0, 10 * time.Minute},      // unset: the default
		{-5, 10 * time.Minute},     // nonsense: the default
		{30, 120 * time.Second},    // below the floor: raised to it
		{119, 120 * time.Second},   // just under: raised
		{120, 120 * time.Second},   // exactly the floor
		{300, 300 * time.Second},   // in range: honoured
		{3600, 3600 * time.Second}, // long is fine
	}
	for _, c := range cases {
		if got := (Config{PollSeconds: c.seconds}).PollInterval(); got != c.want {
			t.Errorf("PollSeconds=%d: interval = %v, want %v", c.seconds, got, c.want)
		}
	}
}

// Remembering the last view must not save a token that was only passed for
// one run.
func TestSavePrefsLeavesTheStoredTokenAlone(t *testing.T) {
	withTempConfig(t)
	if err := (Config{Token: "evt_saved"}).Save(); err != nil {
		t.Fatal(err)
	}
	run := Config{Token: "evt_for_this_run", Mode: "Today only", Status: "today", ListID: "l1"}
	if err := run.SavePrefs(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "evt_saved" {
		t.Errorf("token = %q, want the saved one", got.Token)
	}
	if got.Mode != "Today only" || got.Status != "today" || got.ListID != "l1" {
		t.Errorf("prefs not saved: %+v", got)
	}
}

// A config that cannot be read must be reported, not replaced with the whole
// runtime config: that would save a token meant for one run and destroy
// whatever the person had stored.
func TestSavePrefsRefusesAMalformedConfig(t *testing.T) {
	withTempConfig(t)
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	const broken = `{"token": "evt_saved", `
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	run := Config{Token: "evt_for_this_run", Mode: "Today only"}
	if err := run.SavePrefs(); err == nil {
		t.Fatal("SavePrefs replaced a malformed config")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != broken {
		t.Errorf("file changed to %q, want it left as it was", b)
	}
}

func TestSavePrefsRefusesAnUnreadableConfig(t *testing.T) {
	withTempConfig(t)
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	// A directory in the file's place fails the read on every platform,
	// and unlike a missing file it is not "nothing saved yet".
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	run := Config{Token: "evt_for_this_run", Mode: "Today only"}
	if err := run.SavePrefs(); err == nil {
		t.Fatal("SavePrefs wrote over an unreadable config")
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Errorf("the unreadable path was replaced: %v, %v", fi, err)
	}
}

func TestSavePrefsWithNoConfigSavesPrefsButNotTheToken(t *testing.T) {
	withTempConfig(t)
	run := Config{Token: "evt_for_this_run", Mode: "Today only", Status: "today", ListID: "l1", Board: true}
	if err := run.SavePrefs(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "" {
		t.Errorf("token = %q, the per-run token was saved", got.Token)
	}
	if got.Mode != "Today only" || got.Status != "today" || got.ListID != "l1" || !got.Board {
		t.Errorf("prefs not saved: %+v", got)
	}
}

func TestLoadMigratesLegacyServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("EVADO_CONFIG", path)
	if err := os.WriteFile(path, []byte(`{"token":"evt_x","baseUrl":"https://evado.nerdlabz.com/api/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://evado.app/api/v1" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.Token != "evt_x" {
		t.Errorf("token lost: %q", c.Token)
	}
}

func TestDateOrderIsReadFromTheFileAndSurvivesSavePrefs(t *testing.T) {
	dir := withTempConfig(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"dateOrder":"dmy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.DateOrder != "dmy" {
		t.Fatalf("Load = %+v, %v; want dateOrder dmy", got, err)
	}
	if err := (Config{Status: "today"}).SavePrefs(); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	if got, _ := Load(); got.DateOrder != "dmy" {
		t.Errorf("dateOrder after SavePrefs = %q; want dmy", got.DateOrder)
	}
}
