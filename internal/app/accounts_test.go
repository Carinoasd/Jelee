package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	accountUserID    = "12345678-1234-1234-1234-123456789abc"
	accountSessionID = "abcdef12-abcd-abcd-abcd-abcdef123456"
	accountTargetID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

// Unoverridden methods intentionally remain nil: unexpected repository work
// fails the test instead of silently succeeding through an empty stub.
type accountRepositoryFake struct {
	AccountRepository
	credentials    func(context.Context, string) (domain.Credentials, error)
	credentialsFor func(context.Context, domain.Actor, string) (domain.Credentials, error)
	commitLogin    func(context.Context, domain.LoginInput) (domain.SessionGrant, error)
	createUser     func(context.Context, domain.Actor, domain.UserInput, string) (domain.User, bool, error)
	updateUser     func(context.Context, domain.Actor, string, domain.UserInput) (domain.User, error)
	replace        func(context.Context, domain.Actor, string, domain.Credentials, string) error
}

func (f accountRepositoryFake) Credentials(ctx context.Context, name string) (domain.Credentials, error) {
	return f.credentials(ctx, name)
}
func (f accountRepositoryFake) CredentialsFor(ctx context.Context, actor domain.Actor, id string) (domain.Credentials, error) {
	return f.credentialsFor(ctx, actor, id)
}
func (f accountRepositoryFake) CommitLogin(ctx context.Context, input domain.LoginInput) (domain.SessionGrant, error) {
	return f.commitLogin(ctx, input)
}
func (f accountRepositoryFake) CreateUser(ctx context.Context, actor domain.Actor, input domain.UserInput, key string) (domain.User, bool, error) {
	return f.createUser(ctx, actor, input, key)
}
func (f accountRepositoryFake) UpdateUser(ctx context.Context, actor domain.Actor, id string, input domain.UserInput) (domain.User, error) {
	return f.updateUser(ctx, actor, id, input)
}
func (f accountRepositoryFake) ReplacePassword(ctx context.Context, actor domain.Actor, id string, expected domain.Credentials, hash string) error {
	return f.replace(ctx, actor, id, expected, hash)
}

type accountPasswordFake struct {
	hash   func(context.Context, string) (string, error)
	verify func(context.Context, string, string) (bool, error)
	dummy  func(context.Context, string) error
}

func (f accountPasswordFake) Hash(ctx context.Context, password string) (string, error) {
	return f.hash(ctx, password)
}
func (f accountPasswordFake) Verify(ctx context.Context, password, hash string) (bool, error) {
	return f.verify(ctx, password, hash)
}
func (f accountPasswordFake) DummyVerify(ctx context.Context, password string) error {
	return f.dummy(ctx, password)
}

