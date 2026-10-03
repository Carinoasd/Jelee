package compat

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	tokA = strings.Repeat("A", 43)
	tokB = strings.Repeat("b", 42) + "w" // last char carries only zero padding bits
	tokC = "0123456789-_abcdefghijklmnopqrstuvwxyzABCDE"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func primary(params string) string { return schemePrimary + " " + params }

func TestValidToken(t *testing.T) {
	for _, tok := range []string{tokA, tokB, tokC} {
		if !ValidToken(tok) {
			t.Fatalf("expected valid %q", tok)
		}
	}
	for _, tok := range []string{
		"", "short", strings.Repeat("A", 42), strings.Repeat("A", 44),
		strings.Repeat("A", 42) + "B", // non-zero padding bits: non-canonical
		strings.Repeat("A", 42) + "=", strings.Repeat("A", 42) + "+", strings.Repeat("A", 42) + "/",
		strings.Repeat("A", 42) + " ", " " + strings.Repeat("A", 42), strings.Repeat("A", 42) + "\x00",
		strings.Repeat("A", 41) + "é",      // 43 bytes, not base64url
		"0123456789abcdef0123456789abcdef", // 32-hex legacy API key form
	} {
		if ValidToken(tok) {
			t.Fatalf("expected invalid %q", tok)
		}
	}
}

