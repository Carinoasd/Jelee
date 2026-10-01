package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoCLIData(command string) map[string]any {
	switch command {
	case "validate":
		return validJobsCLIJob()
	case "policy-get", "policy-set":
		return map[string]any{"libraryId": jobsTestID, "mode": "read-only", "generation": 2}
	case "job":
		return map[string]any{"jobId": jobsTestID, "libraryId": jobsTestID, "mode": "read-only", "phase": "done",
			"processed": 7, "hits": 1, "negativeHits": 1, "parsed": 2, "valid": 2, "invalid": 2,
			"warningFiles": 1, "changed": 1, "unavailable": 1, "rejected": 1}
	case "images":
		return map[string]any{"jobId": jobsTestID, "libraryId": jobsTestID, "added": 0, "changed": 23,
			"unchanged": 477, "missing": 0, "uncompared": 0, "comparisonComplete": true}
	case "current-validations":
		return map[string]any{"items": []any{map[string]any{"id": jobsTestID, "rootId": jobsTestID, "path": "Series/episode.nfo", "status": "valid", "entries": 1,
			"failureCode": "", "warningCount": 0, "errorCount": 0, "issueCount": 0, "issuesTruncated": false,
			"observedAt": "2026-10-01T01:02:03Z", "expiresAt": "2026-10-01T02:02:03Z"}}}
	case "issues":
		return map[string]any{"observationId": jobsTestID, "entries": 1, "failureCode": "", "issueCount": 1, "issuesTruncated": false, "offset": 0,
			"issues": []any{map[string]any{"severity": "warning", "code": "nfo_title_missing", "field": "title", "entry": 0}}}
	}
	panic("unknown test command")
}

func nfoCLIContract(command string) nfoCLIRequest {
	return nfoCLIRequest{command: command, library: jobsTestID, id: jobsTestID, observation: jobsTestID,
		mode: domain.NFOModeReadOnly, expected: 1, limit: 2}
}

func nfoCLIArgs(command, origin string) []string {
	args := []string{command, "--url", origin, "--token-stdin"}
	if command == "job" || command == "images" {
		return append(args, "--id", jobsTestID)
	}
	args = append(args, "--library", jobsTestID)
	switch command {
	case "validate":
		args = append(args, "--key", "nfo-test", "--priority", "background")
	case "policy-set":
		args = append(args, "--key", "nfo-test", "--mode", "read-only", "--expected-generation", "1")
	case "current-validations":
		args = append(args, "--limit", "2")
	case "issues":
		args = append(args, "--observation", jobsTestID, "--limit", "2")
	}
	return args
}

func TestNFOCLIUsesFixedAuthenticatedRoutes(t *testing.T) {
	for _, tc := range []struct {
		command, method, path, query string
		body                         map[string]any
	}{
		{"validate", "POST", "/api/v1/libraries/" + jobsTestID + "/nfo/validate", "", map[string]any{"priority": "background"}},
		{"policy-get", "GET", "/api/v1/libraries/" + jobsTestID + "/nfo/policy", "", nil},
		{"policy-set", "PUT", "/api/v1/libraries/" + jobsTestID + "/nfo/policy", "", map[string]any{"mode": "read-only", "expectedGeneration": float64(1)}},
		{"current-validations", "GET", "/api/v1/libraries/" + jobsTestID + "/nfo/current-validations", "limit=2", nil},
		{"issues", "GET", "/api/v1/libraries/" + jobsTestID + "/nfo/current-validations/" + jobsTestID + "/issues", "limit=2&offset=0", nil},
		{"job", "GET", "/api/v1/jobs/" + jobsTestID + "/nfo", "", nil},
		{"images", "GET", "/api/v1/jobs/" + jobsTestID + "/images", "", nil},
	} {
		t.Run(tc.command, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != tc.query || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) {
					t.Error("wrong authenticated route")
				}
				body, _ := io.ReadAll(r.Body)
				if tc.body == nil {
					if len(body) != 0 || r.Header.Get("Idempotency-Key") != "" {
						t.Error("read request carried mutation fields")
					}
				} else {
					var decoded map[string]any
					if json.Unmarshal(body, &decoded) != nil || !reflect.DeepEqual(decoded, tc.body) || r.Header.Get("Idempotency-Key") != "nfo-test" || r.Header.Get("Content-Type") != "application/json" {
						t.Error("wrong fixed mutation body")
					}
				}
				if tc.command == "validate" {
					w.WriteHeader(http.StatusAccepted)
				}
				data := nfoCLIData(tc.command)
				data["rawXML"], data["identity"], data["RootID"] = "private-payload", "private-payload", "private-payload"
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "token": "private-payload"})
			}))
			defer server.Close()
			var out, diagnostics bytes.Buffer
			status := runNFOCLI(context.Background(), nfoCLIArgs(tc.command, server.URL), strings.NewReader(strings.Repeat("a", 43)+"\r\n"), &out, &diagnostics)
			if status != 0 || calls != 1 || diagnostics.Len() != 0 || strings.Contains(out.String(), "private-payload") || strings.Contains(out.String(), strings.Repeat("a", 43)) {
				t.Fatalf("fixed route failed: status=%d calls=%d", status, calls)
			}
		})
	}
}

