package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

type accountCLIStore interface {
	BootstrapAdmin(context.Context, domain.UserInput) (domain.User, error)
	SetLocalPassword(context.Context, string, string) (domain.User, error)
	ResetLocalTwoFactor(context.Context, string) (domain.User, bool, error)
}

type accountCLIHasher interface {
	Hash(context.Context, string) (string, error)
}

type accountCLIDependencies struct {
	load      func() (config.Config, error)
	newHasher func(password.Config) (accountCLIHasher, error)
	open      func(context.Context, config.Config) (accountCLIStore, func(), error)
}

func runAccountCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runAccountCLIWith(ctx, argv, stdin, stdout, stderr, accountCLIDependencies{
		load: config.Load,
		newHasher: func(cfg password.Config) (accountCLIHasher, error) {
			return password.New(cfg)
		},
		open: func(ctx context.Context, cfg config.Config) (accountCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
	})
}

func runAccountCLIWith(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer, dependencies accountCLIDependencies) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli account bootstrap --name NAME [--display-name TEXT] [--locale zh-CN] --password-stdin")
		fmt.Fprintln(stderr, "       jelee-cli account set-password --name NAME --password-stdin")
		_, _ = fmt.Fprintln(stderr, "       jelee-cli account reset-two-factor --name NAME")
		return 2
	}
	if ctx == nil {
		fmt.Fprintln(stderr, "account_invalid_context")
		return 1
	}
	if len(argv) > 0 && argv[0] == "reset-two-factor" {
		return runResetTwoFactor(ctx, argv[1:], stdout, stderr, dependencies, usage)
	}
	if len(argv) == 0 || argv[0] != "bootstrap" && argv[0] != "set-password" {
		return usage()
	}
	command := argv[0]
	flags := flag.NewFlagSet("account "+command, flag.ContinueOnError)
	// Flag errors may contain argv values. Print only our fixed usage text.
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "account name")
	fromStdin := flags.Bool("password-stdin", false, "read the password from standard input")
	displayName, locale := "", "zh-CN"
	if command == "bootstrap" {
		flags.StringVar(&displayName, "display-name", "", "display name")
		flags.StringVar(&locale, "locale", "zh-CN", "account locale")
	}
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 || !*fromStdin || !app.ValidUserName(*name) || !validAccountDisplayName(displayName) || !app.ValidLocale(locale) || stdin == nil {
		return usage()
	}
	fail := func(err error, fallback string) int {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		code, exit := accountCLIFailure(err, fallback)
		fmt.Fprintln(stderr, code)
		return exit
	}
	if err := ctx.Err(); err != nil {
		return fail(err, "account_cancelled")
	}
	cfg, err := dependencies.load()
	if err != nil {
		return fail(err, "account_configuration_invalid")
	}
	work, err := accountPasswordConfig(cfg.Accounts)
	if err != nil {
		return fail(err, "account_configuration_invalid")
	}
	hasher, err := dependencies.newHasher(work)
	if err != nil {
		return fail(err, "account_password_hash_failed")
	}
	secret, err := readAccountPassword(ctx, stdin)
	if err != nil {
		return fail(err, "account_password_read_failed")
	}
	encoded, err := hasher.Hash(ctx, secret)
	if err != nil {
		return fail(err, "account_password_hash_failed")
	}
	if err = ctx.Err(); err != nil {
		return fail(err, "account_cancelled")
	}
	store, closeStore, err := dependencies.open(ctx, cfg)
	if err != nil {
		return fail(err, "account_database_unavailable")
	}
	defer closeStore()
	var user domain.User
	if command == "bootstrap" {
		user, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: *name, DisplayName: displayName, Locale: locale, Admin: true, PasswordHash: encoded})
	} else {
		user, err = store.SetLocalPassword(ctx, *name, encoded)
	}
	if err != nil {
		return fail(err, "account_operation_failed")
	}
	if err = ctx.Err(); err != nil {
		return fail(err, "account_cancelled")
	}
	// User is the explicit public projection; it contains no credentials,
	// password hash, session token or connection information.
	stopOutputCancellation := accountCloseOnCancellation(ctx, stdout)
	defer stopOutputCancellation()
	if err = json.NewEncoder(stdout).Encode(user); err != nil {
		return fail(err, "account_output_failed")
	}
	return 0
}

