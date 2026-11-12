// Package config persists the API token and UI preferences.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/atomicfile"
)

// Config is the on-disk state, at $XDG_CONFIG_HOME/evado/config.json
// (or the platform equivalent).
type Config struct {
	// Demo is a run-only profile in a temporary directory.
	Demo    bool   `json:"-"`
	Token   string `json:"token,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Account string `json:"account,omitempty"`
	// Status is the saved status filter, and Mode the label of the view it
	// belongs to, so the Todos tab reopens where the user left it.
	Status string `json:"status,omitempty"`
	Mode   string `json:"mode,omitempty"`
	ListID string `json:"listId,omitempty"`
	// Board reopens the Todos tab as a column per day, and HideSidebar
	// keeps its sidebar folded away.
	Board       bool `json:"board,omitempty"`
	HideSidebar bool `json:"hideSidebar,omitempty"`
	// SavedViews are the named filter combinations, one set for each server
	// and account, and one for use without an account.
	SavedViews []ViewSet `json:"savedViews,omitempty"`

	// PollSeconds is how often the open view refreshes itself in the
	// background. Zero means DefaultPoll, and anything below MinPoll is
	// raised to it, so the API is never hammered.
	PollSeconds int `json:"pollSeconds,omitempty"`
	// Theme is "auto" (the default), "light", "dark", or the name of a
	// built-in theme such as "tokyonight".
	Theme string `json:"theme,omitempty"`
	// Mouse turns on wheel scrolling and click-to-select. It is off by
	// default because capturing the mouse stops the terminal selecting text.
	Mouse bool `json:"mouse,omitempty"`
	// NoCache keeps no todos on this computer: the TUI talks straight to
	// the server and needs a connection for everything. Without it the
	// todos live in a file here and sync in the background.
	NoCache bool `json:"noCache,omitempty"`
	// Local records the choice to use the TUI without an account, so it
	// does not ask for a token on every start.
	Local bool `json:"local,omitempty"`
	// Keys remaps keys: pressing the key on the left does what the key on
	// the right does, for example {"J": "j"}. It applies to the main views,
	// never to text entry.
	Keys map[string]string `json:"keys,omitempty"`
}

// Polling bounds. Ten minutes keeps a long-open session fresh without
// costing the server anything; two minutes is the floor.
const (
	DefaultPoll = 10 * time.Minute
	MinPoll     = 120 * time.Second
)

// SignedOut is c without the account it was signed in as: the token, the
// account label and the remembered view. Settings such as theme and keys
// stay, since they belong to the person and not to the account.
func (c Config) SignedOut() Config {
	c.Token, c.Account, c.Local = "", "", false
	c.Status, c.Mode, c.ListID = "", "", ""
	return c
}

// PollInterval is the effective background refresh interval.
func (c Config) PollInterval() time.Duration {
	if c.PollSeconds <= 0 {
		return DefaultPoll
	}
	return max(time.Duration(c.PollSeconds)*time.Second, MinPoll)
}

// SavePrefs writes only the view preferences into the file on disk,
// leaving the stored token alone. A token passed with -token is for one
// run, and remembering the last view must not quietly save it. A file that
// cannot be read or parsed is reported and left as it is: writing c over it
// would save that run-only token and discard whatever was stored. A missing
// file is fine, since Load returns an empty config for it.
func (c Config) SavePrefs() error {
	disk, err := Load()
	if err != nil {
		return err
	}
	disk.Status, disk.Mode, disk.ListID = c.Status, c.Mode, c.ListID
	disk.Board, disk.HideSidebar = c.Board, c.HideSidebar
	disk.SavedViews = c.SavedViews
	return disk.Save()
}

// SaveTheme writes only the theme into the file on disk, for the same
// reason SavePrefs leaves the token alone.
func SaveTheme(name string) error {
	disk, err := Load()
	if err != nil {
		return err
	}
	disk.Theme = name
	return disk.Save()
}

// Path returns the config file path. EVADO_CONFIG overrides it, which is
// handy for testing and for throwaway profiles.
func Path() (string, error) {
	if p := strings.TrimSpace(os.Getenv("EVADO_CONFIG")); p != "" {
		return p, nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Dir returns the config directory, honouring XDG_CONFIG_HOME on every
// platform so the same layout works everywhere.
func Dir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "evado"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	// Windows keeps config under %AppData%; every other OS follows XDG.
	if runtime.GOOS == "windows" {
		if dir := strings.TrimSpace(os.Getenv("AppData")); dir != "" {
			return filepath.Join(dir, "evado"), nil
		}
	}
	return filepath.Join(home, ".config", "evado"), nil
}

// Load reads the config, returning a zero value when none exists yet.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	// A server saved before production moved would only redirect, and the
	// login screen would show an address that no longer works.
	c.BaseURL = api.MigrateLegacy(c.BaseURL)
	return c, nil
}

// Save writes the config with owner-only permissions.
func (c Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	// In one step, so that a crash or a power cut cannot truncate a good
	// token.
	if err := atomicfile.Write(path, b); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	return nil
}
