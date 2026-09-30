package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type strictJSONFixture struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nested   *struct {
		Enabled bool `json:"enabled"`
	} `json:"nested"`
	List []struct {
		Name string `json:"name"`
	} `json:"list"`
}

func TestDecodeJSONValidObjects(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON; charset=UTF-8"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(` {"username":"alice","password":"value","nested":{"enabled":true},"list":[{"name":"one"}]} `))
		r.Header.Set("Content-Type", contentType)
		var target strictJSONFixture
		if err := DecodeJSON(httptest.NewRecorder(), r, &target, 1024); err != nil {
			t.Fatalf("%s: %v", contentType, err)
		}
		if target.Username != "alice" || target.Password != "value" || target.Nested == nil || !target.Nested.Enabled || len(target.List) != 1 {
			t.Fatal("valid body was not decoded")
		}
	}
}

func TestDecodeJSONRejectsAmbiguousOrMalformedBodies(t *testing.T) {
	for _, body := range []string{
		``, ` `, `null`, `[]`, `[{}]`, `true`, `"string"`, `123`,
		`{"username":null}`, `{"nested":{"enabled":null}}`, `{"list":[null]}`,
		`{} {}`, `{}null`, `{}x`, `{} /* comment */`, `{"username":}`, `{"username":"x",}`,
		`{"unknown":"private-value"}`, `{"nested":{"unknown":true}}`,
		`{"username":"first","username":"second"}`,
		`{"username":"first","\u0075sername":"second"}`,
		`{"username":"first","USERNAME":"second"}`,
		`{"password":"first","paſſword":"second"}`,
		`{"nested":{"enabled":true,"enabled":false}}`,
		`{"list":[{"name":"one","name":"two"}]}`,
		`{"list":[{"name":"one","NAME":"two"}]}`,
		`{"username":false}`, `{"password":1e10000000}`,
		"{\"username\":\"\xff\"}",
		`{"list":` + strings.Repeat(`[`, 100) + `0` + strings.Repeat(`]`, 100) + `}`,
	} {
		t.Run(body, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			var target strictJSONFixture
			if err := DecodeJSON(httptest.NewRecorder(), r, &target, 4096); !errors.Is(err, domain.ErrInvalid) || err.Error() != domain.ErrInvalid.Error() {
				t.Fatalf("malformed/ambiguous input returned %v", err)
			}
		})
	}
}

func TestDecodeJSONContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json, text/plain", "application/json; charset=latin1", "application/json; charset=utf-8; charset=latin1", "application/json; bad"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		var target strictJSONFixture
		if err := DecodeJSON(httptest.NewRecorder(), r, &target, 100); !errors.Is(err, media.ErrUnsupportedMediaType) {
			t.Fatalf("content-type %q returned %v", contentType, err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add("Content-Type", "application/json")
	var target strictJSONFixture
	if err := DecodeJSON(httptest.NewRecorder(), r, &target, 100); !errors.Is(err, media.ErrUnsupportedMediaType) {
		t.Fatal("duplicate Content-Type headers accepted")
	}
}

type strictCountingBody struct {
	read int
}

func (b *strictCountingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	b.read += len(p)
	return len(p), nil
}
func (*strictCountingBody) Close() error { return nil }

func TestDecodeJSONBodyBoundIncludesWhitespaceAndUnknownLength(t *testing.T) {
	for _, length := range []int64{-1, 4096} {
		body := &strictCountingBody{}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Body, r.ContentLength = body, length
		r.Header.Set("Content-Type", "application/json")
		var target strictJSONFixture
		if err := DecodeJSON(httptest.NewRecorder(), r, &target, 128); !errors.Is(err, media.ErrBodyTooLarge) {
			t.Fatalf("unbounded body returned %v", err)
		}
		if body.read > 129 {
			t.Fatalf("read beyond bound: %d", body.read)
		}
	}
	for _, tc := range []struct {
		body string
		want error
	}{{"{}", nil}, {"{} ", media.ErrBodyTooLarge}} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		r.ContentLength = -1
		r.Header.Set("Content-Type", "application/json")
		var target strictJSONFixture
		if err := DecodeJSON(httptest.NewRecorder(), r, &target, 2); !errors.Is(err, tc.want) {
			t.Fatalf("body %q: %v", tc.body, err)
		}
	}
}

type strictFailedReader struct{}

func (strictFailedReader) Read([]byte) (int, error) { return 0, errors.New("private-body-path") }

func TestDecodeJSONCancellationAndReadFailure(t *testing.T) {
	body := &strictCountingBody{}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = body
	r.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	var target strictJSONFixture
	if err := DecodeJSON(httptest.NewRecorder(), r.WithContext(ctx), &target, 100); !errors.Is(err, context.Canceled) || body.read != 0 {
		t.Fatal("cancelled request read its body")
	}
	r.Body = io.NopCloser(strictFailedReader{})
	if err := DecodeJSON(httptest.NewRecorder(), r, &target, 100); err != domain.ErrInvalid {
		t.Fatalf("read failure exposed private details: %v", err)
	}
}

func FuzzDecodeJSON(f *testing.F) {
	for _, body := range []string{`{}`, `{"username":"x"}`, `{"username":1,"username":2}`, `{"nested":{"enabled":false}}`, `null`, `{"list":[{"name":"x"}]}`} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var target strictJSONFixture
		err := DecodeJSON(httptest.NewRecorder(), r, &target, 4096)
		if err != nil && err != domain.ErrInvalid && err != media.ErrBodyTooLarge {
			t.Fatalf("unexpected or nonsanitized error: %v", err)
		}
	})
}
