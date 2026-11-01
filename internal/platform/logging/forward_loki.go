package logging

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lokiSender pushes batches to the Loki HTTP API. TLS verification uses the
// system roots; there is no switch to turn it off.
type lokiSender struct {
	endpoint string
	service  string
	header   http.Header
	client   *http.Client
}

func newLokiSender(o LokiOptions, timeout time.Duration) (*lokiSender, error) {
	u, err := o.parseURL()
	if err != nil {
		return nil, err
	}
	s := &lokiSender{endpoint: u.String(), service: o.Service, header: http.Header{}}
	if s.service == "" {
		s.service = "jelee"
	}
	s.header.Set("Content-Type", "application/json")
	s.header.Set("User-Agent", "jelee-log-forwarder")
	switch {
	case o.BearerToken != "":
		s.header.Set("Authorization", "Bearer "+o.BearerToken)
	case o.Username != "":
		r := http.Request{Header: http.Header{}}
		r.SetBasicAuth(o.Username, o.Password)
		s.header.Set("Authorization", r.Header.Get("Authorization"))
	}
	if o.TenantID != "" {
		s.header.Set("X-Scope-OrgID", o.TenantID)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = timeout
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 2, 2
	s.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// A redirect would carry the credentials to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s, nil
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

// lokiLevel keeps the level label to the four slog names.
func lokiLevel(level string) string {
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return strings.ToLower(level)
	}
	return "info"
}

// lokiComponent keeps the component label bounded and identifier-like.
func lokiComponent(component string) string {
	if component != "" && safeToken(component, 32, "_") {
		return component
	}
	return "unknown"
}

// body groups the batch into one stream per component and level.
func (s *lokiSender) body(records [][]byte) ([]byte, error) {
	type key struct{ component, level string }
	index := map[key]int{}
	var push lokiPush
	type stamped struct {
		ns   int64
		line string
	}
	groups := map[key][]stamped{}
	for _, rec := range records {
		meta, at := parseRecordMeta(rec)
		k := key{lokiComponent(meta.Component), lokiLevel(meta.Level)}
		if _, ok := index[k]; !ok {
			index[k] = len(push.Streams)
			push.Streams = append(push.Streams, lokiStream{Stream: map[string]string{"service": s.service, "component": k.component, "level": k.level}})
		}
		groups[k] = append(groups[k], stamped{at.UnixNano(), string(rec)})
	}
	for k, i := range index {
		g := groups[k]
		// Loki rejects out of order entries within a stream.
		slices.SortStableFunc(g, func(a, b stamped) int {
			switch {
			case a.ns < b.ns:
				return -1
			case a.ns > b.ns:
				return 1
			}
			return 0
		})
		for _, e := range g {
			push.Streams[i].Values = append(push.Streams[i].Values, [2]string{strconv.FormatInt(e.ns, 10), e.line})
		}
	}
	return json.Marshal(push)
}

func (s *lokiSender) send(ctx context.Context, records [][]byte) error {
	payload, err := s.body(records)
	if err != nil {
		return &deliverError{class: "encode"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return &deliverError{class: "request"}
	}
	req.Header = s.header.Clone()
	resp, err := s.client.Do(req)
	if err != nil {
		return classifyNetError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusTooManyRequests || code == http.StatusRequestTimeout || code >= 500:
		return &deliverError{class: "status " + strconv.Itoa(code), retry: true, wait: retryAfter(resp.Header.Get("Retry-After"))}
	default:
		// 4xx (entry too old, bad credentials, line too long) will not
		// change on retry.
		return &deliverError{class: "rejected " + strconv.Itoa(code)}
	}
}

func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(min(n, int(forwardMaxRetryWait/time.Second))) * time.Second
}

func (s *lokiSender) close() { s.client.CloseIdleConnections() }
