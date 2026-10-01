package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoCLIRequest struct {
	command, library, id, observation, key, mode, cursor, priority string
	expected                                                       int64
	offset, limit                                                  int
}

func runNFOCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli nfo validate --root ABSOLUTE_PATH --file RELATIVE/FILE.nfo [--max-bytes N] | validate --library UUID --token-stdin --key KEY [--priority manual|background] [--url ORIGIN] | policy-get|policy-set|current-validations|issues --library UUID --token-stdin [options] | job|images --id UUID --token-stdin [--url ORIGIN]")
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	c := nfoCLIRequest{command: argv[0]}
	flags := flag.NewFlagSet("nfo "+c.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("url", "http://127.0.0.1:8097", "service origin")
	fromStdin := flags.Bool("token-stdin", false, "read bearer token from stdin")
	var root, file string
	maxBytes := int64(nfo.DefaultMaxBytes)
	switch c.command {
	case "validate":
		flags.StringVar(&root, "root", "", "absolute metadata root")
		flags.StringVar(&file, "file", "", "root-relative NFO path")
		flags.Int64Var(&maxBytes, "max-bytes", nfo.DefaultMaxBytes, "source byte limit")
		flags.StringVar(&c.library, "library", "", "library UUID")
		flags.StringVar(&c.key, "key", "", "idempotency key")
		flags.StringVar(&c.priority, "priority", domain.JobPriorityManual, "queue priority")
	case "policy-get", "policy-set", "current-validations", "issues":
		flags.StringVar(&c.library, "library", "", "library UUID")
		if c.command == "policy-set" {
			flags.StringVar(&c.mode, "mode", "", "off or read-only")
			flags.Int64Var(&c.expected, "expected-generation", 0, "current policy generation")
			flags.StringVar(&c.key, "key", "", "idempotency key")
		}
		if c.command == "current-validations" {
			flags.StringVar(&c.cursor, "cursor", "", "observation UUID cursor")
			flags.IntVar(&c.limit, "limit", domain.NFOObservationPageDefault, "page size")
		}
		if c.command == "issues" {
			flags.StringVar(&c.observation, "observation", "", "observation UUID")
			flags.IntVar(&c.offset, "offset", 0, "retained issues already consumed")
			flags.IntVar(&c.limit, "limit", domain.NFOIssuesPageMax, "issue page size")
		}
	case "job", "images":
		flags.StringVar(&c.id, "id", "", "job UUID")
	default:
		return usage()
	}
	if flags.Parse(argv[1:]) != nil || flags.NArg() != 0 {
		return usage()
	}
	seen := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if c.command == "validate" && !seen["library"] {
		if root == "" || file == "" || maxBytes < 1 || maxBytes > nfo.MaxAllowedBytes ||
			seen["url"] || seen["token-stdin"] || seen["key"] || seen["priority"] {
			return usage()
		}
		return runNFOLocal(ctx, root, file, maxBytes, stdout, stderr)
	}
	if !*fromStdin || seen["root"] || seen["file"] || seen["max-bytes"] {
		return usage()
	}
	u, err := jobsBaseURL(*base)
	if err != nil || c.command != "job" && c.command != "images" && !domain.ValidID(c.library) ||
		(c.command == "job" || c.command == "images") && !domain.ValidID(c.id) {
		return usage()
	}
	if c.command == "validate" && (!validNFOCLIKey(c.key) || c.priority != domain.JobPriorityManual && c.priority != domain.JobPriorityBackground) ||
		c.command == "policy-set" && domain.ValidateNFOPolicyUpdate(c.library, c.key, c.expected, c.mode) != nil ||
		c.command == "current-validations" && (c.limit < 1 || c.limit > domain.NFOObservationPageMax || c.cursor != "" && !domain.ValidID(c.cursor)) ||
		c.command == "issues" && (!domain.ValidID(c.observation) || c.offset < 0 || c.offset > domain.NFOIssuesMax || c.limit < 1 || c.limit > domain.NFOIssuesPageMax) {
		return usage()
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_token_read_failed")
	}
	method, body := http.MethodGet, ""
	u.Path = "/api/v1/libraries/" + c.library + "/nfo/"
	switch c.command {
	case "validate":
		u.Path += "validate"
		method = http.MethodPost
		raw, _ := json.Marshal(map[string]string{"priority": c.priority})
		body = string(raw)
	case "policy-get", "policy-set":
		u.Path += "policy"
		if c.command == "policy-set" {
			method = http.MethodPut
			raw, _ := json.Marshal(map[string]any{"mode": c.mode, "expectedGeneration": c.expected})
			body = string(raw)
		}
	case "current-validations":
		u.Path += "current-validations"
		query := url.Values{"limit": []string{strconv.Itoa(c.limit)}}
		if c.cursor != "" {
			query.Set("cursor", c.cursor)
		}
		u.RawQuery = query.Encode()
	case "issues":
		u.Path += "current-validations/" + c.observation + "/issues"
		u.RawQuery = url.Values{"offset": []string{strconv.Itoa(c.offset)}, "limit": []string{strconv.Itoa(c.limit)}}.Encode()
	case "job":
		u.Path = "/api/v1/jobs/" + c.id + "/nfo"
	case "images":
		u.Path = "/api/v1/jobs/" + c.id + "/images"
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		request.Header.Set("Idempotency-Key", c.key)
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	response, err := client.Do(request)
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_service_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && !(c.command == "validate" && response.StatusCode == http.StatusAccepted) {
		if ctx.Err() != nil {
			return nfoCLIError(ctx, stderr, "nfo_request_failed")
		}
		fmt.Fprintf(stderr, "nfo_request_rejected (HTTP %d)\n", response.StatusCode)
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	value, ok := decodeCLINFOResponse(raw, c)
	if !ok {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	stop := accountCloseOnCancellation(ctx, stdout)
	defer stop()
	if json.NewEncoder(stdout).Encode(value) != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	return 0
}

func validNFOCLIKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, value := range key {
		if value < '!' || value > '~' {
			return false
		}
	}
	return true
}

func nfoCLIError(ctx context.Context, stderr io.Writer, fallback string) int {
	if ctx.Err() != nil {
		code, exit := nfoFailure(ctx.Err())
		fmt.Fprintln(stderr, code)
		return exit
	}
	// All callers pass compiled constants, never a transport/parser diagnostic.
	fmt.Fprintln(stderr, fallback)
	return 1
}
