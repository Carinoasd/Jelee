package access

import (
	"context"
	"testing"
)

func TestPrincipalMustBeCompleteAndServerAssigned(t *testing.T) {
	for _, principal := range []Principal{{}, {UserID: "user"}, {SessionID: "session"}} {
		if _, ok := PrincipalFromContext(WithPrincipal(context.Background(), principal)); ok {
			t.Fatal("incomplete identity was accepted")
		}
	}
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("anonymous context was accepted")
	}
	want := Principal{UserID: "user", SessionID: "session", Kind: ClientNative, Admin: true}
	ctx := WithPrincipal(context.Background(), want)
	got, ok := PrincipalFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("principal = %#v, %v", got, ok)
	}
	got.Kind = ClientWeb
	if original, _ := PrincipalFromContext(ctx); original != want {
		t.Fatal("value read mutated context principal")
	}
}
