package tracing

import (
	"strings"
	"testing"
	"time"
)

func TestOTLPSettingsValidation(t *testing.T) {
	ok := []OTLPSettings{
		{Endpoint: "https://collector.example:4318/v1/traces"},
		{Endpoint: "https://collector.example", Headers: map[string]string{"Authorization": "Bearer t", "X-Scope-OrgID": "jelee"}},
		{Endpoint: "http://127.0.0.1:4318", Insecure: true},
		{Endpoint: "http://[::1]:4318/v1/traces", Insecure: true},
		{Endpoint: "https://c.example", Timeout: OTLPMaxTimeout, BatchSize: OTLPMaxBatchSize, Interval: OTLPMinInterval, QueueSize: OTLPMaxQueueSize},
		{Endpoint: "https://c.example", Timeout: OTLPMinTimeout, BatchSize: 1, Interval: OTLPMaxInterval, QueueSize: 1},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Fatalf("%+v refused: %v", s, err)
		}
	}
	bad := map[string]OTLPSettings{
		"empty":             {},
		"not a url":         {Endpoint: "https://[::1"},
		"no host":           {Endpoint: "https:///v1/traces"},
		"credentials":       {Endpoint: "https://user:secret@c.example"},
		"query":             {Endpoint: "https://c.example/?k=secret"},
		"empty query":       {Endpoint: "https://c.example/?"},
		"fragment":          {Endpoint: "https://c.example/#secret"},
		"opaque":            {Endpoint: "https:c.example"},
		"scheme":            {Endpoint: "ftp://c.example"},
		"https insecure":    {Endpoint: "https://c.example", Insecure: true},
		"http remote":       {Endpoint: "http://192.0.2.1:4318", Insecure: true},
		"http name":         {Endpoint: "http://localhost:4318", Insecure: true},
		"http not insecure": {Endpoint: "http://127.0.0.1:4318"},
		"header token":      {Endpoint: "https://c.example", Headers: map[string]string{"X(secret)": "v"}},
		"header long name":  {Endpoint: "https://c.example", Headers: map[string]string{strings.Repeat("x", 129): "v"}},
		"header empty name": {Endpoint: "https://c.example", Headers: map[string]string{"": "v"}},
		"header value":      {Endpoint: "https://c.example", Headers: map[string]string{"X-A": "secret\n"}},
		"header nul":        {Endpoint: "https://c.example", Headers: map[string]string{"X-A": "secret\x00"}},
		"header empty":      {Endpoint: "https://c.example", Headers: map[string]string{"X-A": ""}},
		"header reserved":   {Endpoint: "https://c.example", Headers: map[string]string{"content-encoding": "identity"}},
		"header total":      {Endpoint: "https://c.example", Headers: map[string]string{"X-A": strings.Repeat("s", OTLPMaxHeaderBytes)}},
		"timeout low":       {Endpoint: "https://c.example", Timeout: time.Millisecond},
		"timeout high":      {Endpoint: "https://c.example", Timeout: 2 * OTLPMaxTimeout},
		"batch":             {Endpoint: "https://c.example", BatchSize: -1},
		"interval low":      {Endpoint: "https://c.example", Interval: time.Millisecond},
		"interval high":     {Endpoint: "https://c.example", Interval: 2 * OTLPMaxInterval},
		"queue":             {Endpoint: "https://c.example", QueueSize: OTLPMaxQueueSize + 1},
	}
	for name, s := range bad {
		err := s.Validate()
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example") {
			t.Fatalf("%s: error echoes the setting: %v", name, err)
		}
	}
	many := map[string]string{}
	for i := 0; i <= OTLPMaxHeaders; i++ {
		many["X-"+strings.Repeat("a", i+1)] = "v"
	}
	if ValidateOTLPHeaders(many) == nil {
		t.Fatal("too many headers accepted")
	}
}