func accountTestActor() domain.Actor {
	return domain.Actor{UserID: accountUserID, SessionID: accountSessionID, IP: "192.0.2.5"}
}
func accountTestInput() domain.UserInput {
	return domain.UserInput{Name: "alice", DisplayName: "Alice", Locale: "zh-TW", Hidden: true}
}
func accountTestOptions() AccountOptions {
	return AccountOptions{SessionTTL: 12 * time.Hour, MaxSessions: 7, LockAfter: 4, LockFor: 5 * time.Minute}
}
func accountService(t *testing.T, repository AccountRepository, passwords PasswordHasher) *Accounts {
	t.Helper()
	a, err := NewAccounts(repository, passwords, accountTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAccountsConstructorRejectsInvalidSecurityPolicy(t *testing.T) {
	for _, mutate := range []func(*AccountOptions){
		func(o *AccountOptions) { o.SessionTTL = time.Hour - 1 },
		func(o *AccountOptions) { o.SessionTTL = 30*24*time.Hour + 1 },
		func(o *AccountOptions) { o.MaxSessions = 0 }, func(o *AccountOptions) { o.MaxSessions = 101 },
		func(o *AccountOptions) { o.LockAfter = 2 }, func(o *AccountOptions) { o.LockAfter = 101 },
		func(o *AccountOptions) { o.LockFor = time.Minute - 1 }, func(o *AccountOptions) { o.LockFor = 24*time.Hour + 1 },
	} {
		opts := accountTestOptions()
		mutate(&opts)
		if _, err := NewAccounts(accountRepositoryFake{}, accountPasswordFake{}, opts); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid policy accepted: %v", err)
		}
	}
	if _, err := NewAccounts(nil, accountPasswordFake{}, accountTestOptions()); err != domain.ErrInvalid {
		t.Fatal("nil repository accepted")
	}
	if _, err := NewAccounts(accountRepositoryFake{}, nil, accountTestOptions()); err != domain.ErrInvalid {
		t.Fatal("nil password service accepted")
	}
}

func TestAccountsLoginCostAndFailureAccounting(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		notFound, legacy, corrupt bool
		matched, disabled         bool
		wantVerify, wantDummy     int
	}{
		{name: "unknown", notFound: true, wantDummy: 1},
		{name: "legacy-without-hash", legacy: true, wantDummy: 1},
		{name: "wrong-password", wantVerify: 1},
		{name: "corrupt-hash", corrupt: true, wantVerify: 1, wantDummy: 1},
		{name: "disabled-user-still-pays-KDF", disabled: true, matched: true, wantVerify: 1},
		{name: "successful-password", matched: true, wantVerify: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := domain.Credentials{UserID: accountUserID, Name: "alice", PasswordHash: "stored-phc", Version: 17, Disabled: tc.disabled}
			if tc.legacy {
				credentials.PasswordHash = ""
			}
			verifyCalls, dummyCalls, commits := 0, 0, 0
			repo := accountRepositoryFake{
				credentials: func(_ context.Context, name string) (domain.Credentials, error) {
					if name != "alice" {
						t.Fatal("wrong credential lookup")
					}
					if tc.notFound {
						return domain.Credentials{}, domain.ErrNotFound
					}
					return credentials, nil
				},
				commitLogin: func(_ context.Context, input domain.LoginInput) (domain.SessionGrant, error) {
					commits++
					opts := accountTestOptions()
					want := domain.LoginInput{Credentials: credentials, PasswordOK: tc.matched, DeviceName: "browser", IP: "192.0.2.5", MaxSessions: opts.MaxSessions, SessionTTL: opts.SessionTTL, LockAfter: opts.LockAfter, LockFor: opts.LockFor}
					if !reflect.DeepEqual(input, want) {
						t.Fatal("login did not preserve credential snapshot, password result, or lock/session policy")
					}
					if !input.PasswordOK || tc.disabled {
						return domain.SessionGrant{}, domain.ErrUnauthenticated
					}
					return domain.SessionGrant{Token: "fixture-issued-token"}, nil
				},
			}
			passwords := accountPasswordFake{
				verify: func(_ context.Context, password, hash string) (bool, error) {
					verifyCalls++
					if password != "provided-password" || hash != credentials.PasswordHash {
						t.Fatal("verification arguments differ")
					}
					if tc.corrupt {
						return false, errors.New("unsupported hash")
					}
					return tc.matched, nil
				},
				dummy: func(_ context.Context, password string) error {
					dummyCalls++
					if password != "provided-password" {
						t.Fatal("dummy verification uses a different password")
					}
					return nil
				},
			}
			grant, err := accountService(t, repo, passwords).Login(context.Background(), "alice", "provided-password", "browser", "192.0.2.5")
			if tc.matched && !tc.disabled {
				if err != nil || grant.Token != "fixture-issued-token" {
					t.Fatalf("successful login failed: %v", err)
				}
			} else if !errors.Is(err, domain.ErrUnauthenticated) || grant.Token != "" {
				t.Fatalf("failed login issued token or changed error: %v", err)
			}
			wantCommits := 1
			if tc.notFound {
				wantCommits = 0
			}
			if verifyCalls != tc.wantVerify || dummyCalls != tc.wantDummy || commits != wantCommits {
				t.Fatalf("verify=%d dummy=%d commits=%d", verifyCalls, dummyCalls, commits)
			}
		})
	}
}

func TestAccountsLoginCancellationCannotIssueToken(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		credentials := domain.Credentials{UserID: accountUserID, PasswordHash: "stored-phc"}
		if legacy {
			credentials.PasswordHash = ""
		}
		repo := accountRepositoryFake{credentials: func(context.Context, string) (domain.Credentials, error) { return credentials, nil }}
		passwords := accountPasswordFake{
			verify: func(context.Context, string, string) (bool, error) { cancel(); return true, nil },
			dummy:  func(context.Context, string) error { cancel(); return nil },
		}
		grant, err := accountService(t, repo, passwords).Login(ctx, "alice", "password", "", "")
		cancel()
		if !errors.Is(err, context.Canceled) || grant.Token != "" {
			t.Fatalf("cancelled login reached commit or issued token: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	repo := accountRepositoryFake{credentials: func(context.Context, string) (domain.Credentials, error) {
		return domain.Credentials{UserID: accountUserID, PasswordHash: "stored-phc"}, nil
	}}
	passwords := accountPasswordFake{verify: func(context.Context, string, string) (bool, error) { cancel(); return false, context.Canceled }}
	if grant, err := accountService(t, repo, passwords).Login(ctx, "alice", "password", "", ""); !errors.Is(err, context.Canceled) || grant.Token != "" {
		t.Fatal("cancelled verification fell back to dummy work or committed")
	}
	cancel()
}

