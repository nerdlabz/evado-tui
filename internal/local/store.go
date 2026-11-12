package local

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/atomicfile"
	"github.com/nerdlabz/evado-tui/internal/config"
)

// version is bumped when the file layout changes.
const version = 2

// The names a todo's pending changes go by. They are the fields of a /sync
// change, except "do", which stands for the do date however it is sent.
const (
	fTitle     = "title"
	fNotes     = "notes"
	fList      = "listId"
	fAssignee  = "assigneeId"
	fDo        = "do"
	fDue       = "dueAt"
	fRemind    = "remindAt"
	fRule      = "recurrenceRule"
	fCompleted = "completed"
	fSort      = "sortOrder"
	fDeleted   = "deleted"
	fChecklist = "checklist"
)

// Todo is a todo as it is kept on this computer: what the API would say
// about it, its checklist, and what the service has not been told yet.
type Todo struct {
	api.Todo
	Steps []api.ChecklistItem `json:"steps,omitempty"`
	// ImportedID records the file's identity when a server collision requires a copy.
	ImportedID string `json:"importedId,omitempty"`
	// ImportStarted keeps a delete pending when an upload may already have landed.
	ImportStarted bool `json:"importStarted,omitempty"`
	// ImportBase is the first attempt, so edits survive an ambiguous response.
	ImportBase *Todo `json:"importBase,omitempty"`
	// New means the service has never seen this todo. Dirty names the
	// fields changed since it last did.
	New   bool     `json:"new,omitempty"`
	Dirty []string `json:"dirty,omitempty"`
	// Base is the todo as the service held it before the changes in Dirty,
	// so a pull can tell a field the service changed meanwhile from one it
	// still holds as it was. Only its API fields and steps are used.
	Base *Todo `json:"base,omitempty"`
	// Refused is why the service turned the last push down. A refused todo
	// is not sent again until it is edited, so one bad change cannot block
	// the rest.
	Refused string `json:"refused,omitempty"`
	// Spawned means this repeating todo's next occurrence was made here,
	// which happens once however often it is reopened and completed, as on
	// the service.
	Spawned bool `json:"spawned,omitempty"`
	// NextOnSync means the service is to make the next occurrence of this
	// completed repeating todo. It only does that when a todo it already
	// holds goes from open to completed, so one it has never seen goes up
	// open and its completion follows as an edit.
	NextOnSync bool `json:"nextOnSync,omitempty"`
}

// pending reports whether the service still has to hear about this todo.
func (t *Todo) pending() bool { return t.Refused == "" && (t.New || len(t.Dirty) > 0) }

// completesLater reports whether this todo's completion is held back from
// the push that creates it on the service.
func (t *Todo) completesLater() bool { return t.New && t.NextOnSync && t.IsCompleted }

// touch records a change to one or more fields. A todo the service holds
// must have its Base set first, while it is still as the service has it;
// see based.
//
// The service settles conflicts by which change is newer, and it stamps
// what it stores with its own clock. If this computer's clock runs behind,
// an edit made here could look older than the version it was made from and
// be dropped without a word. So an edit is always stamped after the version
// it changes, whatever the clock says.
func (t *Todo) touch(now time.Time, fields ...string) {
	t.UpdatedAt = after(t.UpdatedAt, now)
	t.Refused = ""
	for _, f := range fields {
		if !contains(t.Dirty, f) {
			t.Dirty = append(t.Dirty, f)
		}
	}
}

// based is what a todo's Base is to be once it changes: the one it has,
// or the todo itself while nothing in it waits to be sent. A todo the
// service has never seen has none. Call it before changing anything.
func (t *Todo) based() *Todo {
	if t.New || t.Base != nil || len(t.Dirty) > 0 {
		return t.Base
	}
	// The steps are copied because a step is changed in place.
	return &Todo{Todo: t.Todo, Steps: slices.Clone(t.Steps)}
}

