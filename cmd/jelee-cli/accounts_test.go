package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

type accountStoreStub struct {
	user                       domain.User
	err                        error
	bootstraps, resets, closed int
	input                      domain.UserInput
	name, hash                 string
	ctx                        context.Context
}

func (s *accountStoreStub) BootstrapAdmin(ctx context.Context, input domain.UserInput) (domain.User, error) {
	s.bootstraps++
	s.ctx, s.input = ctx, input
	return s.user, s.err
}

func (s *accountStoreStub) SetLocalPassword(ctx context.Context, name, hash string) (domain.User, error) {
	s.resets++
	s.ctx, s.name, s.hash = ctx, name, hash
	return s.user, s.err
}

type accountHashFunc func(context.Context, string) (string, error)

func (f accountHashFunc) Hash(ctx context.Context, value string) (string, error) {
	return f(ctx, value)
}

type accountCLIFixture struct {
	dependencies accountCLIDependencies
	store        *accountStoreStub
	loaded       int
	hashed       int
	opened       int
	secret       string
	work         password.Config
	config       config.Config
}

func newAccountCLIFixture() *accountCLIFixture {
	f := &accountCLIFixture{
		store:  &accountStoreStub{user: domain.User{ID: "fdf8be29-41af-4b60-ab36-e2edcc31774d", Name: "admin", DisplayName: "管理员", Locale: "zh-CN", Admin: true, CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}},
		config: config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 3, Accounts: config.DefaultAccountsConfig()},
	}
	f.dependencies = accountCLIDependencies{
		load: func() (config.Config, error) { f.loaded++; return f.config, nil },
		newHasher: func(cfg password.Config) (accountCLIHasher, error) {
			f.work = cfg
			return accountHashFunc(func(_ context.Context, secret string) (string, error) {
				f.hashed++
				f.secret = secret
				return "private encoded hash", nil
			}), nil
		},
		open: func(_ context.Context, cfg config.Config) (accountCLIStore, func(), error) {
			f.opened++
			if cfg.DatabaseURL != f.config.DatabaseURL || cfg.MaxConnections != f.config.MaxConnections {
				return nil, nil, errors.New("configuration not forwarded")
			}
			return f.store, func() { f.store.closed++ }, nil
		},
	}
	return f
}

func accountArgs(command string) []string {
	return []string{command, "--name", "admin", "--password-stdin"}
}

func TestAccountCLICommandRoutingAndPrivateSummary(t *testing.T) {
	for _, command := range []string{"bootstrap", "set-password"} {
		t.Run(command, func(t *testing.T) {
			f := newAccountCLIFixture()
			ctx := context.WithValue(context.Background(), accountTestContextKey{}, "caller marker")
			args := accountArgs(command)
			if command == "bootstrap" {
				args = append(args, "--display-name", "管理员", "--locale", "ja-JP")
			}
			var out, diagnostics bytes.Buffer
			exit := runAccountCLIWith(ctx, args, strings.NewReader("  private password  \r\n"), &out, &diagnostics, f.dependencies)
			if exit != 0 || diagnostics.Len() != 0 || f.secret != "  private password  " || f.hashed != 1 || f.opened != 1 || f.store.closed != 1 || f.store.ctx != ctx {
				t.Fatalf("unexpected command result: exit=%d diagnostics=%s", exit, &diagnostics)
			}
			if f.work != password.DefaultConfig() {
				t.Fatalf("configuration not mapped: %#v", f.work)
			}
			if command == "bootstrap" {
				in := f.store.input
				if f.store.bootstraps != 1 || f.store.resets != 0 || in.Name != "admin" || in.DisplayName != "管理员" || in.Locale != "ja-JP" || !in.Admin || in.Disabled || in.PasswordHash != "private encoded hash" {
					t.Fatal("bootstrap input or operation differs")
				}
			} else if f.store.resets != 1 || f.store.bootstraps != 0 || f.store.name != "admin" || f.store.hash != "private encoded hash" {
				t.Fatal("password reset input or operation differs")
			}
			var got domain.User
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got != f.store.user {
				t.Fatalf("invalid public summary: %v", err)
			}
			for _, secret := range []string{f.secret, "private encoded hash", f.config.DatabaseURL, "password", "token"} {
				if strings.Contains(out.String()+diagnostics.String(), secret) {
					t.Fatal("credential material was emitted")
				}
			}
		})
	}
}

