// Package metadata accesses provider APIs through the controlled transport.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

var (
	ErrCredentials = errors.New("tmdb_credentials_rejected")
	ErrRateLimited = errors.New("tmdb_rate_limited")
	ErrUnavailable = errors.New("tmdb_unavailable")
	ErrResponse    = errors.New("tmdb_response_invalid")
)

type TMDB struct {
	key      string
	client   *outbound.Client
	fetch    func(context.Context, string, int64) (outbound.Response, error)
	wait     func(context.Context, time.Duration) error
	now      func() time.Time
	governor *requestGovernor
	movies   movieCache
	series   seriesCache
}

func NewTMDB(key string) (*TMDB, error) {
	if !ValidTMDBKey(key) {
		return nil, ErrCredentials
	}
	c, err := outbound.New([]string{"api.themoviedb.org"})
	if err != nil {
		return nil, ErrUnavailable
	}
	return NewTMDBWithClient(key, c)
}

// NewTMDBWithClient supports composition with an owned, controlled client.
// A consumer cannot supply a raw transport or an arbitrary HTTP implementation.
func NewTMDBWithClient(key string, client *outbound.Client) (*TMDB, error) {
	if !ValidTMDBKey(key) || client == nil {
		return nil, ErrCredentials
	}
	return &TMDB{key: key, client: client, fetch: client.Fetch, wait: waitRetry, now: time.Now, governor: newRequestGovernor()}, nil
}

func ValidTMDBKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, ch := range key {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// ValidateCredentials calls the documented key validation endpoint. A 429
// blocks startup; it is not evidence of the account's remaining quota.
func (t *TMDB) ValidateCredentials(ctx context.Context) error {
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{"api_key": []string{t.key}}
	r, err := t.authenticationRequest(budget, "https://api.themoviedb.org/3/authentication?"+q.Encode())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return ErrUnavailable
	}
	switch r.Status {
	case 401, 403:
		return ErrCredentials
	case 429:
		return ErrRateLimited
	case 200:
	default:
		return ErrUnavailable
	}
	var data struct {
		Success    bool `json:"success"`
		StatusCode int  `json:"status_code"`
	}
	if json.Unmarshal(r.Body, &data) != nil || !data.Success || data.StatusCode != 1 {
		return ErrResponse
	}
	return nil
}

func (t *TMDB) Close() { t.client.CloseIdleConnections() }