// after is now, or the first stamp the service takes as newer than prev
// if now is not. The service keeps milliseconds and skips a change not
// newer than what it holds, so a stamp less than a millisecond later is
// no later at all.
func after(prev, now time.Time) time.Time {
	if prev = prev.Truncate(time.Millisecond); now.Truncate(time.Millisecond).After(prev) {
		return now
	}
	return prev.Add(time.Millisecond)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// List is a list as it is kept on this computer.
type List struct {
	api.List
	Members []api.Member `json:"members,omitempty"`
	// Local means the service has not been told about this list yet.
	Local bool `json:"local,omitempty"`
	// Renamed means the name here has not reached the service, which still
	// calls the list Was. A refused rename goes back to Was.
	Renamed bool   `json:"renamed,omitempty"`
	Was     string `json:"was,omitempty"`
}

// State is everything in the file.
type State struct {
	Version int `json:"version"`
	// UserID is the account this data belongs to, or empty while it
	// belongs to nobody but this computer.
	UserID  string   `json:"userId,omitempty"`
	BaseURL string   `json:"baseUrl,omitempty"`
	User    api.User `json:"user"`
	Lists   []List   `json:"lists"`
	Todos   []Todo   `json:"todos"`
	// ImportedIDs keeps reimports idempotent after a copied todo is deleted or erased.
	ImportedIDs map[string]string `json:"importedIds,omitempty"`
	// Deleted are the lists deleted here that the service still holds, in
	// the order they were deleted. They are gone from Lists, with their
	// todos, and are deleted there on the next sync.
	Deleted []List `json:"deleted,omitempty"`
	// Cursor is the service's place in its change log after the last pull,
	// which is where the next one starts. It is kept and sent back as it
	// came: it is not a time, and nothing here reads it.
	Cursor   string     `json:"cursor,omitempty"`
	LastSync *time.Time `json:"lastSync,omitempty"`
	// Invitations are the pending invitations the last sync reported, newest
	// first, so they can be read with no connection. Nil means no sync has
	// reported any, which is not the same as none pending: an empty snapshot
	// is an empty, non-nil slice. Like everything here it belongs to the
	// account and server in UserID and BaseURL, and goes when they change.
	Invitations []api.Invitation `json:"invitations"`
}

// localUser is the id the user has before there is an account.
const localUser = "local"

// fresh is the state of a computer that has never been used: one personal
// list and nothing in it.
func fresh(now time.Time) State {
	return State{
		Version: version,
		User:    api.User{ID: localUser, TZ: zoneName(), CreatedAt: now},
		Lists: []List{{
			List:  api.List{ID: NewID(), Name: "Personal", IsPersonal: true, OwnerID: localUser, Role: "owner", CreatedAt: now},
			Local: true,
		}},
	}
}

// zoneName is this computer's timezone as an IANA name where it can be
// found, and "Local" otherwise, which Go reads as the same thing.
func zoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "Local"
}

// NewID makes a random UUID, which is the form the service accepts for an
// id made on the client.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's random source is gone; nothing can run
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Path is where the todos are kept, next to the config file.
func Path() (string, error) {
	p, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "todos.json"), nil
}

// load reads the file. A missing one is a computer that has never been
// used; a damaged one is an error, because guessing would lose todos.
func load(path string, now time.Time) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fresh(now), nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%s is damaged: %w", path, err)
	}
	if (st.Version != 1 && st.Version != version) || len(st.Lists) == 0 {
		return State{}, fmt.Errorf("%s was written by a different version of evado", path)
	}
	st.Version = version
	// Older files counted runes or left edited titles unbounded. Mark only
	// changed fields so their first sync sends canonical titles once.
	for i := range st.Todos {
		t := &st.Todos[i]
		base := t.based()
		var fields []string
		if title := storedTitle(t.Title); title != t.Title {
			t.Title = title
			fields = append(fields, fTitle)
		}
		for j := range t.Steps {
			if title := storedTitle(t.Steps[j].Title); title != t.Steps[j].Title {
				t.Steps[j].Title = title
				fields = append(fields, fChecklist)
			}
		}
		if len(fields) > 0 {
			t.Base = base
			t.touch(now, fields...)
		}
	}
	// Files written before the service gave out cursors hold a time here,
	// which it would refuse. Dropping it makes the next sync a full pull,
	// which keeps what is here and takes what the account has.
	if !cursorShape.MatchString(st.Cursor) {
		st.Cursor = ""
	}
	return st, nil
}

// cursorShape is what the service accepts as a cursor.
var cursorShape = regexp.MustCompile(`^[0-9]{1,15}$`)

// createNew makes a file that must not exist yet. Tests replace it to make a
// backup fail the same way on every platform.
var createNew = func(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// fileSafe turns an account id into part of a file name. The id comes from
// the server, and one holding a separator or ".." would put the backup
// somewhere else, so anything but plain characters is replaced by a hash.
func fileSafe(id string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
	}
	if id != "" && len(id) <= 64 && strings.IndexFunc(id, func(r rune) bool { return !plain(r) }) < 0 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "id-" + hex.EncodeToString(sum[:8])
}

// setAside keeps a copy of st next to the file under a name nothing else
// has, and returns where it went. An earlier backup is never replaced: it
// may be the only copy of another account's todos.
func setAside(path, owner string, st State) (string, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	owner = fileSafe(owner)
	name := path + "." + owner + ".bak"
	for n := 1; ; n++ {
		f, err := createNew(name)
		if errors.Is(err, os.ErrExist) {
			name = fmt.Sprintf("%s.%s.%d.bak", path, owner, n)
			continue
		}
		if err != nil {
			return "", err
		}
		// Sync so the copy is on disk before the original is let go.
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
}

// save writes the file with owner-only permissions, in one step so that
// neither a crash nor a power cut can leave half of one.
func save(path string, st State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b)
}
