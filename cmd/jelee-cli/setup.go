package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

const setupUsage = "usage: jelee-cli setup --non-interactive --admin-name NAME --password-stdin [--locale zh-CN] [--admin-display-name TEXT]\n" +
	"       [--library NAME=/abs/path]... [--tmdb --tmdb-language zh-CN] [--accept-degraded-tools]\n" +
	"       [--nfo-read read-only|off] [--nfo-write off|write-back] [--image-fetch] [--image-write-back]\n" +
	"       [--network-mode local|lan|reverse-proxy] [--listen IP:PORT] [--allowed-host HOST]... [--trusted-proxy CIDR]... [--accept-privacy-notice]"

var (
	errSetupUsage = errors.New("setup usage")
	// errSetupNotImplemented is returned by the placeholder ports until the
	// PostgreSQL setup_state repository and environment probes are wired.
	errSetupNotImplemented = errors.New("setup storage is not implemented")
)

// setupCLIRunner is the app.Setup surface the command needs.
type setupCLIRunner interface {
	RunHeadless(context.Context, app.SetupPlan, string) (domain.SetupState, error)
}

type setupCLIDependencies struct {
	open func(context.Context) (setupCLIRunner, func(), error)
}

// runSetupMain is the os-level entry used by main's command dispatch.
func runSetupMain(argv []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return runSetupCLI(ctx, argv, os.Stdin, os.Stdout, os.Stderr)
}

func runSetupCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runSetupCLIWith(ctx, argv, stdin, stdout, stderr, setupCLIDependencies{open: openUnimplementedSetup})
}

// openUnimplementedSetup wires app.Setup to ports that report
// errSetupNotImplemented. The PostgreSQL adapter will replace them.
func openUnimplementedSetup(context.Context) (setupCLIRunner, func(), error) {
	ports := setupUnimplementedPorts{}
	setup, err := app.NewSetup(ports, ports, ports, time.Now)
	if err != nil {
		return nil, nil, err
	}
	return setup, func() {}, nil
}

type setupUnimplementedPorts struct{}