func TestNFOCLIRejectsAmbiguousModesBeforeCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"validate", "--library", jobsTestID, "--token-stdin"},
		{"validate", "--library", jobsTestID, "--key", "k"},
		{"validate", "--library", jobsTestID, "--key", "k", "--token-stdin", "--root="},
		{"validate", "--library", jobsTestID, "--key", "k", "--token-stdin", "--file="},
		{"validate", "--library", jobsTestID, "--key", "k", "--token-stdin", "--max-bytes", "8388608"},
		{"validate", "--root", "private", "--file", "movie.nfo", "--library="},
		{"validate", "--root", "private", "--file", "movie.nfo", "--token-stdin=false"},
		{"validate", "--root", "private", "--file", "movie.nfo", "--url", "http://127.0.0.1:8097"},
		{"validate", "--library", jobsTestID, "--key", "bad key", "--token-stdin"},
		{"validate", "--library", jobsTestID, "--key", "k", "--priority", "urgent", "--token-stdin"},
		{"validate", "--library", jobsTestID, "--key", "k", "--token-stdin", "--probe"},
		{"policy-get", "--library", jobsTestID, "--key", "k", "--token-stdin"},
		{"policy-set", "--library", jobsTestID, "--mode", "read-write", "--key", "k", "--expected-generation", "1", "--token-stdin"},
		{"policy-set", "--library", jobsTestID, "--mode", "off", "--key", "k", "--token-stdin"},
		{"current-validations", "--library", jobsTestID, "--limit", "51", "--token-stdin"},
		{"current-validations", "--library", jobsTestID, "--cursor", "private", "--token-stdin"},
		{"issues", "--library", jobsTestID, "--observation", jobsTestID, "--offset", "65", "--token-stdin"},
		{"issues", "--library", jobsTestID, "--observation", jobsTestID, "--limit", "33", "--token-stdin"},
		{"issues", "--library", jobsTestID, "--token-stdin"},
		{"job", "--library", jobsTestID, "--token-stdin"},
		{"images", "--id", "private", "--token-stdin"},
		{"job", "--id", jobsTestID, "--url", "http://example.com", "--token-stdin"},
		{"job", "--id", jobsTestID, "--url", "https://user:secret@example.com", "--token-stdin"},
		{"job", "--id", jobsTestID, "--token-stdin", "extra"},
	} {
		var out, diagnostics bytes.Buffer
		if status := runNFOCLI(context.Background(), args, unreadProbeToken{t}, &out, &diagnostics); status != 2 || out.Len() != 0 || !strings.HasPrefix(diagnostics.String(), "usage:") || strings.Contains(diagnostics.String(), "secret") {
			t.Fatal("invalid command consumed credentials or escaped fixed usage")
		}
	}
}

func TestNFOCLILocalModeUsesParsedFlagsAndRemainsDatabaseFree(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "invalid-private-database")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "--library"), []byte("<movie><title>Private</title></movie>"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	status := runNFOCLI(context.Background(), []string{"validate", "--root", root, "--file", "--library"}, unreadProbeToken{t}, &out, &diagnostics)
	if status != 0 || diagnostics.Len() != 0 || strings.Contains(out.String(), "Private") || strings.Contains(out.String(), root) {
		t.Fatal("a local filename was mistaken for a remote option")
	}
}