func TestAccountCLIUsesRealHashAndDefaultLocale(t *testing.T) {
	f := newAccountCLIFixture()
	f.config.Accounts.PasswordMemoryKiB = int(password.MinMemoryKiB)
	f.config.Accounts.PasswordIterations = 2
	f.config.Accounts.PasswordParallelism = 1
	f.dependencies.newHasher = func(cfg password.Config) (accountCLIHasher, error) { return password.New(cfg) }
	secret := "  实际密码🙂\x00  "
	var out, diagnostics bytes.Buffer
	if exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), strings.NewReader(secret+"\n"), &out, &diagnostics, f.dependencies); exit != 0 {
		t.Fatalf("exit=%d diagnostics=%s", exit, &diagnostics)
	}
	if f.store.input.Locale != "zh-CN" {
		t.Fatal("default locale differs")
	}
	verifier, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := verifier.Verify(context.Background(), secret, f.store.input.PasswordHash); err != nil || !matched {
		t.Fatalf("created password did not preserve input: %v", err)
	}
	if matched, err := verifier.Verify(context.Background(), strings.TrimSpace(secret), f.store.input.PasswordHash); err != nil || matched {
		t.Fatalf("password unexpectedly trimmed: %v", err)
	}
}

func TestAccountCLIUsageNeverEchoesSecretArguments(t *testing.T) {
	for _, args := range [][]string{
		nil, {"other"}, {"bootstrap"}, {"bootstrap", "--name", "admin"},
		{"bootstrap", "--name", "admin", "--password", "argv-private-secret"},
		{"bootstrap", "--name", "admin", "--password-stdin=argv-private-secret"},
		{"bootstrap", "--name", "admin", "--password-stdin=false"},
		{"bootstrap", "--name", "admin", "--password-stdin", "argv-private-secret"},
		{"bootstrap", "--name", " bad ", "--password-stdin"},
		{"bootstrap", "--name", "bad\nname", "--password-stdin"},
		{"bootstrap", "--name", strings.Repeat("n", 129), "--password-stdin"},
		{"bootstrap", "--name", "admin", "--locale", "unknown", "--password-stdin"},
		{"bootstrap", "--name", "admin", "--display-name", "bad\nname", "--password-stdin"},
		{"bootstrap", "--name", "admin", "--display-name", strings.Repeat("x", 129), "--password-stdin"},
		{"bootstrap", "--name", "admin", "--display-name", strings.Repeat("界", 43), "--password-stdin"},
		{"set-password", "--name", "admin", "--locale", "zh-CN", "--password-stdin"},
		{"set-password", "--name", "admin", "--display-name", "text", "--password-stdin"},
	} {
		f := newAccountCLIFixture()
		var out, diagnostics bytes.Buffer
		exit := runAccountCLIWith(context.Background(), args, strings.NewReader("stdin-private-secret"), &out, &diagnostics, f.dependencies)
		if exit != 2 || out.Len() != 0 || !strings.HasPrefix(diagnostics.String(), "usage: ") || strings.Contains(diagnostics.String(), "private-secret") || f.loaded != 0 || f.hashed != 0 || f.opened != 0 {
			t.Fatalf("invalid argv leaked or performed work: exit=%d diagnostics=%s", exit, &diagnostics)
		}
	}
}

func TestAccountPasswordInputBoundariesAndLineEnding(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"abcdefghijkl", "abcdefghijkl"},
		{"  password  \n", "  password  "},
		{"  password  \r\n", "  password  "},
		{"abcdefghijkl\r", "abcdefghijkl\r"},
		{"abcdefghijkl\n\n", "abcdefghijkl\n"},
		{"abcdefghijkl\r\n\r\n", "abcdefghijkl\r\n"},
		{strings.Repeat("a", 1024), strings.Repeat("a", 1024)},
		{strings.Repeat("a", 1024) + "\n", strings.Repeat("a", 1024)},
		{strings.Repeat("a", 1024) + "\r\n", strings.Repeat("a", 1024)},
	} {
		got, err := readAccountPassword(context.Background(), strings.NewReader(tc.input))
		if err != nil || got != tc.want {
			t.Fatalf("input length %d was changed or rejected: %v", len(tc.input), err)
		}
	}
	for _, input := range []string{"", "abcdefghijk\n", "abc\xffdefghijkl", strings.Repeat("a", 1025), strings.Repeat("a", 1025) + "\n", strings.Repeat("a", 1025) + "\r\n", strings.Repeat("a", 1<<20)} {
		f := newAccountCLIFixture()
		reader := strings.NewReader(input)
		var out, diagnostics bytes.Buffer
		if exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), reader, &out, &diagnostics, f.dependencies); exit != 2 || diagnostics.String() != "account_password_invalid\n" || f.hashed != 0 || f.opened != 0 {
			t.Fatalf("invalid stdin performed expensive work: exit=%d diagnostics=%s", exit, &diagnostics)
		}
		if len(input)-reader.Len() > 1027 {
			t.Fatal("read beyond bounded password input")
		}
	}
}

