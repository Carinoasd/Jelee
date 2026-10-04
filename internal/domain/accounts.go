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
	// ErrNativeLoginDisabled is returned only after the password was verified,
	// so it never reveals the setting of an account to an unauthenticated caller.
	ErrNativeLoginDisabled = errors.New("native login disabled for user")
	// ErrPasswordMismatch rejects a password change whose current password
	// does not verify. It is distinct from ErrUnauthenticated: the session is
	// valid, only the typed value is wrong.
	ErrPasswordMismatch = errors.New("current password does not match")
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
	AllowNative bool       `json:"allowNative"`
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
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	ClientKind string `json:"clientKind"`
	DeviceName string `json:"deviceName"`
	// Client, DeviceID and Version are labels reported by a native client at
	// login. They identify a device for listing and policy, never prove it.
	Client     string     `json:"client,omitempty"`
	DeviceID   string     `json:"deviceId,omitempty"`
	Version    string     `json:"version,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	LastIP     string     `json:"lastIp,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// NativeClient is the client identity a native login reports. Name and
// DeviceID are required; Device and Version may be empty.
type NativeClient struct {
	Name     string
	Device   string
	DeviceID string
	Version  string
}

// Field limits of NativeClient in UTF-8 bytes, matching the session columns.
const (
	NativeClientNameMax    = 128
	NativeDeviceNameMax    = 128
	NativeDeviceIDMax      = 256
	NativeClientVersionMax = 64
)

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
	// Native requests a native session; Client is stored with it. The store
	// issues one only when the user is allowed native devices.
	Native      bool
	Client      NativeClient
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