func TestNFOCLIResponsesRequireExactFieldsAndSemanticValidation(t *testing.T) {
	for _, command := range []string{"policy-get", "policy-set", "job", "images", "current-validations", "issues"} {
		request := nfoCLIContract(command)
		for key := range nfoCLIData(command) {
			for _, remove := range []bool{true, false} {
				data := nfoCLIData(command)
				if remove {
					delete(data, key)
				} else {
					data[key] = nil
				}
				if value, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), request); valid || value != nil {
					t.Fatalf("missing/null field accepted for %s/%s", command, key)
				}
			}
		}
		for _, raw := range [][]byte{[]byte(`null`), []byte(`{}`), []byte(`{"Data":{}}`), []byte(`{"data":null}`), []byte(`{"data":{},"data":{}}`), []byte(`{"data":{}} {}`), {0xff}} {
			if value, valid := decodeCLINFOResponse(raw, request); valid || value != nil {
				t.Fatal("invalid successful envelope accepted")
			}
		}
	}
	for _, tc := range []struct {
		command, key string
		value        any
	}{
		{"policy-get", "generation", 0}, {"policy-set", "generation", 3}, {"policy-set", "mode", "off"},
		{"job", "processed", 8}, {"job", "hits", 1<<63 - 1}, {"job", "mode", "read-write"},
		{"job", "phase", "private"}, {"job", "errorCode", "private-path"}, {"job", "errorCode", nil},
		{"images", "missing", -1}, {"images", "added", 500001}, {"images", "uncompared", 1},
		{"current-validations", "nextCursor", jobsTestID + "bad"}, {"current-validations", "nextCursor", nil},
		{"issues", "nextOffset", 1}, {"issues", "nextOffset", nil}, {"issues", "offset", 1}, {"issues", "issueCount", 65},
	} {
		data := nfoCLIData(tc.command)
		data[tc.key] = tc.value
		if value, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), nfoCLIContract(tc.command)); valid || value != nil {
			t.Fatalf("unsafe semantics accepted for %s/%s", tc.command, tc.key)
		}
	}
}

func TestNFOCLINestedFieldsCannotLeakOrOverride(t *testing.T) {
	for _, command := range []string{"current-validations", "issues"} {
		listKey := "items"
		if command == "issues" {
			listKey = "issues"
		}
		data := nfoCLIData(command)
		item := data[listKey].([]any)[0].(map[string]any)
		for key := range item {
			for _, remove := range []bool{true, false} {
				copy := nfoCLIData(command)
				nested := copy[listKey].([]any)[0].(map[string]any)
				if remove {
					delete(nested, key)
				} else {
					nested[key] = nil
				}
				if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, copy), nfoCLIContract(command)); valid {
					t.Fatal("missing/null nested public field accepted")
				}
			}
		}
		item["rawXML"], item["Code"], item["Path"] = "private-payload", "private-payload", "/private-payload"
		value, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), nfoCLIContract(command))
		raw, err := json.Marshal(value)
		if !valid || err != nil || bytes.Contains(raw, []byte("private-payload")) {
			t.Fatal("unknown nested fields leaked or overrode exact public fields")
		}
		encoded := jobsCLIJSON(t, nfoCLIData(command))
		field := `"entry":0`
		if command == "current-validations" {
			field = `"entries":1`
		}
		duplicate := bytes.Replace(encoded, []byte(field), []byte(field+","+field), 1)
		if _, valid := decodeCLINFOResponse(duplicate, nfoCLIContract(command)); valid {
			t.Fatal("duplicate nested field accepted")
		}
	}
	for _, path := range []string{"../private", "/absolute/private", `C:\private`, "line\nprivate"} {
		data := nfoCLIData("current-validations")
		data["items"].([]any)[0].(map[string]any)["path"] = path
		if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), nfoCLIContract("current-validations")); valid {
			t.Fatal("unsafe current observation path accepted")
		}
	}
}

