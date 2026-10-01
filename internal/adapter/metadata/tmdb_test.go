package metadata

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

const testKey = "0123456789abcdef0123456789abcdef"

func TestTMDBCredentialResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"valid", 200, `{"success":true,"status_code":1,"status_message":"Success."}`, nil},
		{"unauthorized", 401, `{"secret":"do not print"}`, ErrCredentials},
		{"forbidden", 403, "", ErrCredentials}, {"throttled", 429, "", ErrRateLimited},
		{"server_error", 500, "", ErrUnavailable}, {"redirect", 302, "", ErrUnavailable},
		{"malformed", 200, "secret", ErrResponse}, {"false", 200, `{"success":false,"status_code":1}`, ErrResponse},
		{"wrong_code", 200, `{"success":true,"status_code":7}`, ErrResponse}, {"missing", 200, `{}`, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewTMDB(testKey)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			calls := 0
			client.fetch = func(ctx context.Context, raw string, maxBytes int64) (outbound.Response, error) {
				calls++
				u, err := url.Parse(raw)
				if err != nil || u.Scheme != "https" || u.Host != "api.themoviedb.org" || u.Path != "/3/authentication" || u.Query().Get("api_key") != testKey || maxBytes != 4096 {
					t.Fatal("credential request contract differs")
				}
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			err = client.ValidateCredentials(context.Background())
			if !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if err != nil && (strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "secret")) {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestTMDBMapsRawFetchErrors(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{errors.New("https://secret@10.0.0.1/?api_key=" + testKey), ErrUnavailable}, {outbound.ErrDenied, ErrUnavailable}, {outbound.ErrTooLarge, ErrUnavailable}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		client, _ := NewTMDB(testKey)
		client.fetch = func(context.Context, string, int64) (outbound.Response, error) { return outbound.Response{}, tc.source }
		if err := client.ValidateCredentials(context.Background()); !errors.Is(err, tc.want) {
			t.Fatalf("error=%v", err)
		}
		client.Close()
	}
	for _, key := range []string{"", "secret", strings.Repeat("g", 32), strings.Repeat("a", 33)} {
		if _, err := NewTMDB(key); err != ErrCredentials {
			t.Fatal("invalid credential accepted")
		}
	}
}

func TestTMDBProductionFetchRespectsCancellation(t *testing.T) {
	client, _ := NewTMDB(testKey)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.ValidateCredentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("production transport cancellation=%v", err)
	}
}
