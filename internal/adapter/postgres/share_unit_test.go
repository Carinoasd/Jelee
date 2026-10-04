package postgres

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestRequestScopeArg(t *testing.T) {
	if requestScopeArg(context.Background()) != nil {
		t.Fatal("scope outside a request")
	}
	p := access.Principal{UserID: "u", SessionID: "s"}
	if requestScopeArg(access.WithPrincipal(context.Background(), p)) != nil {
		t.Fatal("scope without a request")
	}
	lib := "11111111-1111-4111-8111-111111111111"
	p.Request = &access.RequestScope{IP: netip.MustParseAddr("::ffff:192.168.1.4"), Kind: access.ClientWeb, Libraries: []string{lib, "not-an-id", lib}}
	raw, _ := requestScopeArg(access.WithPrincipal(context.Background(), p)).(string)
	var scope struct {
		IP        string   `json:"ip"`
		LAN       *bool    `json:"lan"`
		Kind      string   `json:"kind"`
		Libraries []string `json:"libraries"`
	}
	if err := json.Unmarshal([]byte(raw), &scope); err != nil || scope.IP != "192.168.1.4" || scope.LAN == nil || !*scope.LAN || scope.Kind != "web" || !slices.Equal(scope.Libraries, []string{lib}) {
		t.Fatalf("scope %s %v", raw, err)
	}
	// Without an address the network conditions stay unknown, and an empty
	// library set stays a restriction.
	p.Request = &access.RequestScope{Kind: access.ClientNative, Libraries: []string{}}
	raw, _ = requestScopeArg(access.WithPrincipal(context.Background(), p)).(string)
	if raw != `{"kind":"native","libraries":[]}` {
		t.Fatalf("scope without address %s", raw)
	}
}

func TestNormalizeNetworkRule(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	in, ok := normalizeNetworkRule(domain.NetworkRuleInput{LibraryID: id, Network: "any", CIDRs: []string{"10.1.2.3", "10.1.2.3/32", "::ffff:10.0.0.0/104", "2001:db8::7/32"}, ClientKinds: []string{"web", "native"}})
	if !ok || !slices.Equal(in.CIDRs, []string{"10.0.0.0/8", "10.1.2.3/32", "2001:db8::/32"}) || !slices.Equal(in.ClientKinds, []string{"native", "web"}) {
		t.Fatalf("normalized %+v %t", in, ok)
	}
	for _, bad := range []string{"10.0.0.0/33", "::ffff:10.0.0.0/90", "fe80::1%eth0", "fe80::/10%eth0", "host"} {
		if _, ok := normalizeNetworkRule(domain.NetworkRuleInput{LibraryID: id, Network: "any", CIDRs: []string{bad}}); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, ok := normalizeNetworkRule(domain.NetworkRuleInput{LibraryID: id, Network: "office"}); ok {
		t.Fatal("invalid network accepted")
	}
	if !sameNetworkRule(in, in) || sameNetworkRule(in, domain.NetworkRuleInput{LibraryID: id}) {
		t.Fatal("rule comparison")
	}
}

func TestShareTokenHash(t *testing.T) {
	token := strings.Repeat("A", 43)
	hash, ok := shareTokenHash(token)
	if !ok || len(hash) != 32 {
		t.Fatal("token digest")
	}
	// A share token never digests to a session token's digest.
	if again, _ := shareTokenHash(token); string(again) != string(hash) {
		t.Fatal("digest not stable")
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 42) + "!", strings.Repeat("A", 44)} {
		if _, ok := shareTokenHash(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if shareAudit(domain.Share{Note: "n"})["note"] != "n" || networkRuleAudit(domain.NetworkRuleInput{Network: "lan"})["network"] != "lan" {
		t.Fatal("audit forms")
	}
}