func TestNFOCLIResponseBindingAndPageBounds(t *testing.T) {
	other := "22222222-2222-4222-8222-222222222222"
	for _, command := range []string{"validate", "policy-get", "policy-set", "job", "images", "issues"} {
		data := nfoCLIData(command)
		key := "libraryId"
		if command == "job" || command == "images" {
			key = "jobId"
		} else if command == "issues" {
			key = "observationId"
		}
		data[key] = other
		if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), nfoCLIContract(command)); valid {
			t.Fatal("response escaped requested resource binding")
		}
	}
	request := nfoCLIContract("current-validations")
	request.cursor = jobsTestID
	if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, nfoCLIData(request.command)), request); valid {
		t.Fatal("page moved backward past requested cursor")
	}
	for _, command := range []string{"current-validations", "issues"} {
		request := nfoCLIContract(command)
		request.limit = 0
		if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, nfoCLIData(command)), request); valid {
			t.Fatal("reply exceeded requested page limit")
		}
	}
}

func TestNFOCLIErrorsRedirectsAndBodyBoundsAreSafe(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 429, 500, 503, 202} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "private-payload")
		}))
		var out, diagnostics bytes.Buffer
		exit := runNFOCLI(context.Background(), nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
		server.Close()
		if exit != 1 || out.Len() != 0 || strings.Contains(diagnostics.String(), "private-payload") || !strings.HasPrefix(diagnostics.String(), "nfo_request_rejected (HTTP ") {
			t.Fatal("service error disclosed data or non-GET status accepted")
		}
	}
	for _, body := range []string{`{"data":null,"secret":"private-payload"}`, strings.Repeat("x", (1<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		var out, diagnostics bytes.Buffer
		exit := runNFOCLI(context.Background(), nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
		server.Close()
		if exit != 1 || out.Len() != 0 || diagnostics.String() != "nfo_response_invalid\n" {
			t.Fatal("unbounded/invalid successful response accepted")
		}
	}
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if exit := runNFOCLI(context.Background(), nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); exit != 1 || redirected != 0 || out.Len() != 0 || diagnostics.String() != "nfo_service_unavailable\n" {
		t.Fatal("redirect forwarded credentials")
	}
}

func TestNFOCLIContextAndOutputErrorsAreFixed(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		wantExit, wantMessage := 130, "nfo_cancelled\n"
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			wantExit, wantMessage = 124, "nfo_timeout\n"
		} else {
			cancel()
		}
		var out, diagnostics bytes.Buffer
		exit := runNFOCLI(ctx, nfoCLIArgs("job", "http://127.0.0.1:8097"), unreadProbeToken{t}, &out, &diagnostics)
		cancel()
		if exit != wantExit || out.Len() != 0 || diagnostics.String() != wantMessage {
			t.Fatal("context cause lost or credentials read after cancellation")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nfoCLIData("job")})
	}))
	defer server.Close()
	var diagnostics bytes.Buffer
	if status := runNFOCLI(context.Background(), nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), failedWriter{}, &diagnostics); status != 1 || diagnostics.String() != "nfo_output_failed\n" {
		t.Fatal("output error was not fixed")
	}
	for _, token := range []string{"", "private-token", strings.Repeat("a", 44), strings.Repeat("a", 42) + "!"} {
		var out, diagnostics bytes.Buffer
		if status := runNFOCLI(context.Background(), nfoCLIArgs("job", server.URL), strings.NewReader(token), &out, &diagnostics); status != 1 || out.Len() != 0 || diagnostics.String() != "nfo_token_read_failed\n" {
			t.Fatal("invalid stdin token accepted or disclosed")
		}
	}
}

func TestJobsCLINFOOptInOnlyOnScan(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var data map[string]any
			want := map[string]any{"priority": "manual", "probe": true}
			if enabled {
				want["nfo"] = true
			}
			if json.NewDecoder(r.Body).Decode(&data) != nil || !reflect.DeepEqual(data, want) {
				t.Error("wrong independent opt-in fields")
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": validJobsCLIJob()})
		}))
		args := []string{"scan", "--id", jobsTestID, "--key", "key", "--token-stdin", "--url", server.URL, "--probe", "--nfo=" + map[bool]string{true: "true", false: "false"}[enabled]}
		var out, diagnostics bytes.Buffer
		status := runJobsCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
		server.Close()
		if status != 0 || calls != 1 || diagnostics.Len() != 0 {
			t.Fatal("scan NFO opt-in failed")
		}
	}
	for _, command := range []string{"probe-rebuild-library", "probe-rebuild-item", "retry", "get"} {
		var out, diagnostics bytes.Buffer
		if status := runJobsCLI(context.Background(), []string{command, "--id", jobsTestID, "--key", "k", "--nfo", "--token-stdin"}, unreadProbeToken{t}, &out, &diagnostics); status != 2 {
			t.Fatal("NFO flag accepted outside scan admission")
		}
	}
}