func (setupUnimplementedPorts) LoadSetupState(context.Context) (domain.SetupState, error) {
	return domain.SetupState{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) HasActiveAdmin(context.Context) (bool, error) {
	return false, errSetupNotImplemented
}
func (setupUnimplementedPorts) SaveSetupState(context.Context, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) CreateSetupAdmin(context.Context, domain.UserInput, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) CompleteSetup(context.Context, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) DatabaseStatus(context.Context) (app.SetupDatabaseStatus, error) {
	return app.SetupDatabaseStatus{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) InspectDirectory(context.Context, string) (app.SetupDirectoryStatus, error) {
	return app.SetupDirectoryStatus{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) ListenAvailable(context.Context, string) (bool, error) {
	return false, errSetupNotImplemented
}
func (setupUnimplementedPorts) DetectTools(context.Context) (app.SetupToolReport, error) {
	return app.SetupToolReport{}, errSetupNotImplemented
}
func (setupUnimplementedPorts) TMDBCredentialConfigured() bool { return false }
func (setupUnimplementedPorts) Hash(context.Context, string) (string, error) {
	return "", errSetupNotImplemented
}

type setupStringList []string

func (l *setupStringList) String() string { return strings.Join(*l, ",") }
func (l *setupStringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// parseSetupCLI maps flags to a plan without validating values; it fails only
// on flag syntax, missing mandatory switches or malformed NAME=PATH pairs.
func parseSetupCLI(argv []string) (app.SetupPlan, error) {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	// Flag errors may contain argv values. Print only our fixed usage text.
	flags.SetOutput(io.Discard)
	nonInteractive := flags.Bool("non-interactive", false, "run without prompts")
	fromStdin := flags.Bool("password-stdin", false, "read the administrator password from standard input")
	var plan app.SetupPlan
	var libraries, hosts, proxies setupStringList
	flags.StringVar(&plan.Locale, "locale", "zh-CN", "server and administrator locale")
	flags.StringVar(&plan.Admin.Name, "admin-name", "", "administrator account name")
	flags.StringVar(&plan.Admin.DisplayName, "admin-display-name", "", "administrator display name")
	flags.Var(&libraries, "library", "media library as NAME=/absolute/path (repeatable)")
	flags.BoolVar(&plan.TMDB.Enabled, "tmdb", false, "enable TMDB metadata (credential comes from TMDB_API_KEY_FILE)")
	flags.StringVar(&plan.TMDB.Language, "tmdb-language", "", "TMDB metadata language")
	flags.BoolVar(&plan.AcceptDegradedTools, "accept-degraded-tools", false, "continue when media tools are missing")
	flags.StringVar(&plan.MetadataPolicy.NFORead, "nfo-read", domain.NFOModeReadOnly, "NFO read mode")
	flags.StringVar(&plan.MetadataPolicy.NFOWrite, "nfo-write", domain.SetupNFOWriteOff, "NFO write mode")
	flags.BoolVar(&plan.MetadataPolicy.ImageFetch, "image-fetch", false, "fetch remote images")
	flags.BoolVar(&plan.MetadataPolicy.ImageWriteBack, "image-write-back", false, "write images next to media")
	flags.StringVar(&plan.Network.Mode, "network-mode", domain.SetupNetworkLocal, "network publication mode")
	flags.StringVar(&plan.Network.Listen, "listen", "127.0.0.1:8097", "listen address")
	flags.Var(&hosts, "allowed-host", "allowed Host header value (repeatable)")
	flags.Var(&proxies, "trusted-proxy", "trusted reverse proxy CIDR (repeatable)")
	flags.BoolVar(&plan.Network.PrivacyAcknowledged, "accept-privacy-notice", false, "acknowledge that clients learn the server address")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 0 || !*nonInteractive || !*fromStdin {
		return app.SetupPlan{}, errSetupUsage
	}
	for _, value := range libraries {
		name, path, ok := strings.Cut(value, "=")
		if !ok {
			return app.SetupPlan{}, errSetupUsage
		}
		plan.Media = append(plan.Media, domain.SetupLibrary{Name: name, Path: path})
	}
	if plan.TMDB.Enabled && plan.TMDB.Language == "" {
		plan.TMDB.Language = plan.Locale
	}
	plan.Network.AllowedHosts = []string(hosts)
	if len(plan.Network.AllowedHosts) == 0 {
		plan.Network.AllowedHosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	plan.Network.TrustedProxies = []string(proxies)
	return plan, nil
}

func writeSetupIssues(stderr io.Writer, issues []app.SetupIssue) {
	fmt.Fprintln(stderr, "setup_input_invalid")
	for _, issue := range issues {
		// Field paths and codes are fixed strings plus indexes; no values.
		fmt.Fprintf(stderr, "%s %s\n", issue.Field, issue.Code)
	}
}

func runSetupCLIWith(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer, dependencies setupCLIDependencies) int {
	if ctx == nil {
		fmt.Fprintln(stderr, "setup_invalid_context")
		return 1
	}
	plan, err := parseSetupCLI(argv)
	if err != nil || stdin == nil {
		fmt.Fprintln(stderr, setupUsage)
		return 2
	}
	if issues := app.ValidateSetupPlan(plan); len(issues) > 0 {
		writeSetupIssues(stderr, issues)
		return 2
	}
	fail := func(err error) int {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		code, exit := setupCLIFailure(err)
		fmt.Fprintln(stderr, code)
		return exit
	}
	secret, err := readAccountPassword(ctx, stdin)
	if err != nil && !errors.Is(err, password.ErrInvalidPassword) {
		return fail(err)
	}
	if issues := app.ValidateSetupPassword(plan.Admin.Name, secret); err != nil || len(issues) > 0 {
		if len(issues) == 0 {
			issues = []app.SetupIssue{{Field: "admin.password", Code: "password_length_invalid"}}
		}
		writeSetupIssues(stderr, issues)
		return 2
	}
	runner, closeRunner, err := dependencies.open(ctx)
	if err != nil {
		return fail(err)
	}
	defer closeRunner()
	state, err := runner.RunHeadless(ctx, plan, secret)
	var invalid *app.SetupValidationError
	if errors.As(err, &invalid) {
		writeSetupIssues(stderr, invalid.Issues)
		return 2
	}
	if err != nil {
		return fail(err)
	}
	stopOutputCancellation := accountCloseOnCancellation(ctx, stdout)
	defer stopOutputCancellation()
	// SetupState holds no credentials by construction.
	if err = json.NewEncoder(stdout).Encode(state); err != nil {
		return fail(fmt.Errorf("%w: %w", errSetupOutput, err))
	}
	return 0
}

var errSetupOutput = errors.New("setup output failed")

func setupCLIFailure(err error) (string, int) {
	switch {
	case errors.Is(err, context.Canceled):
		return "setup_cancelled", 130
	case errors.Is(err, context.DeadlineExceeded):
		return "setup_timeout", 124
	case errors.Is(err, errSetupNotImplemented):
		return "setup_not_implemented", 1
	case errors.Is(err, errSetupOutput):
		return "setup_output_failed", 1
	case errors.Is(err, domain.ErrSetupCompleted):
		return "setup_already_completed", 1
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrSetupStepOrder):
		return "setup_conflict", 1
	case errors.Is(err, domain.ErrDatabase):
		return "setup_database_unavailable", 1
	default:
		return "setup_failed", 1
	}
}
