package domain

import (
	"strings"
	"testing"
)

func TestValidItemImageRemoteURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://image.example.com/t/p/original/a.jpg", true},
		{"https://example.com", true},
		{"https://example.com:8443/a%20b.jpg?x=1#f", true},
		{"https://[2001:db8::1]/a.jpg", true},
		{"https://[2001:db8::1]:443/a.jpg", true},
		{"https://xn--fsq.example/a.jpg", true},
		{"http://example.com/a.jpg", false},
		{"https://", false},
		{"https:///a.jpg", false},
		{"https://user@example.com/a.jpg", false},
		{"https://user:pw@example.com/a.jpg", false},
		{"https://example.com:/a.jpg", false},
		{"https://example.com:99999x/a.jpg", false},
		{"https://exa mple.com/a.jpg", false},
		{"https://example.com/a\x01.jpg", false},
		{"https://example.com/%zz.jpg", false},
		{"https://example.com/a%2", false},
		{"https://ex_ample.com/a.jpg", false},
		{"https://[]/a.jpg", false},
		{"https://[2001:db8::1/a.jpg", false},
		{"https://example.com/" + strings.Repeat("a", ItemImageMaxURLBytes), false},
	} {
		if got := ValidItemImageRemoteURL(tc.url); got != tc.want {
			t.Errorf("ValidItemImageRemoteURL(%q)=%v want %v", tc.url, got, tc.want)
		}
	}
}