func TestAccountPasswordConfigurationRejectsNarrowingAndDisabledAccountBypass(t *testing.T) {
	for _, change := range []func(*config.AccountsConfig){
		func(c *config.AccountsConfig) { c.PasswordMemoryKiB = -1 },
		func(c *config.AccountsConfig) { c.PasswordMemoryKiB = 131073 },
		func(c *config.AccountsConfig) { c.PasswordIterations = -1 },
		func(c *config.AccountsConfig) { c.PasswordIterations = 7 },
		func(c *config.AccountsConfig) { c.PasswordParallelism = 257 },
		func(c *config.AccountsConfig) { c.PasswordParallelism = -255 },
		func(c *config.AccountsConfig) { c.PasswordConcurrency = 0 },
		func(c *config.AccountsConfig) { c.PasswordConcurrency = 9 },
	} {
		f := newAccountCLIFixture()
		change(&f.config.Accounts)
		var out, diagnostics bytes.Buffer
		if exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), strings.NewReader("a valid password"), &out, &diagnostics, f.dependencies); exit != 1 || diagnostics.String() != "account_configuration_invalid\n" || f.hashed != 0 || f.opened != 0 {
			t.Fatalf("invalid signed work factors accepted: %d %s", exit, &diagnostics)
		}
	}
}

type accountErrorReader struct{}

func (accountErrorReader) Read([]byte) (int, error) { return 0, errors.New("private stdin path") }

type accountErrorWriter struct{}

func (accountErrorWriter) Write([]byte) (int, error) { return 0, errors.New("private output path") }

func TestAccountCLIErrorsAreFixedAndStoreAlwaysClosed(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		setup          func(*accountCLIFixture)
	}{
		{"config", "account_configuration_invalid", func(f *accountCLIFixture) {
			f.dependencies.load = func() (config.Config, error) { return config.Config{}, errors.New("private configuration path") }
		}},
		{"new_hasher", "account_password_hash_failed", func(f *accountCLIFixture) {
			f.dependencies.newHasher = func(password.Config) (accountCLIHasher, error) { return nil, password.ErrEntropy }
		}},
		{"hash", "account_password_hash_failed", func(f *accountCLIFixture) {
			f.dependencies.newHasher = func(password.Config) (accountCLIHasher, error) {
				return accountHashFunc(func(context.Context, string) (string, error) { return "", errors.New("private password") }), nil
			}
		}},
		{"open", "account_database_unavailable", func(f *accountCLIFixture) {
			f.dependencies.open = func(context.Context, config.Config) (accountCLIStore, func(), error) {
				return nil, nil, errors.New("private database URL")
			}
		}},
		{"store", "account_operation_failed", func(f *accountCLIFixture) { f.store.err = errors.New("private database SQL") }},
		{"conflict", "account_conflict", func(f *accountCLIFixture) { f.store.err = fmt.Errorf("private details: %w", domain.ErrConflict) }},
		{"not_found", "account_not_found", func(f *accountCLIFixture) { f.store.err = domain.ErrNotFound }},
		{"forbidden", "account_forbidden", func(f *accountCLIFixture) { f.store.err = domain.ErrForbidden }},
		{"database", "account_database_unavailable", func(f *accountCLIFixture) { f.store.err = domain.ErrDatabase }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccountCLIFixture()
			tc.setup(f)
			var out, diagnostics bytes.Buffer
			exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), strings.NewReader("a valid password"), &out, &diagnostics, f.dependencies)
			if exit != 1 || out.Len() != 0 || diagnostics.String() != tc.expected+"\n" || f.store.closed != f.opened {
				t.Fatalf("unsafe error/lifecycle: exit=%d diagnostic=%s", exit, &diagnostics)
			}
		})
	}
	f := newAccountCLIFixture()
	var out, diagnostics bytes.Buffer
	if exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), accountErrorReader{}, &out, &diagnostics, f.dependencies); exit != 1 || diagnostics.String() != "account_password_read_failed\n" || f.opened != 0 {
		t.Fatalf("read error: %d %s", exit, &diagnostics)
	}
	diagnostics.Reset()
	if exit := runAccountCLIWith(context.Background(), accountArgs("bootstrap"), strings.NewReader("a valid password"), accountErrorWriter{}, &diagnostics, f.dependencies); exit != 1 || diagnostics.String() != "account_output_failed\n" || f.store.closed != 1 || f.store.bootstraps != 1 {
		t.Fatalf("output error after commit: %d %s", exit, &diagnostics)
	}
}

