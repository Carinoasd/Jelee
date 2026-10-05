package compat

import (
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestIDRoundTrip(t *testing.T) {
	for _, native := range []string{
		"0123abcd-4567-89ef-0123-456789abcdef",
		"00000000-0000-0000-0000-000000000001", // smallest non-nil
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"10000000-0000-4000-8000-000000000000",
	} {
		wire, err := FormatID(native)
		if err != nil || len(wire) != 32 || strings.Contains(wire, "-") || wire != strings.ToLower(wire) {
			t.Fatalf("FormatID(%q) = %q, %v", native, wire, err)
		}
		for _, input := range []string{wire, strings.ToUpper(wire), native, strings.ToUpper(native)} {
			back, err := ParseID(input)
			if err != nil || back != native {
				t.Fatalf("ParseID(%q) = %q, %v; want %q", input, back, err, native)
			}
		}
	}
	got, err := ParseID("0123ABcd456789EF0123456789abcdef")
	if err != nil || got != "0123abcd-4567-89ef-0123-456789abcdef" {
		t.Fatalf("mixed case: %q %v", got, err)
	}
}

func TestParseIDRejects(t *testing.T) {
	valid := "0123abcd456789ef0123456789abcdef"
	for _, input := range []string{
		"", "0", valid[:31], valid + "0", valid[:30] + "  ",
		" " + valid[1:], valid[:31] + " ", valid[:31] + "\x00", valid[:31] + "\n",
		valid[:31] + "g", valid[:31] + "G", valid[:31] + "-", "-" + valid[1:], "+" + valid[1:],
		"{" + valid[:30] + "}", "(" + valid[:30] + ")",
		strings.Repeat("0", 32), "00000000-0000-0000-0000-000000000000",
		"0123abcd-4567-89ef-0123-456789abcde",       // 35
		"0123abcd-4567-89ef-0123-456789abcdef0",     // 37
		"0123abcd4-567-89ef-0123-456789abcdef",      // misplaced dash
		"0123abcd-4567-89ef-0123-456789abcd-f",      // extra dash in a group
		"{0123abcd-4567-89ef-0123-456789abcdef}",    // braces
		"0123abcd_4567_89ef_0123_456789abcdef",      // wrong separator
		"0123abcd-4567-89ef-0123-456789abcdeé"[:36], // non-ASCII truncated
		"0123abcd456789ef0123456789abcdeé"[:32],
		"０123abcd456789ef0123456789abcd",
		"0x23abcd456789ef0123456789abcdef",
	} {
		if got, err := ParseID(input); err == nil || !errors.Is(err, ErrInvalidID) || !errors.Is(err, domain.ErrInvalid) || got != "" {
			t.Fatalf("ParseID(%q) = %q, %v", input, got, err)
		}
	}
}

func TestFormatIDRejects(t *testing.T) {
	for _, input := range []string{
		"", "0123abcd456789ef0123456789abcdef", "0123ABCD-4567-89EF-0123-456789ABCDEF",
		"00000000-0000-0000-0000-000000000000", " 0123abcd-4567-89ef-0123-456789abcdef",
		"{0123abcd-4567-89ef-0123-456789abcdef}",
	} {
		if got, err := FormatID(input); !errors.Is(err, ErrInvalidID) || got != "" {
			t.Fatalf("FormatID(%q) = %q, %v", input, got, err)
		}
	}
}

func FuzzParseID(f *testing.F) {
	f.Add("0123abcd456789ef0123456789abcdef")
	f.Add("0123abcd-4567-89ef-0123-456789abcdef")
	f.Add("{0123abcd-4567-89ef-0123-456789abcdef}")
	f.Fuzz(func(t *testing.T, input string) {
		native, err := ParseID(input)
		if err != nil {
			return
		}
		if !domain.ValidID(native) {
			t.Fatalf("non-canonical result %q", native)
		}
		wire, err := FormatID(native)
		if err != nil || !strings.EqualFold(wire, strings.ReplaceAll(input, "-", "")) {
			t.Fatalf("round trip %q -> %q -> %q %v", input, native, wire, err)
		}
	})
}