func TestParseClientAuth(t *testing.T) {
	full := primary(`Client="Some Client", Device="Living%20Room+TV", DeviceId="abc%3D%3D", Version="1.2.3", Token="` + tokA + `"`)
	tests := []struct {
		name  string
		h     http.Header
		q     url.Values
		want  ClientAuth
		err   error
		strip bool // legacy disabled
	}{
		{name: "empty", h: http.Header{}, want: ClientAuth{}},
		{name: "full quoted header", h: hdr("Authorization", full),
			want: ClientAuth{Token: tokA, Client: "Some Client", Device: "Living Room TV", DeviceID: "abc==", Version: "1.2.3", Source: TokenSourceAuthorization}},
		{name: "scheme case-insensitive", h: hdr("Authorization", strings.ToLower(schemePrimary)+` Token="`+tokA+`"`),
			want: ClientAuth{Token: tokA, Source: TokenSourceAuthorization}},
		{name: "legacy scheme", h: hdr("Authorization", strings.ToUpper(schemeLegacy)+` Token="`+tokA+`"`),
			want: ClientAuth{Token: tokA, Source: TokenSourceAuthorization}},
		{name: "legacy scheme disabled", strip: true, h: hdr("Authorization", schemeLegacy+` Token="`+tokA+`"`), want: ClientAuth{}},
		{name: "unquoted values no spaces", h: hdr("Authorization", primary("Client=c,Device=d,DeviceId=i,Version=v,Token="+tokB)),
			want: ClientAuth{Token: tokB, Client: "c", Device: "d", DeviceID: "i", Version: "v", Source: TokenSourceAuthorization}},
		{name: "comma inside quotes", h: hdr("Authorization", primary(`Device="a,b", Client="x"`)),
			want: ClientAuth{Device: "a,b", Client: "x"}},
		{name: "equals inside quotes", h: hdr("Authorization", primary(`DeviceId="YQ==", Client="x"`)),
			want: ClientAuth{DeviceID: "YQ==", Client: "x"}},
		{name: "unquoted equals: last key wins like upstream", h: hdr("Authorization", primary("Token=abc=def")),
			want: ClientAuth{}},
		{name: "url decoding plus and percent", h: hdr("Authorization", primary(`Device="H%C3%B6rb%C3%BCcher+1"`)),
			want: ClientAuth{Device: "Hörbücher 1"}},
		{name: "raw utf8 kept", h: hdr("Authorization", primary(`Device=Hörbücher`)),
			want: ClientAuth{Device: "Hörbücher"}},
		{name: "invalid percent kept literally", h: hdr("Authorization", primary(`Device=%22%Hörbücher`)),
			want: ClientAuth{Device: `"%Hörbücher`}},
		{name: "trailing percent", h: hdr("Authorization", primary(`Device=ab%2`)),
			want: ClientAuth{Device: "ab%2"}},
		{name: "duplicate key last wins", h: hdr("Authorization", primary(`Client="one", Client="two"`)),
			want: ClientAuth{Client: "two"}},
		{name: "keys are case-sensitive", h: hdr("Authorization", primary(`client="x", token="`+tokA+`"`)),
			want: ClientAuth{}},
		{name: "empty token param falls through", h: hdr("Authorization", primary(`Token="", Client="c"`), headerLegacyToken, tokB),
			want: ClientAuth{Token: tokB, Client: "c", Source: TokenSourceLegacyHeader}},
		{name: "no space after scheme", h: hdr("Authorization", schemePrimary), want: ClientAuth{}},
		{name: "unknown scheme ignored", h: hdr("Authorization", "Basic "+tokA), want: ClientAuth{}},
		{name: "bearer ignored", h: hdr("Authorization", "Bearer "+tokA), want: ClientAuth{}},

		// Header source priority.
		{name: "legacy authorization header used when authorization absent", h: hdr(headerLegacyAuthorization, primary(`Client="c", Token="`+tokA+`"`)),
			want: ClientAuth{Token: tokA, Client: "c", Source: TokenSourceAuthorization}},
		{name: "legacy authorization header disabled", strip: true, h: hdr(headerLegacyAuthorization, primary(`Token="`+tokA+`"`)), want: ClientAuth{}},
		{name: "authorization shadows legacy authorization", h: hdr("Authorization", primary(`Client="main"`), headerLegacyAuthorization, primary(`Client="legacy", Token="`+tokA+`"`)),
			want: ClientAuth{Client: "main"}},
		{name: "non-matching authorization still shadows legacy authorization", h: hdr("Authorization", "Bearer x", headerLegacyAuthorization, primary(`Token="`+tokA+`"`)),
			want: ClientAuth{}},
		{name: "empty authorization falls back", h: hdr("Authorization", "", headerLegacyAuthorization, primary(`Token="`+tokA+`"`)),
			want: ClientAuth{Token: tokA, Source: TokenSourceAuthorization}},

		// Token source priority.
		{name: "param beats legacy header", h: hdr("Authorization", primary(`Token="`+tokA+`"`), headerLegacyToken, tokB),
			q: url.Values{"ApiKey": {tokC}}, want: ClientAuth{Token: tokA, Source: TokenSourceAuthorization}},
		{name: "legacy header", h: hdr(headerLegacyToken, tokA), want: ClientAuth{Token: tokA, Source: TokenSourceLegacyHeader}},
		{name: "legacy header first beats alt", h: hdr(headerLegacyToken, tokA, headerLegacyTokenAlt, tokB), want: ClientAuth{Token: tokA, Source: TokenSourceLegacyHeader}},
		{name: "alt legacy header", h: hdr(headerLegacyTokenAlt, tokB), want: ClientAuth{Token: tokB, Source: TokenSourceLegacyHeader}},
		{name: "legacy headers beat query", h: hdr(headerLegacyTokenAlt, tokB), q: url.Values{"ApiKey": {tokA}},
			want: ClientAuth{Token: tokB, Source: TokenSourceLegacyHeader}},
		{name: "legacy headers disabled", strip: true, h: hdr(headerLegacyToken, tokB), q: url.Values{"ApiKey": {tokA}},
			want: ClientAuth{Token: tokA, Source: TokenSourceQueryAPIKey}},
		{name: "ApiKey beats api_key", q: url.Values{"ApiKey": {tokA}, "api_key": {tokB}}, want: ClientAuth{Token: tokA, Source: TokenSourceQueryAPIKey}},
		{name: "query key case-insensitive", q: url.Values{"APIKEY": {tokA}}, want: ClientAuth{Token: tokA, Source: TokenSourceQueryAPIKey}},
		{name: "api_key", q: url.Values{"Api_Key": {tokB}}, want: ClientAuth{Token: tokB, Source: TokenSourceQueryLegacyAPIKey}},
		{name: "api_key disabled", strip: true, q: url.Values{"api_key": {tokB}}, want: ClientAuth{}},
		{name: "empty ApiKey falls through", q: url.Values{"ApiKey": {""}, "api_key": {tokB}}, want: ClientAuth{Token: tokB, Source: TokenSourceQueryLegacyAPIKey}},

		// Invalid tokens: the first non-empty source decides; no fall-through.
		{name: "invalid param token", h: hdr("Authorization", primary(`Client="c", Token="nope"`), headerLegacyToken, tokA),
			want: ClientAuth{Client: "c"}, err: ErrInvalidToken},
		{name: "hex api key form rejected", q: url.Values{"api_key": {"0123456789abcdef0123456789abcdef"}}, err: ErrInvalidToken},
		{name: "padded token rejected", h: hdr(headerLegacyToken, tokA+"="), err: ErrInvalidToken},
		{name: "whitespace token rejected", h: hdr(headerLegacyToken, " "+tokA), err: ErrInvalidToken},
		{name: "space-only token selected and rejected", h: hdr(headerLegacyToken, " "), q: url.Values{"ApiKey": {tokA}}, err: ErrInvalidToken},
		{name: "duplicate legacy header", h: hdr(headerLegacyToken, tokA, headerLegacyToken, tokA), err: ErrInvalidToken},
		{name: "duplicate query value", q: url.Values{"api_key": {tokA, tokA}}, err: ErrInvalidToken},
		{name: "duplicate query key by case", q: url.Values{"ApiKey": {tokA}, "apikey": {tokB}}, err: ErrInvalidToken},
		{name: "url-encoded token in param decoded then checked", h: hdr("Authorization", primary(`Token="`+tokA[:42]+`%41"`)),
			want: ClientAuth{Token: tokA, Source: TokenSourceAuthorization}},
		{name: "percent-encoded junk token", h: hdr("Authorization", primary(`Token="`+tokA[:40]+`%2B%2F"`)), err: ErrInvalidToken},

		// Malformed input.
		{name: "two authorization lines", h: hdr("Authorization", primary(`Token="`+tokA+`"`), "Authorization", primary(`Token="`+tokB+`"`)), err: ErrMalformedAuth},
		{name: "oversized header", h: hdr("Authorization", primary(`Client="`+strings.Repeat("x", MaxAuthHeaderBytes)+`"`)), err: ErrMalformedAuth},
		{name: "oversized field", h: hdr("Authorization", primary(`Client="`+strings.Repeat("x", MaxAuthFieldBytes+1)+`"`)), err: ErrMalformedAuth},
		{name: "field at limit", h: hdr("Authorization", primary(`Client="`+strings.Repeat("x", MaxAuthFieldBytes)+`"`)),
			want: ClientAuth{Client: strings.Repeat("x", MaxAuthFieldBytes)}},
		{name: "raw control char", h: hdr("Authorization", primary("Client=a\x01b")), err: ErrMalformedAuth},
		{name: "raw DEL", h: hdr("Authorization", primary("Client=a\x7fb")), err: ErrMalformedAuth},
		{name: "encoded newline", h: hdr("Authorization", primary(`Device="a%0D%0Ab"`)), err: ErrMalformedAuth},
		{name: "encoded NUL", h: hdr("Authorization", primary(`Device="a%00b"`)), err: ErrMalformedAuth},
		{name: "encoded tab", h: hdr("Authorization", primary(`Device="a%09b"`)), err: ErrMalformedAuth},
		{name: "encoded C1 control", h: hdr("Authorization", primary(`Device="a%C2%85b"`)), err: ErrMalformedAuth},
		{name: "encoded invalid utf8", h: hdr("Authorization", primary(`Device="a%FFb"`)), err: ErrMalformedAuth},
		{name: "raw invalid utf8", h: hdr("Authorization", primary("Device=a\xffb")), err: ErrMalformedAuth},
		{name: "too many params", h: hdr("Authorization", primary(manyParams(MaxAuthParams+1))), err: ErrMalformedAuth},
		{name: "max params", h: hdr("Authorization", primary(manyParams(MaxAuthParams))), want: ClientAuth{}},
		{name: "raw tab allowed as key whitespace", h: hdr("Authorization", primary("\tClient=c")), want: ClientAuth{Client: "c"}},
		{name: "unterminated quote", h: hdr("Authorization", primary(`Client="abc, Device=d`)), want: ClientAuth{Client: "abc, Device=d"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClientAuthOptions(tc.h, tc.q, AuthOptions{Legacy: !tc.strip})
			if !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err != nil && got.Token != "" {
				t.Fatalf("token returned with error: %v", got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func manyParams(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "k" + strings.Repeat("x", i) + "=v"
	}
	return strings.Join(parts, ",")
}

func TestParseClientAuthDefaultsToLegacy(t *testing.T) {
	got, err := ParseClientAuth(hdr(headerLegacyAuthorization, schemeLegacy+` Token="`+tokA+`"`), nil)
	if err != nil || got.Token != tokA {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestErrorsMapToDomain(t *testing.T) {
	if !errors.Is(ErrInvalidToken, domain.ErrUnauthenticated) || !errors.Is(ErrMalformedAuth, domain.ErrInvalid) {
		t.Fatal("compat errors must wrap domain errors")
	}
}

func TestClientAuthFormattingHidesToken(t *testing.T) {
	a := ClientAuth{Token: tokA, Client: "c"}
	for _, s := range []string{a.String(), a.GoString()} {
		if strings.Contains(s, tokA) {
			t.Fatalf("token leaked: %s", s)
		}
	}
}

func TestSplitParamsUpstreamVectors(t *testing.T) {
	// Vectors from the upstream part splitter test data.
	for in, want := range map[string]map[string]string{
		`x="123,123",y="123"`:                   {"x": "123,123", "y": "123"},
		`x="123,123",         y="123",z="'hi'"`: {"x": "123,123", "y": "123", "z": "'hi'"},
		`x="ab"`:                                {"x": "ab"},
		`param=Hörbücher`:                       {"param": "Hörbücher"},
		`param=%22%Hörbücher`:                   {"param": `"%Hörbücher`},
	} {
		got, err := splitParams(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
}

func TestRedactQuery(t *testing.T) {
	in := url.Values{
		"api_key": {tokA}, "ApiKey": {tokB}, "API_KEY": {tokC}, "access_token": {"x"}, "Token": {"y"},
		"password": {"p"}, "Pw": {"q"}, "UserId": {"u"}, "Fields": {"a", "b"},
	}
	before := in.Encode()
	got := RedactQuery(in)
	want := url.Values{"UserId": {"u"}, "Fields": {"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if in.Encode() != before {
		t.Fatal("input mutated")
	}
	got["Fields"][0] = "changed"
	if in["Fields"][0] != "a" {
		t.Fatal("output aliases input")
	}
	if RedactQuery(nil) != nil {
		t.Fatal("nil input")
	}
	for _, s := range []string{tokA, tokB, tokC} {
		if strings.Contains(got.Encode(), s) {
			t.Fatal("token leaked")
		}
	}
}

func FuzzParseClientAuth(f *testing.F) {
	f.Add(primary(`Client="c", Device="d", DeviceId="i", Version="v", Token="`+tokA+`"`), "", "api_key="+tokB)
	f.Add(primary(`x="123,123",         y="123"`), tokA, "")
	f.Add(schemeLegacy+` Device=%22%Hö`, "", "ApiKey=%00")
	f.Add(`"`, "\x00", "api_key=a&api_key=b")
	f.Fuzz(func(t *testing.T, authorization, legacyToken, rawQuery string) {
		h := http.Header{}
		h.Set("Authorization", authorization)
		h.Set(headerLegacyToken, legacyToken)
		q, _ := url.ParseQuery(rawQuery)
		got, err := ParseClientAuth(h, q)
		if err != nil {
			if got.Token != "" {
				t.Fatal("token returned with error")
			}
			if !errors.Is(err, ErrInvalidToken) && !errors.Is(err, ErrMalformedAuth) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if got.Token != "" && !ValidToken(got.Token) {
			t.Fatalf("invalid token accepted: %q", got.Token)
		}
		for _, v := range []string{got.Client, got.Device, got.DeviceID, got.Version} {
			if len(v) > MaxAuthFieldBytes || !utf8.ValidString(v) || hasControl(v) {
				t.Fatalf("unsafe field accepted: %q", v)
			}
		}
		for key := range RedactQuery(q) {
			if sensitiveQueryKey(key) {
				t.Fatalf("sensitive key kept: %q", key)
			}
		}
	})
}
