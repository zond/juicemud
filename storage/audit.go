package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// AuditLogger writes security-relevant events to a log file as JSON.
// Log rotation is handled automatically via lumberjack.
type AuditLogger struct {
	mu     sync.Mutex
	writer io.WriteCloser
	enc    *json.Encoder
}

// AuditRef identifies a user by both ID and name for audit logging.
// ID is a pointer to distinguish between "ID is 0" and "no ID" (nil for system).
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
	User   AuditRef `json:"user"`
	Remote string   `json:"remote"`
}

func (AuditSessionEnd) auditData() {}

// AuditLoginFailed is logged on failed login attempt.
type AuditLoginFailed struct {
	User   AuditRef `json:"user"`
	Remote string   `json:"remote"`
}

func (AuditLoginFailed) auditData() {}

// AuditWizardGrant is logged when wizard privileges are granted to a user.
type AuditWizardGrant struct {
	Target  AuditRef `json:"target"`  // user receiving wizard privileges
	GrantedBy AuditRef `json:"granted_by"` // wizard who granted the privileges
}

func (AuditWizardGrant) auditData() {}

// AuditWizardRevoke is logged when wizard privileges are revoked from a user.
type AuditWizardRevoke struct {
	Target    AuditRef `json:"target"`     // user losing wizard privileges
	RevokedBy AuditRef `json:"revoked_by"` // wizard who revoked the privileges
}

func (AuditWizardRevoke) auditData() {}

// AuditServerConfigChange is logged when the server configuration is modified.
type AuditServerConfigChange struct {
	ChangedBy AuditRef `json:"changed_by"` // wizard who made the change
	Path      string   `json:"path"`       // dot-separated path that was changed
	OldValue  string   `json:"old_value"`  // JSON representation of old value
	NewValue  string   `json:"new_value"`  // JSON representation of new value
}

func (AuditServerConfigChange) auditData() {}

// NewAuditLogger creates a new audit logger writing to the specified file
// with automatic log rotation.
func NewAuditLogger(path string) *AuditLogger {
	writer := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    100,  // megabytes
		MaxBackups: 10,   // old log files
		MaxAge:     365,  // days
		Compress:   true, // gzip rotated files
	}
	return &AuditLogger{
		writer: writer,
		enc:    json.NewEncoder(writer),
	}
}

// Log writes a structured audit entry as JSON.
// Panics if encoding fails. This is intentional: all AuditData implementations
// are typed structs defined in this package with JSON-safe fields, so encoding
// should never fail. A failure indicates a programming error that must be fixed.
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
}

// Close closes the audit log file.
func (a *AuditLogger) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writer.Close()
}
