package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAccountStorageErrorsPreserveClassificationWithoutLeakingDetails(t *testing.T) {
	for _, tc := range []struct{ input, errorClass error }{
		{&pgconn.PgError{Code: "23505", Message: "private database detail"}, domain.ErrConflict},
		{&pgconn.PgError{Code: "23503", Detail: "private database detail"}, domain.ErrInvalid},
		{&pgconn.PgError{Code: "23514", Message: "private database detail"}, domain.ErrInvalid},
		{&pgconn.PgError{Code: "22P02", Message: "private database detail"}, domain.ErrInvalid},
		{&pgconn.PgError{Code: "XX000", Message: "private database detail"}, domain.ErrDatabase},
		{errors.New("private database detail"), domain.ErrDatabase},
		{pgx.ErrNoRows, domain.ErrNotFound}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		got := storageError(tc.input)
		if !errors.Is(got, tc.errorClass) || strings.Contains(got.Error(), "private") {
			t.Fatalf("unsafe or incorrect error classification: %v", got)
		}
		var detail *pgconn.PgError
		if errors.As(got, &detail) {
			t.Fatal("database error retained behind safe wrapper")
		}
	}
	if storageError(nil) != nil {
		t.Fatal("nil error changed")
	}
}

func TestCredentialStructsCannotAccidentallySerializePasswordHashes(t *testing.T) {
	for _, value := range []any{domain.Credentials{PasswordHash: accountTestHash, UserID: "private-id", Name: "private-name"}, domain.UserInput{PasswordHash: accountTestHash}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "argon2") || strings.Contains(string(raw), "private") {
			t.Fatal("internal credential values became JSON output")
		}
	}
}

func TestAccountValidationRejectsUnsafeAndUnboundedInputsBeforeDatabaseUse(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	for _, input := range []domain.UserInput{accountInput(" padded "), accountInput("bad\x00name"), accountInput(strings.Repeat("名", 129)), {Name: "user", PasswordHash: "plaintext"}, {Name: "user", Locale: "unknown", PasswordHash: accountTestHash}} {
		if _, err := s.BootstrapAdmin(ctx, input); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid bootstrap input reached database")
		}
	}
	if _, _, err := s.authorizedTransaction(ctx, domain.Actor{UserID: "forged", SessionID: "forged"}, true); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("malformed actor reached database")
	}
	if _, err := s.RotateSession(ctx, domain.Actor{}, "device", 31*24*time.Hour); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded session lifetime accepted")
	}
	if err := s.ReplaceLibraryAccess(ctx, domain.Actor{}, "unused", []string{"invalid-id"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid ACL identifier reached database")
	}
}
