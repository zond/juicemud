package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/zond/juicemud"
)

// GenerateSessionID creates a unique session ID.
// This is a convenience wrapper around juicemud.NextUniqueID.
func GenerateSessionID() string {
	return juicemud.NextUniqueID()
}

// AuditLogger writes security-relevant events to a log file as JSON.
// TODO: Consider implementing log rotation when the audit log grows large.
type AuditLogger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

// AuditRef identifies a user or group by both ID and name for audit logging.
// ID is a pointer to distinguish between "ID is 0" and "no ID" (nil for system/owner).
type AuditRef struct {
	ID   *int64 `json:"id,omitempty"`
	Name string `json:"name"`
}

// Ref creates an AuditRef with the given ID and name.
func Ref(id int64, name string) AuditRef {
	return AuditRef{ID: &id, Name: name}
}

// SystemRef creates an AuditRef for "system" (no ID).
func SystemRef() AuditRef {
	return AuditRef{Name: "system"}
}

// OwnerRef creates an AuditRef for "owner" (no ID).
func OwnerRef() AuditRef {
	return AuditRef{Name: "owner"}
}

// RefPtr creates a pointer to an AuditRef with the given ID and name.
func RefPtr(id int64, name string) *AuditRef {
	return &AuditRef{ID: &id, Name: name}
}

// OwnerRefPtr creates a pointer to an AuditRef for "owner" (no ID).
func OwnerRefPtr() *AuditRef {
	return &AuditRef{Name: "owner"}
}

// AuditData is the interface for typed audit event data.
type AuditData interface {
	auditData()
}

// AuditEntry represents a single audit log entry.
type AuditEntry struct {
	Time      string    `json:"time"`
	SessionID string    `json:"session_id,omitempty"`
	Event     string    `json:"event"`
	Data      AuditData `json:"data"`
}

// AuditUserCreate is logged when a new user registers.
type AuditUserCreate struct {
	User   AuditRef `json:"user"`
	Remote string   `json:"remote"`
}

func (AuditUserCreate) auditData() {}

// AuditUserLogin is logged on successful login.
type AuditUserLogin struct {
	User   AuditRef `json:"user"`
	Remote string   `json:"remote"`
}

func (AuditUserLogin) auditData() {}

// AuditSessionEnd is logged when a session ends (disconnect/logout).
type AuditSessionEnd struct {
	User AuditRef `json:"user"`
}

func (AuditSessionEnd) auditData() {}

// AuditLoginFailed is logged on failed login attempt.
type AuditLoginFailed struct {
	User   AuditRef `json:"user"`
	Remote string   `json:"remote"`
}

func (AuditLoginFailed) auditData() {}

// AuditGroupCreate is logged when a group is created.
type AuditGroupCreate struct {
	Caller     AuditRef `json:"caller"`
	Group      AuditRef `json:"group"`
	Owner      AuditRef `json:"owner"`
	Supergroup bool     `json:"supergroup"`
}

func (AuditGroupCreate) auditData() {}

// AuditGroupDelete is logged when a group is deleted.
type AuditGroupDelete struct {
	Caller AuditRef `json:"caller"`
	Group  AuditRef `json:"group"`
}

func (AuditGroupDelete) auditData() {}

// AuditGroupEdit is logged when a group is modified.
type AuditGroupEdit struct {
	Caller         AuditRef  `json:"caller"`
	Group          AuditRef  `json:"group"`
	NameFrom       string    `json:"name_from,omitempty"`
	NameTo         string    `json:"name_to,omitempty"`
	OwnerFrom      *AuditRef `json:"owner_from,omitempty"`
	OwnerTo        *AuditRef `json:"owner_to,omitempty"`
	SupergroupFrom *bool     `json:"supergroup_from,omitempty"`
	SupergroupTo   *bool     `json:"supergroup_to,omitempty"`
}

func (AuditGroupEdit) auditData() {}

// AuditMemberAdd is logged when a user is added to a group.
type AuditMemberAdd struct {
	Caller AuditRef `json:"caller"`
	Added  AuditRef `json:"added"`
	Group  AuditRef `json:"group"`
}

func (AuditMemberAdd) auditData() {}

// AuditMemberRemove is logged when a user is removed from a group.
type AuditMemberRemove struct {
	Caller  AuditRef `json:"caller"`
	Removed AuditRef `json:"removed"`
	Group   AuditRef `json:"group"`
}

func (AuditMemberRemove) auditData() {}

// NewAuditLogger creates a new audit logger writing to the specified file.
func NewAuditLogger(path string) (*AuditLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &AuditLogger{
		file: f,
		enc:  json.NewEncoder(f),
	}, nil
}

// Log writes a structured audit entry as JSON and flushes to disk.
// Panics if encoding fails (indicates a bug in the typed AuditData structs).
func (a *AuditLogger) Log(ctx context.Context, event string, data AuditData) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessionID, _ := SessionID(ctx)
	if err := a.enc.Encode(AuditEntry{
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: sessionID,
		Event:     event,
		Data:      data,
	}); err != nil {
		panic(fmt.Sprintf("audit log encode failed: %v", err))
	}
	if err := a.file.Sync(); err != nil {
		log.Printf("audit log sync failed: %v", err)
	}
}

// Close closes the audit log file.
func (a *AuditLogger) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.file.Close()
}
