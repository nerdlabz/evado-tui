// Package api is a typed client for the Evado public API v1.
package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// User is the account a token belongs to.
type User struct {
	SmartParsing bool `json:"smartParsing"`
	// SmartParsingAvailable is copied from the /me envelope and kept locally.
	SmartParsingAvailable bool   `json:"smartParsingAvailable,omitempty"`
	ID                    string `json:"id"`
	Email                 string `json:"email"`
	// Username is how people find the account to share a list. It is null
	// until one is chosen, which is different from an empty string.
	Username *string `json:"username"`
	TZ       string  `json:"tz"`
	// DateOrder is how the account reads a date written in numbers: "mdy"
	// makes 3/4 March 4 and "dmy" the 3rd of April.
	DateOrder string    `json:"dateOrder,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Handle is a username as people write it, "@sam", or "" when there is
// none.
func Handle(username *string) string {
	if username == nil || *username == "" {
		return ""
	}
	return "@" + *username
}

// List is a todo container.
type List struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	IsPersonal bool       `json:"isPersonal"`
	OwnerID    string     `json:"ownerId"`
	Role       string     `json:"role"`
	ReadOnly   bool       `json:"readOnly"`
	DeletesAt  *time.Time `json:"deletesAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// Member is a list member as returned by the change feed and /sync.
type Member struct {
	UserID   string  `json:"userId"`
	Username *string `json:"username"`
	Email    string  `json:"email"`
	Role     string  `json:"role"`
}

// Name is what to call the member: their username, or the email the
// service returned (masked, except your own) when they have none.
func (m Member) Name() string {
	if h := Handle(m.Username); h != "" {
		return h
	}
	return m.Email
}

// Person is someone the account shares at least one list with, as GET
// /people returns them.
type Person struct {
	UserID        string   `json:"userId"`
	Username      *string  `json:"username"`
	Email         string   `json:"email"` // masked
	SharedListIDs []string `json:"sharedListIds"`
}

// Name is what to call the person: their username, or the masked email.
func (p Person) Name() string {
	if h := Handle(p.Username); h != "" {
		return h
	}
	return p.Email
}

// ListSnapshot is a List plus its members.
type ListSnapshot struct {
	Shared bool `json:"shared,omitempty"`
	List
	Members []Member `json:"members"`
}

// Todo is a single task. Instants are ISO 8601 UTC; ScheduledDay is a
// calendar day in the account timezone.
type Todo struct {
	ID             string     `json:"id"`
	ListID         string     `json:"listId"`
	AssigneeID     *string    `json:"assigneeId"`
	Title          string     `json:"title"`
	RawInput       *string    `json:"rawInput"`
	Notes          *string    `json:"notes"`
	DoAt           *time.Time `json:"doAt"`
	ScheduledDay   *string    `json:"scheduledDay"`
	DueAt          *time.Time `json:"dueAt"`
	HasTime        bool       `json:"hasTime"`
	RemindAt       *time.Time `json:"remindAt"`
	RemindedAt     *time.Time `json:"remindedAt"`
	RecurrenceRule *string    `json:"recurrenceRule"`
	SortOrder      int        `json:"sortOrder"`
	IsCompleted    bool       `json:"isCompleted"`
	CompletedAt    *time.Time `json:"completedAt"`
	DeletedAt      *time.Time `json:"deletedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// Tombstoned reports whether the todo is a soft-deleted change-feed entry.
func (t Todo) Tombstoned() bool { return t.DeletedAt != nil }

// Completed reports whether the todo is done.
func (t Todo) Completed() bool { return t.IsCompleted }

// SyncTodo is a Todo with its checklist inlined, as the change feed and
// /sync return it.
type SyncTodo struct {
	Todo
	Checklist []ChecklistItem `json:"checklist"`
}

// ChecklistItem is one step of a todo.
type ChecklistItem struct {
	ID          string     `json:"id"`
	TodoID      string     `json:"todoId"`
	Title       string     `json:"title"`
	Position    int        `json:"position"`
	CompletedAt *time.Time `json:"completedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Completed reports whether the step is ticked.
func (c ChecklistItem) Completed() bool { return c.CompletedAt != nil }

// SyncResponse is the change feed / /sync body.
type SyncResponse struct {
	Todos []SyncTodo `json:"todos"`
	Lists []List     `json:"lists"`
	// Invitations is a full snapshot, like Lists. An empty array means none
	// are pending and decodes to an empty slice; nil means the server sent
	// no snapshot, which must not be taken for "none".
	Invitations []Invitation `json:"invitations"`
	// Cursor is a position in the service's change log, not a time. Keep it
	// and send it back as the next Since without reading, comparing or
	// building one: only the service knows what it means.
	Cursor  string      `json:"cursor"`
	HasMore bool        `json:"hasMore"`
	Errors  []SyncError `json:"errors,omitempty"`
	// Deferred names the pushed changes the service did not get to in this
	// request. None of them was applied; send each again as it was. It is
	// only present for a request that set AcceptDeferred.
	Deferred []DeferredChange `json:"deferred,omitempty"`
}

// Invitation is a pending invitation to a list, addressed to the account.
type Invitation struct {
	ID   string `json:"id"`
	List struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"list"`
	InvitedBy struct {
		UserID   string  `json:"userId"`
		Username *string `json:"username"`
		Email    string  `json:"email"` // masked
	} `json:"invitedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// SyncError is a per-change failure; the batch still applies.
type SyncError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
	// Code is a machine-readable reason, when the service gives one.
	Code string `json:"code,omitempty"`
}

// DeferredChange is a pushed change the service left unapplied because the
// request held more than it takes at once.
type DeferredChange struct {
	ID string `json:"id"`
}

// CodeDeferred is the code of a SyncError that is a deferral, not a refusal.
const CodeDeferred = "deferred"

// notAppliedPrefix starts the message the service gives a change it did not
// get to, for a client that did not ask for the deferred list.
const notAppliedPrefix = "not applied:"

// Deferred reports whether the service left this change unapplied to be
// sent again, rather than turning it down. A client that sets
// AcceptDeferred is told in the deferred list instead, so this is the
// safety net for an answer that put one in errors anyway: by its code, or
// by the "not applied:" message the service gives when it was not asked.
func (e SyncError) Deferred() bool {
	return e.Code == CodeDeferred || strings.HasPrefix(e.Error, notAppliedPrefix)
}

// Channel is a notification channel config (never returned verbatim).
type Channel struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Label     string    `json:"label"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

// APIToken is a minted API token. The value is returned exactly once, on
// creation.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// Export is the normalized full or portable Evado export.
type Export struct {
	Format               string         `json:"format,omitempty"`
	Version              int            `json:"version,omitempty"`
	ExportedAt           time.Time      `json:"exportedAt"`
	User                 User           `json:"user"`
	Lists                []ListSnapshot `json:"lists"`
	Todos                []SyncTodo     `json:"todos"`
	NotificationChannels []Channel      `json:"notificationChannels"`
	APITokens            []APIToken     `json:"apiTokens"`
}

// Todo statuses accepted by the todo list filter.
const (
	StatusOpen      = "open"
	StatusToday     = "today"
	StatusDue       = "due"
	StatusUpcoming  = "upcoming"
	StatusOverdue   = "overdue"
	StatusCompleted = "completed"
	StatusAll       = "all"
)

// Statuses returns the todo status filter values in display order.
func Statuses() []string {
	return []string{
		StatusToday, StatusOverdue, StatusDue, StatusUpcoming,
		StatusOpen, StatusCompleted, StatusAll,
	}
}

// NewTodo is the create body. Only Title is required; a newline in Title
// splits the input into several todos server-side.
type NewTodo struct {
	Smart *bool `json:"smart,omitempty"`
	Parse *bool `json:"parse,omitempty"`
	// TimePassed asks for one reading of a time in the title that has
	// already passed today: TimeKept keeps the todo on today at that time
	// with no reminder, TimeMoved moves it to tomorrow. Empty leaves it to
	// the words: "today" or "tonight" keeps, and a time with no day moves.
	TimePassed   string  `json:"timePassed,omitempty"`
	ID           string  `json:"id,omitempty"`
	Title        string  `json:"title"`
	Notes        *string `json:"notes,omitempty"`
	ListID       string  `json:"listId,omitempty"`
	AssigneeID   *string `json:"assigneeId,omitempty"`
	DoAt         *string `json:"doAt,omitempty"`
	ScheduledDay *string `json:"scheduledDay,omitempty"`
	DueAt        *string `json:"dueAt,omitempty"`
	RemindAt     *string `json:"remindAt,omitempty"`
	IsCompleted  *bool   `json:"isCompleted,omitempty"`
	CompletedAt  *string `json:"completedAt,omitempty"`
	SortOrder    *int    `json:"sortOrder,omitempty"`
}

// Val marshals a string for a patch field.
func Val(s string) json.RawMessage { return json.RawMessage(strconv.Quote(s)) }

// BoolVal marshals a bool for a patch field.
func BoolVal(b bool) json.RawMessage { return json.RawMessage(strconv.FormatBool(b)) }

// IntVal marshals an int for a patch field.
func IntVal(n int) json.RawMessage { return json.RawMessage(strconv.Itoa(n)) }

// Null clears a patch field: sending an explicit JSON null removes the
// value server-side, which is different from omitting the field.
var Null = json.RawMessage("null")

// TodoPatch updates a todo. A nil field is omitted and left alone; Null
// clears the field.
type TodoPatch struct {
	Title          json.RawMessage `json:"title,omitempty"`
	Notes          json.RawMessage `json:"notes,omitempty"`
	ListID         json.RawMessage `json:"listId,omitempty"`
	AssigneeID     json.RawMessage `json:"assigneeId,omitempty"`
	DoAt           json.RawMessage `json:"doAt,omitempty"`
	HasTime        json.RawMessage `json:"hasTime,omitempty"`
	ScheduledDay   json.RawMessage `json:"scheduledDay,omitempty"`
	DueAt          json.RawMessage `json:"dueAt,omitempty"`
	RemindAt       json.RawMessage `json:"remindAt,omitempty"`
	RecurrenceRule json.RawMessage `json:"recurrenceRule,omitempty"`
	IsCompleted    json.RawMessage `json:"isCompleted,omitempty"`
	CompletedAt    json.RawMessage `json:"completedAt,omitempty"`
	SortOrder      json.RawMessage `json:"sortOrder,omitempty"`
}

// Empty reports whether the patch would change nothing, so the client can
// skip a pointless round trip.
func (p TodoPatch) Empty() bool {
	return p.Title == nil && p.Notes == nil && p.ListID == nil &&
		p.AssigneeID == nil && p.DoAt == nil && p.HasTime == nil && p.ScheduledDay == nil &&
		p.DueAt == nil && p.RemindAt == nil && p.RecurrenceRule == nil &&
		p.IsCompleted == nil && p.CompletedAt == nil && p.SortOrder == nil
}

// MaxPageSize is the most todos the service puts in one page of a filtered
// list. It refuses a larger limit, so more than this takes more pages.
const MaxPageSize = 200

// TodoFilter selects todos. An empty Since means a filtered list; setting
// it to a cursor from an earlier sync switches GET /todos to the change
// feed.
type TodoFilter struct {
	Status   string
	ListID   string
	Assigned string
	Query    string
	Undated  bool
	Limit    int
	// Cursor is the NextCursor of an earlier page of this same filtered
	// list, which asks for the page after it. It is a different thing from
	// Since: it only pages a list, and the filters have to be the same.
	Cursor string
	Since  string
}

// TodoPage is one page of a filtered list.
type TodoPage struct {
	Todos []Todo
	// Steps are the checklists the answer carried, by todo id; see
	// ListTodosPage.
	Steps map[string][]ChecklistItem
	// NextCursor is empty on the last page. Otherwise it goes back as
	// TodoFilter.Cursor, with the same filters, to get the next page.
	NextCursor string
}

// ParsePreview is the /parse result. The response body is not described in
// the spec, so this stays permissive.
type ParsePreview struct {
	KeptAsName   string     `json:"keptAsName,omitempty"`
	Title        string     `json:"title"`
	DoAt         *time.Time `json:"doAt"`
	ScheduledDay *string    `json:"scheduledDay"`
	DueAt        *time.Time `json:"dueAt"`
	HasTime      bool       `json:"hasTime"`
	// RecurrenceRule is set when the text said how often, as "1:week:3".
	RecurrenceRule *string `json:"recurrenceRule"`
	// AssumedTime is set when a time was typed without am/pm and one half of
	// the day was guessed.
	AssumedTime *AssumedTime `json:"assumedTime"`
	// TimePassed is TimeKept or TimeMoved when the time read had already
	// passed today, and nil otherwise.
	TimePassed *string        `json:"timePassed"`
	Raw        map[string]any `json:"-"`
}

// Passed is TimePassed as a plain string, empty when no time had passed.
func (p ParsePreview) Passed() string {
	if p.TimePassed == nil {
		return ""
	}
	return *p.TimePassed
}

// What became of a time typed for today that had already passed when it was
// read. TimeKept: the words said today or tonight, so the todo stays on
// today at that time and gets no reminder. TimeMoved: no day was said, so it
// went to tomorrow at that time.
const (
	TimeKept  = "kept"
	TimeMoved = "moved"
)

// AssumedTime says which half of the day a time typed without am/pm was
// read as. Replacing Text with AlternativeText in the title says the other
// half outright.
type AssumedTime struct {
	// Text is the words that were read, "at 8".
	Text string `json:"text"`
	// Assumed is "am" or "pm".
	Assumed         string    `json:"assumed"`
	AlternativeText string    `json:"alternativeText"`
	AlternativeDoAt time.Time `json:"alternativeDoAt"`
}

// Hint tells a person how to say the other half of the day, as "type 9pm
// for evening". It is empty when the service gave no alternative.
func (a AssumedTime) Hint() string {
	alt := strings.TrimPrefix(a.AlternativeText, "at ")
	if alt == "" {
		return ""
	}
	part := "morning"
	if strings.HasSuffix(alt, "pm") {
		part = "afternoon"
		hour, _ := strconv.Atoi(alt[:len(alt)-len(strings.TrimLeft(alt, "0123456789"))])
		if hour >= 6 && hour < 12 {
			part = "evening"
		}
	}
	return "type " + alt + " for " + part
}

// SyncChange is one local change pushed through /sync.
type SyncChange struct {
	ID             string     `json:"id"`
	ListID         *string    `json:"listId,omitempty"`
	Title          *string    `json:"title,omitempty"`
	Notes          *string    `json:"notes,omitempty"`
	IsCompleted    *bool      `json:"isCompleted,omitempty"`
	CompletedAt    *string    `json:"completedAt,omitempty"`
	AssigneeID     *string    `json:"assigneeId,omitempty"`
	ScheduledDay   *string    `json:"scheduledDay,omitempty"`
	DoAt           *string    `json:"doAt,omitempty"`
	DueAt          *string    `json:"dueAt,omitempty"`
	RemindAt       *string    `json:"remindAt,omitempty"`
	RecurrenceRule *string    `json:"recurrenceRule,omitempty"`
	SortOrder      *int       `json:"sortOrder,omitempty"`
	Deleted        *bool      `json:"deleted,omitempty"`
	Checklist      []SyncStep `json:"checklist,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// SyncStep is a checklist entry in a pushed change (id is client-generated).
type SyncStep struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	CompletedAt *string `json:"completedAt,omitempty"`
}

// SyncRequest is the /sync body.
type SyncRequest struct {
	Since   string       `json:"since,omitempty"`
	Changes []SyncChange `json:"changes,omitempty"`
	// AcceptDeferred says the caller keeps the changes the service names in
	// SyncResponse.Deferred and sends them again. Without it the service
	// reports them in errors.
	AcceptDeferred bool `json:"acceptDeferred,omitempty"`
}

// InviteTarget says who to invite. Exactly one field is set: the service
// refuses a request that names someone twice.
type InviteTarget struct {
	UserID   string `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
}

// Invitee is who an invitation went to, as the service names them.
type Invitee struct {
	UserID   string  `json:"userId"`
	Username *string `json:"username"`
}

// InvitationList is the answer to GET /invitations.
type InvitationList struct {
	Invitations []Invitation `json:"invitations"`
	// Cached is set by the copy of the service kept on this computer when
	// it could not reach the real one and answered with what it last saw.
	Cached bool `json:"cached,omitempty"`
}

// ProfilePatch updates the account. A nil field is left alone; Null clears
// the field, which is how a username is removed.
type ProfilePatch struct {
	SmartParsing *bool           `json:"smartParsing,omitempty"`
	TZ           json.RawMessage `json:"tz,omitempty"`
	Username     json.RawMessage `json:"username,omitempty"`
}

// Invite is a created invite link.
type Invite struct {
	InviteURL string `json:"inviteUrl"`
	ExpiresIn int    `json:"expiresIn"`
}