func TestAccountsCreateHashesPasswordAndUpdateCannotInjectHash(t *testing.T) {
	actor, input := accountTestActor(), accountTestInput()
	input.PasswordHash = "attacker-supplied-phc"
	input.Admin = true
	hashCalls, createCalls, updateCalls := 0, 0, 0
	repo := accountRepositoryFake{
		createUser: func(_ context.Context, gotActor domain.Actor, got domain.UserInput, key string) (domain.User, bool, error) {
			createCalls++
			want := input
			want.PasswordHash = "server-generated-phc"
			if gotActor != actor || got != want || key != "request-key" {
				t.Fatal("create failed to replace untrusted hash or preserve authorized input")
			}
			return domain.User{ID: accountTargetID}, true, nil
		},
		updateUser: func(_ context.Context, gotActor domain.Actor, id string, got domain.UserInput) (domain.User, error) {
			updateCalls++
			want := input
			want.PasswordHash = ""
			if gotActor != actor || id != accountTargetID || got != want {
				t.Fatal("update passed password hash to repository")
			}
			return domain.User{ID: id}, nil
		},
	}
	passwords := accountPasswordFake{hash: func(_ context.Context, password string) (string, error) {
		hashCalls++
		if password != "new-password" {
			t.Fatal("password was not sent to hasher")
		}
		return "server-generated-phc", nil
	}}
	a := accountService(t, repo, passwords)
	user, replay, err := a.Create(context.Background(), actor, input, "new-password", "request-key")
	if err != nil || !replay || user.ID != accountTargetID {
		t.Fatalf("create result was not preserved: %v", err)
	}
	if _, err := a.Update(context.Background(), actor, accountTargetID, input); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 1 || createCalls != 1 || updateCalls != 1 {
		t.Fatalf("unexpected KDF/repository calls: %d %d %d", hashCalls, createCalls, updateCalls)
	}
	a = accountService(t, accountRepositoryFake{}, accountPasswordFake{hash: func(context.Context, string) (string, error) { return "", context.Canceled }})
	if _, _, err := a.Create(context.Background(), actor, input, "new-password", "key"); !errors.Is(err, context.Canceled) {
		t.Fatal("KDF failure reached create repository")
	}
}

func TestAccountsPasswordChangeVerifiesOldAndPreservesCASSnapshot(t *testing.T) {
	actor := accountTestActor()
	expected := domain.Credentials{UserID: actor.UserID, Name: "alice", PasswordHash: "old-phc", Version: 51}
	order := []string{}
	repo := accountRepositoryFake{
		credentialsFor: func(_ context.Context, gotActor domain.Actor, id string) (domain.Credentials, error) {
			order = append(order, "read")
			if gotActor != actor || id != actor.UserID {
				t.Fatal("password change fetched another user's credentials")
			}
			return expected, nil
		},
		replace: func(_ context.Context, gotActor domain.Actor, id string, snapshot domain.Credentials, hash string) error {
			order = append(order, "replace")
			if gotActor != actor || id != actor.UserID || !reflect.DeepEqual(snapshot, expected) || hash != "replacement-phc" {
				t.Fatal("password change lost CAS hash/version snapshot")
			}
			return domain.ErrConflict
		},
	}
	passwords := accountPasswordFake{
		verify: func(_ context.Context, password, hash string) (bool, error) {
			order = append(order, "verify")
			if password != "old-password" || hash != expected.PasswordHash {
				t.Fatal("old password was not verified")
			}
			return true, nil
		},
		hash: func(_ context.Context, password string) (string, error) {
			order = append(order, "hash")
			if password != "new-password" {
				t.Fatal("new password was not hashed")
			}
			return "replacement-phc", nil
		},
	}
	if err := accountService(t, repo, passwords).ChangePassword(context.Background(), actor, "old-password", "new-password"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CAS conflict was not preserved: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"read", "verify", "hash", "replace"}) {
		t.Fatalf("unsafe password-change order: %v", order)
	}
	for _, tc := range []struct {
		name, hash string
		verifyErr  error
	}{
		{name: "wrong-old", hash: "old-phc"}, {name: "legacy"},
		{name: "invalid-old-hash", hash: "old-phc", verifyErr: errors.New("bad PHC")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dummyCalls := 0
			r := accountRepositoryFake{credentialsFor: func(context.Context, domain.Actor, string) (domain.Credentials, error) {
				return domain.Credentials{PasswordHash: tc.hash}, nil
			}}
			p := accountPasswordFake{
				verify: func(context.Context, string, string) (bool, error) { return false, tc.verifyErr },
				dummy:  func(context.Context, string) error { dummyCalls++; return nil },
			}
			if err := accountService(t, r, p).ChangePassword(context.Background(), actor, "wrong", "replacement"); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("unverified old password reached replacement: %v", err)
			}
			if (tc.hash == "") != (dummyCalls == 1) {
				t.Fatal("legacy password did not consume dummy verification")
			}
		})
	}
}