type nfoStartedReader struct {
	*io.PipeReader
	started chan struct{}
	once    sync.Once
}

func (r *nfoStartedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.PipeReader.Read(p)
}

type nfoStartedWriter struct {
	*io.PipeWriter
	started chan struct{}
	once    sync.Once
}

func (w *nfoStartedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.PipeWriter.Write(p)
}

func TestNFOCLIBlockedTokenAndOutputCancellationJoin(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		read, write := io.Pipe()
		defer read.Close()
		defer write.Close()
		input := &nfoStartedReader{PipeReader: read, started: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out, diagnostics bytes.Buffer
		done := make(chan int, 1)
		go func() { done <- runNFOCLI(ctx, nfoCLIArgs("job", "http://127.0.0.1:8097"), input, &out, &diagnostics) }()
		select {
		case <-input.started:
		case <-time.After(2 * time.Second):
			t.Fatal("token read never began")
		}
		cancel()
		select {
		case code := <-done:
			if code != 130 || out.Len() != 0 || diagnostics.String() != "nfo_cancelled\n" {
				t.Fatal("blocked credential read lost cancellation")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("blocked credential reader was not joined")
		}
	})
	t.Run("stdout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nfoCLIData("job")})
		}))
		defer server.Close()
		read, write := io.Pipe()
		defer read.Close()
		defer write.Close()
		output := &nfoStartedWriter{PipeWriter: write, started: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var diagnostics bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- runNFOCLIWithOutputCancellation(ctx, nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), output, &diagnostics)
		}()
		select {
		case <-output.started:
		case <-time.After(2 * time.Second):
			t.Fatal("output write never began")
		}
		cancel()
		select {
		case code := <-done:
			if code != 130 || diagnostics.String() != "nfo_cancelled\n" {
				t.Fatal("blocked output lost cancellation")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("blocked output writer was not joined")
		}
	})
}

func TestNFOCLIHTTPDeadlineCancelsAndJoins(t *testing.T) {
	started := make(chan struct{})
	joined := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(joined)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var out, diagnostics bytes.Buffer
	exit := runNFOCLI(ctx, nfoCLIArgs("job", server.URL), strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
	select {
	case <-started:
	default:
		t.Fatal("deadline test did not reach the service")
	}
	if exit != 124 || out.Len() != 0 || diagnostics.String() != "nfo_timeout\n" {
		t.Fatal("HTTP deadline was not classified safely")
	}
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP cancellation did not release handler")
	}
}

func TestNFOCLIRetainedIssuesAndParseFailureProjection(t *testing.T) {
	request := nfoCLIContract("issues")
	request.limit = domain.NFOIssuesPageMax
	data := nfoCLIData("issues")
	issues := make([]any, domain.NFOIssuesPageMax)
	for i := range issues {
		issues[i] = map[string]any{"severity": "warning", "code": "nfo_title_missing", "field": "title", "entry": 0}
	}
	data["issueCount"], data["issuesTruncated"], data["issues"], data["nextOffset"] = 65, true, issues, 32
	if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), request); !valid {
		t.Fatal("first page of retained prefix rejected")
	}
	data["offset"], request.offset = 32, 32
	delete(data, "nextOffset")
	if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), request); !valid {
		t.Fatal("64 retained issues did not terminate despite larger total")
	}
	for _, code := range []domain.NFOFailureCode{domain.NFOFailureInvalidXML, domain.NFOFailureUnsafeXML, domain.NFOFailureInvalidEncoding, domain.NFOFailureUnsupportedEncoding, domain.NFOFailureTooComplex} {
		data := nfoCLIData("issues")
		data["entries"], data["failureCode"], data["issueCount"], data["issues"] = 0, code, 0, []any{}
		if _, valid := decodeCLINFOResponse(jobsCLIJSON(t, data), nfoCLIContract("issues")); !valid {
			t.Fatal("safe negative observation reason was lost")
		}
	}
}
