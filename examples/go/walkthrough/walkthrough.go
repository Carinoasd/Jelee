// Package walkthrough is the Jelee API example of examples/go: sign in, list
// the libraries the account may see, list items of the first library, read
// the details of the first item and sign out again. It uses only the
// standard library and the public /api/v1 contract (api/openapi.json), so it
// doubles as a template for other clients. CI runs it against a real server
// (internal/adapter/http TestGoExampleWalkthroughPostgres).
package walkthrough

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Config names the server and the account. Credentials come from the caller
// (the example program reads environment variables); none is built in.
type Config struct {
	// BaseURL is the server root, for example http://127.0.0.1:8097.
	BaseURL string
	// Name and Password are the account credentials.
	Name, Password string
	// Client is the HTTP client; nil means http.DefaultClient.
	Client *http.Client
	// ItemLimit bounds the item page; 0 means 20.
	ItemLimit int
}

// Library is one library the account may see.
type Library struct {
	ID   string
	Name string
}

// Item is one catalog item as listed by GET /api/v1/items.
type Item struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
}

// Details is the subset of GET /api/v1/items/{id}/details the example shows.
type Details struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	Overview string   `json:"overview"`
	Year     int      `json:"productionYear"`
	Genres   []string `json:"genres"`
}

// Result is what one walk saw.
type Result struct {
	UserID    string
	Admin     bool
	Libraries []Library
	Items     []Item
	// TotalItems is the number of visible items in the first library.
	TotalItems int
	// Details is nil when the first library has no items.
	Details *Details
}

// APIError is a Jelee error envelope ({"error":{"code",...}}).
type APIError struct {
	Status  int
	Code    string
	Message string
	TraceID string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("jelee: HTTP %d %s: %s (traceId %s)", e.Status, e.Code, e.Message, e.TraceID)
}

// ErrSecondFactor reports an account that needs a second login step, which
// this example does not implement (see POST /api/v1/auth/login/second-factor).
var ErrSecondFactor = errors.New("jelee: the account requires a second factor; this example covers password-only accounts")

// Run performs the walk and writes a short report to out (nil discards it).
// The session it creates is revoked before Run returns, also on failure.
func Run(ctx context.Context, cfg Config, out io.Writer) (result Result, err error) {
	if out == nil {
		out = io.Discard
	}
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return Result{}, fmt.Errorf("jelee: base URL must be an absolute http(s) URL")
	}
	if cfg.Name == "" || cfg.Password == "" {
		return Result{}, errors.New("jelee: name and password are required")
	}
	c := client{base: base.String(), http: cfg.Client}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	limit := cfg.ItemLimit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// 1. Sign in: POST /api/v1/auth/login returns a bearer token.
	var login struct {
		Data struct {
			Token                string `json:"token"`
			SecondFactorRequired bool   `json:"secondFactorRequired"`
			User                 struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Admin bool   `json:"admin"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"name": cfg.Name, "password": cfg.Password}, &login); err != nil {
		return Result{}, err
	}
	if login.Data.SecondFactorRequired {
		return Result{}, ErrSecondFactor
	}
	if login.Data.Token == "" {
		return Result{}, errors.New("jelee: login returned no token")
	}
	c.token = login.Data.Token
	defer func() {
		// 5. Sign out: revoke the session even when a step failed. A
		// web session authenticated by bearer needs no CSRF header.
		if logoutErr := c.do(context.WithoutCancel(ctx), http.MethodPost, "/api/v1/auth/logout", struct{}{}, nil); logoutErr != nil && err == nil {
			err = logoutErr
		}
	}()
	result.UserID, result.Admin = login.Data.User.ID, login.Data.User.Admin
	_, _ = fmt.Fprintf(out, "signed in as %s (administrator: %t)\n", login.Data.User.Name, result.Admin)

	// 2. Libraries: administrators list every library; other accounts
	// read their own library grants.
	if result.Admin {
		var page struct {
			Data struct {
				Libraries []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"libraries"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v1/libraries?limit=100", nil, &page); err != nil {
			return result, err
		}
		for _, library := range page.Data.Libraries {
			result.Libraries = append(result.Libraries, Library{ID: library.ID, Name: library.Name})
		}
	} else {
		var grants struct {
			Data []struct {
				LibraryID string `json:"libraryId"`
				Name      string `json:"name"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v1/users/"+url.PathEscape(result.UserID)+"/libraries", nil, &grants); err != nil {
			return result, err
		}
		for _, grant := range grants.Data {
			result.Libraries = append(result.Libraries, Library{ID: grant.LibraryID, Name: grant.Name})
		}
	}
	_, _ = fmt.Fprintf(out, "%d libraries\n", len(result.Libraries))
	for _, library := range result.Libraries {
		_, _ = fmt.Fprintf(out, "  %s  %s\n", library.ID, library.Name)
	}
	if len(result.Libraries) == 0 {
		return result, nil
	}

	// 3. Items of the first library: the offset form of GET /api/v1/items.
	query := url.Values{"libraryId": {result.Libraries[0].ID}, "limit": {fmt.Sprint(limit)}, "sort": {"name"}}
	var items struct {
		Data       []Item `json:"data"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/items?"+query.Encode(), nil, &items); err != nil {
		return result, err
	}
	result.Items, result.TotalItems = items.Data, items.Pagination.Total
	_, _ = fmt.Fprintf(out, "%d of %d items in %s\n", len(result.Items), result.TotalItems, result.Libraries[0].Name)
	for _, item := range result.Items {
		_, _ = fmt.Fprintf(out, "  %s  %-8s %s\n", item.ID, item.Kind, item.Title)
	}
	if len(result.Items) == 0 {
		return result, nil
	}

	// 4. Details of the first item.
	var details struct {
		Data Details `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/items/"+url.PathEscape(result.Items[0].ID)+"/details", nil, &details); err != nil {
		return result, err
	}
	result.Details = &details.Data
	_, _ = fmt.Fprintf(out, "details: %s (%s", details.Data.Title, details.Data.Kind)
	if details.Data.Year > 0 {
		_, _ = fmt.Fprintf(out, ", %d", details.Data.Year)
	}
	_, _ = fmt.Fprintln(out, ")")
	return result, nil
}

type client struct {
	base  string
	token string
	http  *http.Client
}

// maxResponse bounds every response body the example reads.
const maxResponse = 8 << 20

// do sends one JSON request and decodes a 2xx JSON answer into out (nil
// skips it). Any other status becomes an *APIError.
func (c client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				TraceID string `json:"traceId"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return &APIError{Status: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, TraceID: envelope.Error.TraceID}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(data, out)
}