func TestAccountsRejectInvalidActorAcrossOperationsBeforeWork(t *testing.T) {
	a := accountService(t, accountRepositoryFake{}, accountPasswordFake{})
	ctx := context.Background()
	operations := map[string]func(domain.Actor) error{
		"create": func(actor domain.Actor) error {
			_, _, err := a.Create(ctx, actor, accountTestInput(), "password", "key")
			return err
		},
		"list": func(actor domain.Actor) error { _, err := a.List(ctx, actor, "", 10, false); return err },
		"get":  func(actor domain.Actor) error { _, err := a.Get(ctx, actor, accountTargetID); return err },
		"update": func(actor domain.Actor) error {
			_, err := a.Update(ctx, actor, accountTargetID, accountTestInput())
			return err
		},
		"profile": func(actor domain.Actor) error {
			_, err := a.Profile(ctx, actor, domain.ProfileInput{Locale: "en-US"})
			return err
		},
		"delete":          func(actor domain.Actor) error { return a.Delete(ctx, actor, accountTargetID) },
		"restore":         func(actor domain.Actor) error { _, err := a.Restore(ctx, actor, accountTargetID); return err },
		"unlock":          func(actor domain.Actor) error { return a.Unlock(ctx, actor, accountTargetID) },
		"change-password": func(actor domain.Actor) error { return a.ChangePassword(ctx, actor, "old", "new") },
		"sessions":        func(actor domain.Actor) error { _, err := a.Sessions(ctx, actor, accountTargetID); return err },
		"revoke":          func(actor domain.Actor) error { return a.Revoke(ctx, actor, accountTargetID, accountSessionID) },
		"revoke-all":      func(actor domain.Actor) error { return a.RevokeAll(ctx, actor, accountTargetID) },
		"rotate":          func(actor domain.Actor) error { _, err := a.Rotate(ctx, actor, "device"); return err },
		"libraries":       func(actor domain.Actor) error { _, err := a.Libraries(ctx, actor, accountTargetID); return err },
		"set-libraries":   func(actor domain.Actor) error { return a.SetLibraries(ctx, actor, accountTargetID, nil) },
	}
	for _, actor := range []domain.Actor{
		{}, {UserID: accountUserID}, {UserID: "bad", SessionID: accountSessionID},
		{UserID: accountUserID, SessionID: strings.ToUpper(accountSessionID)},
		{UserID: accountUserID, SessionID: accountSessionID, IP: strings.Repeat("x", 46)},
	} {
		for name, operation := range operations {
			t.Run(name, func(t *testing.T) {
				err := operation(actor)
				if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("invalid actor accepted: %v", err)
				}
			})
		}
	}
}