// runResetTwoFactor removes an account's second factor and recovery codes
// (G07.8): the operator recovery for an account, such as the only
// administrator, that lost its authenticator and recovery codes and so
// cannot reach the web reset. It needs no password; database access is the
// authority, as for set-password.
func runResetTwoFactor(ctx context.Context, argv []string, stdout, stderr io.Writer, dependencies accountCLIDependencies, usage func() int) int {
	flags := flag.NewFlagSet("account reset-two-factor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "account name")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 0 || !app.ValidUserName(*name) {
		return usage()
	}
	fail := func(err error, fallback string) int {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		code, exit := accountCLIFailure(err, fallback)
		_, _ = fmt.Fprintln(stderr, code)
		return exit
	}
	cfg, err := dependencies.load()
	if err != nil {
		return fail(err, "account_configuration_invalid")
	}
	store, closeStore, err := dependencies.open(ctx, cfg)
	if err != nil {
		return fail(err, "account_database_unavailable")
	}
	defer closeStore()
	user, reset, err := store.ResetLocalTwoFactor(ctx, *name)
	if err != nil {
		return fail(err, "account_operation_failed")
	}
	stopOutputCancellation := accountCloseOnCancellation(ctx, stdout)
	defer stopOutputCancellation()
	if err = json.NewEncoder(stdout).Encode(map[string]any{"user": user, "twoFactorReset": reset}); err != nil {
		return fail(err, "account_output_failed")
	}
	return 0
}

func accountPasswordConfig(cfg config.AccountsConfig) (password.Config, error) {
	// Check signed source values before narrowing conversions; for example 257
	// must not become an accepted uint8 parallelism of 1.
	if cfg.PasswordMemoryKiB < int(password.MinMemoryKiB) || cfg.PasswordMemoryKiB > int(password.MaxMemoryKiB) ||
		cfg.PasswordIterations < int(password.MinIterations) || cfg.PasswordIterations > int(password.MaxIterations) ||
		cfg.PasswordParallelism < 1 || cfg.PasswordParallelism > int(password.MaxParallelism) ||
		cfg.PasswordConcurrency < 1 || cfg.PasswordConcurrency > password.MaxConcurrentLimit {
		return password.Config{}, password.ErrInvalidConfig
	}
	work := password.Config{MemoryKiB: uint32(cfg.PasswordMemoryKiB), Iterations: uint32(cfg.PasswordIterations), Parallelism: uint8(cfg.PasswordParallelism), MaxConcurrent: cfg.PasswordConcurrency}
	return work, work.Validate()
}

func validAccountDisplayName(value string) bool {
	if !utf8.ValidString(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

type accountContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r accountContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(buffer)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

// On cancellation only, an io.ReadCloser is closed to release a blocked pipe.
// A generic blocking Reader must provide its own deadline/closing mechanism.
// No reader goroutine is detached when cancellation occurs.
func readAccountPassword(ctx context.Context, stdin io.Reader) (string, error) {
	stop := accountCloseOnCancellation(ctx, stdin)
	defer stop()
	// Read at most the maximum password plus CRLF plus one overflow byte.
	data, err := io.ReadAll(io.LimitReader(accountContextReader{ctx, stdin}, password.MaxPasswordBytes+3))
	defer clear(data)
	if err != nil {
		return "", err
	}
	if len(data) > password.MaxPasswordBytes+2 {
		return "", password.ErrInvalidPassword
	}
	secret := string(data)
	if strings.HasSuffix(secret, "\r\n") {
		secret = strings.TrimSuffix(secret, "\r\n")
	} else {
		secret = strings.TrimSuffix(secret, "\n")
	}
	if err := password.ValidatePassword(secret); err != nil {
		return "", err
	}
	return secret, nil
}

// Standard streams belong to this command. Normal completion leaves them open;
// cancellation closes only the stream currently being read or written. Callers
// supplying a custom io.Closer must make Close release pending I/O promptly.
func accountCloseOnCancellation(ctx context.Context, stream any) func() {
	if closer, ok := stream.(io.Closer); ok {
		closed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(closed)
			_ = closer.Close()
		})
		return func() {
			if !stop() {
				<-closed
			}
		}
	}
	return func() {}
}

func accountCLIFailure(err error, fallback string) (string, int) {
	switch {
	case errors.Is(err, context.Canceled):
		return "account_cancelled", 130
	case errors.Is(err, context.DeadlineExceeded):
		return "account_timeout", 124
	case errors.Is(err, password.ErrInvalidPassword):
		return "account_password_invalid", 2
	case errors.Is(err, password.ErrInvalidConfig):
		return "account_configuration_invalid", 1
	case errors.Is(err, domain.ErrInvalid):
		return "account_input_invalid", 2
	case errors.Is(err, domain.ErrNotFound):
		return "account_not_found", 1
	case errors.Is(err, domain.ErrConflict):
		return "account_conflict", 1
	case errors.Is(err, domain.ErrForbidden):
		return "account_forbidden", 1
	case errors.Is(err, domain.ErrDatabase):
		return "account_database_unavailable", 1
	default:
		return fallback, 1
	}
}