type accountObservedPipe struct {
	*io.PipeReader
	entered chan struct{}
	once    sync.Once
}

func (r *accountObservedPipe) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	return r.PipeReader.Read(data)
}

func TestAccountCLICancellationReleasesBlockedInput(t *testing.T) {
	f := newAccountCLIFixture()
	reader, writer := io.Pipe()
	defer writer.Close()
	input := &accountObservedPipe{PipeReader: reader, entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runAccountCLIWith(ctx, accountArgs("bootstrap"), input, &out, &diagnostics, f.dependencies)
	}()
	<-input.entered
	cancel()
	select {
	case exit := <-done:
		if exit != 130 || diagnostics.String() != "account_cancelled\n" || out.Len() != 0 || f.hashed != 0 || f.opened != 0 {
			t.Fatalf("cancelled input continued: %d %s", exit, &diagnostics)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stdin remained blocked")
	}
}

type accountObservedWriter struct {
	*io.PipeWriter
	entered chan struct{}
	once    sync.Once
}

func (w *accountObservedWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.PipeWriter.Write(data)
}

func TestAccountCLICancellationReleasesBlockedOutputAfterCommit(t *testing.T) {
	f := newAccountCLIFixture()
	reader, writer := io.Pipe()
	defer reader.Close()
	output := &accountObservedWriter{PipeWriter: writer, entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runAccountCLIWith(ctx, accountArgs("bootstrap"), strings.NewReader("a valid password"), output, &diagnostics, f.dependencies)
	}()
	<-output.entered
	cancel()
	select {
	case exit := <-done:
		if exit != 130 || diagnostics.String() != "account_cancelled\n" || f.store.closed != 1 || f.store.bootstraps != 1 {
			t.Fatalf("cancelled output lifecycle: %d %s", exit, &diagnostics)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stdout remained blocked")
	}
}

func TestAccountCLICancelledAndExpiredContextsDoNotLoadConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		ctx     context.Context
		exit    int
		message string
	}{
		{nil, 1, "account_invalid_context"}, {ctx, 130, "account_cancelled"}, {expired, 124, "account_timeout"},
	} {
		f := newAccountCLIFixture()
		var out, diagnostics bytes.Buffer
		exit := runAccountCLIWith(tc.ctx, accountArgs("bootstrap"), strings.NewReader("a valid password"), &out, &diagnostics, f.dependencies)
		if exit != tc.exit || diagnostics.String() != tc.message+"\n" || f.loaded != 0 || f.hashed != 0 || f.opened != 0 {
			t.Fatalf("cancelled preflight continued: %d %s", exit, &diagnostics)
		}
	}
}

func TestAccountCLICancellationAfterHashDoesNotOpenStore(t *testing.T) {
	f := newAccountCLIFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.dependencies.newHasher = func(password.Config) (accountCLIHasher, error) {
		return accountHashFunc(func(context.Context, string) (string, error) { cancel(); return "private hash", nil }), nil
	}
	var out, diagnostics bytes.Buffer
	if exit := runAccountCLIWith(ctx, accountArgs("bootstrap"), strings.NewReader("a valid password"), &out, &diagnostics, f.dependencies); exit != 130 || diagnostics.String() != "account_cancelled\n" || f.opened != 0 {
		t.Fatalf("cancelled KDF continued to database: %d %s", exit, &diagnostics)
	}
}

func TestAccountCLIRealConfigFailureDoesNotExposePath(t *testing.T) {
	t.Setenv("JELEE_CONFIG", "private-nonexistent-account-config-path")
	var out, diagnostics bytes.Buffer
	if exit := runAccountCLI(context.Background(), accountArgs("bootstrap"), strings.NewReader("a valid password"), &out, &diagnostics); exit != 1 || diagnostics.String() != "account_configuration_invalid\n" || out.Len() != 0 {
		t.Fatalf("unsafe config error: %d %s", exit, &diagnostics)
	}
}

// accountTestContextKey marks the caller context so tests can see it forwarded.
type accountTestContextKey struct{}
