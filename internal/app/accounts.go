package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// AccountRepository must authorize every operation against the live session in
// storage. Actor data is a lookup key, never a cached authorization decision.
type AccountRepository interface {
	Credentials(context.Context, string) (domain.Credentials, error)
	CredentialsFor(context.Context, domain.Actor, string) (domain.Credentials, error)
	CommitLogin(context.Context, domain.LoginInput) (domain.SessionGrant, error)
	ListUsers(context.Context, domain.Actor, string, int, bool) ([]domain.User, error)
	GetUser(context.Context, domain.Actor, string) (domain.User, error)
	CreateUser(context.Context, domain.Actor, domain.UserInput, string) (domain.User, bool, error)
	UpdateUser(context.Context, domain.Actor, string, domain.UserInput) (domain.User, error)
	UpdateProfile(context.Context, domain.Actor, domain.ProfileInput) (domain.User, error)
	GetPreferences(context.Context, domain.Actor) (domain.UserPreferences, error)
	SetPreferences(context.Context, domain.Actor, domain.UserPreferences) (domain.UserPreferences, error)
	DeleteUser(context.Context, domain.Actor, string) error
	RestoreUser(context.Context, domain.Actor, string) (domain.User, error)
	UnlockUser(context.Context, domain.Actor, string) error
	ReplacePassword(context.Context, domain.Actor, string, domain.Credentials, string) error
	ListSessions(context.Context, domain.Actor, string) ([]domain.Session, error)
	RevokeSession(context.Context, domain.Actor, string, string) error
	RevokeSessions(context.Context, domain.Actor, string) error
	RotateSession(context.Context, domain.Actor, string, time.Duration) (domain.SessionGrant, error)
	ListAllSessions(context.Context, domain.Actor, string, int) ([]domain.Session, error)
	SetNativeAccess(context.Context, domain.Actor, string, bool) (domain.User, error)
	GetDeliveryLimits(context.Context, domain.Actor, string) (domain.DeliveryLimits, error)
	SetDeliveryLimits(context.Context, domain.Actor, string, domain.DeliveryLimits) (domain.DeliveryLimits, error)
	GetLibraryAccess(context.Context, domain.Actor, string) ([]domain.LibraryGrant, error)
	ReplaceLibraryAccess(context.Context, domain.Actor, string, []string) error
	GetContentAccess(context.Context, domain.Actor, string) (domain.ContentAccessView, error)
	SetContentAccess(context.Context, domain.Actor, string, domain.ContentAccess) (domain.ContentAccessView, error)
	SetItemAccessRule(context.Context, domain.Actor, string, string, domain.ItemAccessEffect) (domain.ItemAccessRule, error)
	DeleteItemAccessRule(context.Context, domain.Actor, string, string) error
	GetAccessPolicy(context.Context, domain.Actor) (domain.AccessPolicy, error)
	SetAccessPolicy(context.Context, domain.Actor, domain.AccessPolicy) (domain.AccessPolicy, error)
	ListParentalRatings(context.Context, domain.Actor) ([]domain.ParentalRating, error)
}

type PasswordHasher interface {
	Hash(context.Context, string) (string, error)
	Verify(context.Context, string, string) (bool, error)
	DummyVerify(context.Context, string) error
}

type AccountOptions struct {
	SessionTTL  time.Duration
	MaxSessions int
	LockAfter   int
	LockFor     time.Duration
}

type Accounts struct {
	repository AccountRepository
	passwords  PasswordHasher
	options    AccountOptions
}

func NewAccounts(repository AccountRepository, passwords PasswordHasher, options AccountOptions) (*Accounts, error) {
	if repository == nil || passwords == nil || options.SessionTTL < time.Hour || options.SessionTTL > 30*24*time.Hour || options.MaxSessions < 1 || options.MaxSessions > 100 || options.LockAfter < 3 || options.LockAfter > 100 || options.LockFor < time.Minute || options.LockFor > 24*time.Hour {
		return nil, domain.ErrInvalid
	}
	return &Accounts{repository: repository, passwords: passwords, options: options}, nil
}

