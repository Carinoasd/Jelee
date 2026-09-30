package domain

import (
	"errors"
	"time"
)

var (
	ErrForbidden    = errors.New("operation forbidden")
	ErrConflict     = errors.New("resource conflict")
	ErrLastAdmin    = errors.New("last active administrator required")
	ErrDatabase     = errors.New("account storage unavailable")
	ErrSessionLimit = errors.New("session limit reached")
)

// Actor contains identity verified by HTTP authentication. Stores recheck the
// live session and current user role inside every account transaction.
type Actor struct {
	UserID    string
	SessionID string
	IP        string
}

type User struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Locale      string     `json:"locale"`
	Hidden      bool       `json:"hidden"`
	Admin       bool       `json:"admin"`
	Disabled    bool       `json:"disabled"`
	DeletedAt   *time.Time `json:"deletedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Credentials must never be serialized, logged, or returned to a client.
type Credentials struct {
	UserID       string     `json:"-"`
	Name         string     `json:"-"`
	PasswordHash string     `json:"-"`
	Version      int64      `json:"-"`
	Disabled     bool       `json:"-"`
	Deleted      bool       `json:"-"`
	LockedUntil  *time.Time `json:"-"`
}

type UserInput struct {
	Name         string
	DisplayName  string
	Locale       string
	Hidden       bool
	Admin        bool
	Disabled     bool
	PasswordHash string `json:"-"`
}

type ProfileInput struct {
	DisplayName string
	Locale      string
	Hidden      bool
}

type Session struct {
	ID         string     `json:"id"`
	UserID     string     `json:"userId"`
	ClientKind string     `json:"clientKind"`
	DeviceName string     `json:"deviceName"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

type SessionGrant struct {
	User    User    `json:"user"`
	Session Session `json:"session"`
	Token   string  `json:"token"`
}

// LoginInput carries the result of an external password KDF. The transaction
// compares the credential snapshot before changing counters or issuing a token.
type LoginInput struct {
	Credentials Credentials
	PasswordOK  bool
	DeviceName  string
	IP          string
	MaxSessions int
	SessionTTL  time.Duration
	LockAfter   int
	LockFor     time.Duration
}

type LibraryGrant struct {
	LibraryID string `json:"libraryId"`
	Name      string `json:"name"`
}