func TestAccountsRejectInvalidTargetsPagesKeysLocaleAndACLBeforeWork(t *testing.T) {
	a := accountService(t, accountRepositoryFake{}, accountPasswordFake{})
	ctx, actor := context.Background(), accountTestActor()
	for _, id := range []string{"", "bad", strings.ToUpper(accountTargetID)} {
		for name, operation := range map[string]func() error{
			"get":            func() error { _, err := a.Get(ctx, actor, id); return err },
			"update":         func() error { _, err := a.Update(ctx, actor, id, accountTestInput()); return err },
			"delete":         func() error { return a.Delete(ctx, actor, id) },
			"restore":        func() error { _, err := a.Restore(ctx, actor, id); return err },
			"unlock":         func() error { return a.Unlock(ctx, actor, id) },
			"sessions":       func() error { _, err := a.Sessions(ctx, actor, id); return err },
			"revoke-user":    func() error { return a.Revoke(ctx, actor, id, accountSessionID) },
			"revoke-session": func() error { return a.Revoke(ctx, actor, accountTargetID, id) },
			"revoke-all":     func() error { return a.RevokeAll(ctx, actor, id) },
			"libraries":      func() error { _, err := a.Libraries(ctx, actor, id); return err },
			"set-libraries":  func() error { return a.SetLibraries(ctx, actor, id, nil) },
		} {
			t.Run(name, func(t *testing.T) {
				if err := operation(); err == nil {
					t.Fatal("invalid target accepted")
				}
			})
		}
	}
	for _, page := range []struct {
		cursor string
		limit  int
	}{{"bad", 10}, {strings.ToUpper(accountTargetID), 10}, {"", 0}, {"", 101}} {
		if _, err := a.List(ctx, actor, page.cursor, page.limit, false); err != domain.ErrInvalid {
			t.Fatal("invalid pagination accepted")
		}
	}
	for _, key := range []string{"", "has space", "\tkey", "金鑰", strings.Repeat("x", 129)} {
		if _, _, err := a.Create(ctx, actor, accountTestInput(), "password", key); err != domain.ErrInvalid {
			t.Fatal("invalid idempotency key accepted")
		}
	}
	for _, locale := range []string{"", "en", "EN-us", "fr-FR", "zh-TW\n"} {
		input := accountTestInput()
		input.Locale = locale
		if _, _, err := a.Create(ctx, actor, input, "password", "key"); err != domain.ErrInvalid {
			t.Fatal("invalid create locale accepted")
		}
		if _, err := a.Update(ctx, actor, accountTargetID, input); err != domain.ErrInvalid {
			t.Fatal("invalid update locale accepted")
		}
		if _, err := a.Profile(ctx, actor, domain.ProfileInput{Locale: locale}); err != domain.ErrInvalid {
			t.Fatal("invalid profile locale accepted")
		}
	}
	for _, ids := range [][]string{{"bad"}, {accountTargetID, accountTargetID}, make([]string, 1001)} {
		if err := a.SetLibraries(ctx, actor, accountTargetID, ids); err != domain.ErrInvalid {
			t.Fatal("invalid/duplicate/oversize ACL accepted")
		}
	}
}

func TestAccountsRejectInvalidCredentialTextBeforeWork(t *testing.T) {
	a := accountService(t, accountRepositoryFake{}, accountPasswordFake{})
	for _, name := range []string{"", " alice", "alice ", "al\nice", "\xff", strings.Repeat("x", 129)} {
		if _, err := a.Login(context.Background(), name, "password", "", ""); err != domain.ErrInvalid {
			t.Fatal("invalid login name accepted")
		}
		input := accountTestInput()
		input.Name = name
		if _, _, err := a.Create(context.Background(), accountTestActor(), input, "password", "key"); err != domain.ErrInvalid {
			t.Fatal("invalid create name accepted")
		}
	}
	for _, password := range []string{"\xff", strings.Repeat("x", 1025)} {
		if _, err := a.Login(context.Background(), "alice", password, "", ""); err != domain.ErrInvalid {
			t.Fatal("invalid password accepted")
		}
		if err := a.ChangePassword(context.Background(), accountTestActor(), password, "new"); err != domain.ErrInvalid {
			t.Fatal("invalid old password accepted")
		}
	}
	for _, device := range []string{"device\n", strings.Repeat("x", 129)} {
		if _, err := a.Login(context.Background(), "alice", "password", device, ""); err != domain.ErrInvalid {
			t.Fatal("invalid device accepted")
		}
		if _, err := a.Rotate(context.Background(), accountTestActor(), device); err != domain.ErrInvalid {
			t.Fatal("invalid rotation device accepted")
		}
	}
	if _, err := a.Login(context.Background(), "alice", "password", "", strings.Repeat("x", 46)); err != domain.ErrInvalid {
		t.Fatal("oversize IP accepted")
	}
	for _, display := range []string{"line\n", "\xff", strings.Repeat("x", 129)} {
		if _, err := a.Profile(context.Background(), accountTestActor(), domain.ProfileInput{DisplayName: display, Locale: "en-US"}); err != domain.ErrInvalid {
			t.Fatal("invalid display name accepted")
		}
		input := accountTestInput()
		input.DisplayName = display
		if _, _, err := a.Create(context.Background(), accountTestActor(), input, "password", "key"); err != domain.ErrInvalid {
			t.Fatal("invalid create display name accepted")
		}
		if _, err := a.Update(context.Background(), accountTestActor(), accountTargetID, input); err != domain.ErrInvalid {
			t.Fatal("invalid update display name accepted")
		}
	}
}