func validText(value string, max int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || len(value) > max || !allowEmpty && strings.TrimSpace(value) == "" {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func ValidUserName(value string) bool {
	return validText(value, 128, false) && value == strings.TrimSpace(value)
}

func ValidLocale(value string) bool {
	return value == "zh-CN" || value == "zh-TW" || value == "ja-JP" || value == "en-US"
}

func validUser(input domain.UserInput) bool {
	return ValidUserName(input.Name) && validText(input.DisplayName, 128, true) && ValidLocale(input.Locale)
}

func validActor(actor domain.Actor) bool {
	return domain.ValidID(actor.UserID) && domain.ValidID(actor.SessionID) && len(actor.IP) <= 45
}

func validTarget(actor domain.Actor, id string) bool { return validActor(actor) && domain.ValidID(id) }

func validKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// Login issues a web session. Web sessions can browse and administer but
// never play media.
func (a *Accounts) Login(ctx context.Context, name, password, deviceName, ip string) (domain.SessionGrant, error) {
	if !validText(deviceName, 128, true) {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	return a.login(ctx, name, password, ip, domain.LoginInput{DeviceName: deviceName})
}

// LoginNative issues a native session, which may use direct delivery. The
// password is verified exactly like Login; the store then refuses with
// ErrNativeLoginDisabled unless an administrator allowed the user native
// devices.
func (a *Accounts) LoginNative(ctx context.Context, name, password string, client domain.NativeClient, ip string) (domain.SessionGrant, error) {
	if !ValidNativeClient(client) {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	return a.login(ctx, name, password, ip, domain.LoginInput{DeviceName: client.Device, Native: true, Client: client})
}

// ValidNativeClient bounds every reported field and refuses control
// characters. The client name and device ID identify the device and are
// required; they may not be blank or carry surrounding spaces.
func ValidNativeClient(c domain.NativeClient) bool {
	required := func(value string, max int) bool {
		return validText(value, max, false) && value == strings.TrimSpace(value)
	}
	return required(c.Name, domain.NativeClientNameMax) && required(c.DeviceID, domain.NativeDeviceIDMax) &&
		validText(c.Device, domain.NativeDeviceNameMax, true) && validText(c.Version, domain.NativeClientVersionMax, true)
}

func (a *Accounts) login(ctx context.Context, name, password, ip string, input domain.LoginInput) (domain.SessionGrant, error) {
	if !ValidUserName(name) || !utf8.ValidString(password) || len(password) > 1024 || len(ip) > 45 {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	credentials, err := a.repository.Credentials(ctx, name)
	if errors.Is(err, domain.ErrNotFound) {
		if err = a.passwords.DummyVerify(ctx, password); err != nil {
			return domain.SessionGrant{}, fmt.Errorf("verify unknown login: %w", err)
		}
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.SessionGrant{}, fmt.Errorf("read login credentials: %w", err)
	}
	matched := false
	if credentials.PasswordHash == "" {
		err = a.passwords.DummyVerify(ctx, password)
	} else {
		matched, err = a.passwords.Verify(ctx, password, credentials.PasswordHash)
		if err != nil && ctx.Err() == nil {
			// Corrupt or unsupported hashes fail closed and consume normal KDF work.
			err = a.passwords.DummyVerify(ctx, password)
			matched = false
		}
	}
	if err != nil {
		return domain.SessionGrant{}, fmt.Errorf("verify login: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return domain.SessionGrant{}, err
	}
	input.Credentials, input.PasswordOK, input.IP = credentials, matched, ip
	input.MaxSessions, input.SessionTTL, input.LockAfter, input.LockFor = a.options.MaxSessions, a.options.SessionTTL, a.options.LockAfter, a.options.LockFor
	grant, err := a.repository.CommitLogin(ctx, input)
	if err != nil {
		return domain.SessionGrant{}, fmt.Errorf("complete login: %w", err)
	}
	return grant, nil
}

func (a *Accounts) Create(ctx context.Context, actor domain.Actor, input domain.UserInput, password, key string) (domain.User, bool, error) {
	if !validActor(actor) || !validUser(input) || !validKey(key) {
		return domain.User{}, false, domain.ErrInvalid
	}
	hash, err := a.passwords.Hash(ctx, password)
	if err != nil {
		return domain.User{}, false, fmt.Errorf("hash new password: %w", err)
	}
	input.PasswordHash = hash
	return a.repository.CreateUser(ctx, actor, input, key)
}

func (a *Accounts) List(ctx context.Context, actor domain.Actor, cursor string, limit int, includeDeleted bool) ([]domain.User, error) {
	if !validActor(actor) || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	return a.repository.ListUsers(ctx, actor, cursor, limit, includeDeleted)
}

func (a *Accounts) Get(ctx context.Context, actor domain.Actor, id string) (domain.User, error) {
	if !validTarget(actor, id) {
		return domain.User{}, domain.ErrNotFound
	}
	return a.repository.GetUser(ctx, actor, id)
}

func (a *Accounts) Update(ctx context.Context, actor domain.Actor, id string, input domain.UserInput) (domain.User, error) {
	if !validTarget(actor, id) || !validUser(input) {
		return domain.User{}, domain.ErrInvalid
	}
	input.PasswordHash = ""
	return a.repository.UpdateUser(ctx, actor, id, input)
}

func (a *Accounts) Profile(ctx context.Context, actor domain.Actor, input domain.ProfileInput) (domain.User, error) {
	if !validActor(actor) || !validText(input.DisplayName, 128, true) || !ValidLocale(input.Locale) {
		return domain.User{}, domain.ErrInvalid
	}
	return a.repository.UpdateProfile(ctx, actor, input)
}

// Preferences reads the caller's interface preferences (G33.3).
func (a *Accounts) Preferences(ctx context.Context, actor domain.Actor) (domain.UserPreferences, error) {
	if !validActor(actor) {
		return domain.UserPreferences{}, domain.ErrInvalid
	}
	return a.repository.GetPreferences(ctx, actor)
}

// SetPreferences replaces the caller's interface preferences.
func (a *Accounts) SetPreferences(ctx context.Context, actor domain.Actor, preferences domain.UserPreferences) (domain.UserPreferences, error) {
	if !validActor(actor) || !preferences.Valid() {
		return domain.UserPreferences{}, domain.ErrInvalid
	}
	return a.repository.SetPreferences(ctx, actor, preferences)
}

func (a *Accounts) Delete(ctx context.Context, actor domain.Actor, id string) error {
	if !validTarget(actor, id) {
		return domain.ErrNotFound
	}
	return a.repository.DeleteUser(ctx, actor, id)
}

func (a *Accounts) Restore(ctx context.Context, actor domain.Actor, id string) (domain.User, error) {
	if !validTarget(actor, id) {
		return domain.User{}, domain.ErrNotFound
	}
	return a.repository.RestoreUser(ctx, actor, id)
}

func (a *Accounts) Unlock(ctx context.Context, actor domain.Actor, id string) error {
	if !validTarget(actor, id) {
		return domain.ErrNotFound
	}
	return a.repository.UnlockUser(ctx, actor, id)
}

func (a *Accounts) ChangePassword(ctx context.Context, actor domain.Actor, oldPassword, newPassword string) error {
	if !validActor(actor) || !utf8.ValidString(oldPassword) || len(oldPassword) > 1024 {
		return domain.ErrInvalid
	}
	credentials, err := a.repository.CredentialsFor(ctx, actor, actor.UserID)
	if err != nil {
		return fmt.Errorf("read own credentials: %w", err)
	}
	if credentials.PasswordHash == "" {
		if err = a.passwords.DummyVerify(ctx, oldPassword); err != nil {
			return err
		}
		return domain.ErrPasswordMismatch
	}
	matched, err := a.passwords.Verify(ctx, oldPassword, credentials.PasswordHash)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.ErrPasswordMismatch
	}
	if !matched {
		return domain.ErrPasswordMismatch
	}
	hash, err := a.passwords.Hash(ctx, newPassword)
	if err != nil {
		return fmt.Errorf("hash replacement password: %w", err)
	}
	return a.repository.ReplacePassword(ctx, actor, actor.UserID, credentials, hash)
}

func (a *Accounts) Sessions(ctx context.Context, actor domain.Actor, id string) ([]domain.Session, error) {
	if !validTarget(actor, id) {
		return nil, domain.ErrNotFound
	}
	return a.repository.ListSessions(ctx, actor, id)
}

func (a *Accounts) Revoke(ctx context.Context, actor domain.Actor, id, sessionID string) error {
	if !validTarget(actor, id) || !domain.ValidID(sessionID) {
		return domain.ErrNotFound
	}
	return a.repository.RevokeSession(ctx, actor, id, sessionID)
}

func (a *Accounts) RevokeAll(ctx context.Context, actor domain.Actor, id string) error {
	if !validTarget(actor, id) {
		return domain.ErrNotFound
	}
	return a.repository.RevokeSessions(ctx, actor, id)
}

// AllSessions lists active sessions of every user for an administrator,
// ordered by session ID with cursor pagination.
func (a *Accounts) AllSessions(ctx context.Context, actor domain.Actor, cursor string, limit int) ([]domain.Session, error) {
	if !validActor(actor) || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	return a.repository.ListAllSessions(ctx, actor, cursor, limit)
}

// SetNativeAccess lets an administrator allow or withdraw native logins for
// a user. Withdrawing also revokes the user's active native sessions.
func (a *Accounts) SetNativeAccess(ctx context.Context, actor domain.Actor, id string, allow bool) (domain.User, error) {
	if !validTarget(actor, id) {
		return domain.User{}, domain.ErrNotFound
	}
	return a.repository.SetNativeAccess(ctx, actor, id, allow)
}

// DeliveryLimits reads a user's direct delivery overrides (administrator only).
func (a *Accounts) DeliveryLimits(ctx context.Context, actor domain.Actor, id string) (domain.DeliveryLimits, error) {
	if !validTarget(actor, id) {
		return domain.DeliveryLimits{}, domain.ErrNotFound
	}
	return a.repository.GetDeliveryLimits(ctx, actor, id)
}

// SetDeliveryLimits replaces a user's direct delivery overrides (G07.4). The
// new values govern streams that start afterwards; streams already running
// keep the limits they were admitted with.
func (a *Accounts) SetDeliveryLimits(ctx context.Context, actor domain.Actor, id string, limits domain.DeliveryLimits) (domain.DeliveryLimits, error) {
	if !validTarget(actor, id) {
		return domain.DeliveryLimits{}, domain.ErrNotFound
	}
	if !limits.Valid() {
		return domain.DeliveryLimits{}, domain.ErrInvalid
	}
	return a.repository.SetDeliveryLimits(ctx, actor, id, limits)
}

func (a *Accounts) Rotate(ctx context.Context, actor domain.Actor, deviceName string) (domain.SessionGrant, error) {
	if !validActor(actor) || !validText(deviceName, 128, true) {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	return a.repository.RotateSession(ctx, actor, deviceName, a.options.SessionTTL)
}

func (a *Accounts) Libraries(ctx context.Context, actor domain.Actor, id string) ([]domain.LibraryGrant, error) {
	if !validTarget(actor, id) {
		return nil, domain.ErrNotFound
	}
	return a.repository.GetLibraryAccess(ctx, actor, id)
}

func (a *Accounts) SetLibraries(ctx context.Context, actor domain.Actor, id string, libraryIDs []string) error {
	if !validTarget(actor, id) || len(libraryIDs) > 1000 {
		return domain.ErrInvalid
	}
	seen := map[string]bool{}
	for _, libraryID := range libraryIDs {
		if !domain.ValidID(libraryID) || seen[libraryID] {
			return domain.ErrInvalid
		}
		seen[libraryID] = true
	}
	return a.repository.ReplaceLibraryAccess(ctx, actor, id, libraryIDs)
}
