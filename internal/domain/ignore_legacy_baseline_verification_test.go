package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLegacyBaselineVerificationPrivateEvidence(t *testing.T) {
	token := LegacyIgnoreBaselineVerificationToken{JobID: "private-job", Generation: 77, Sequence: 31, After: IgnoreProofCursor{RootID: "private-root", Directory: "private-lookup"}}
	page := LegacyIgnoreBaselineVerificationPage{Token: token}
	for _, value := range []any{token, page} {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != "{}" {
			t.Fatal("private baseline evidence JSON leak")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			rendered := fmt.Sprintf(format, value)
			if strings.Contains(rendered, "private-") || !strings.Contains(rendered, "redacted") {
				t.Fatal("private baseline evidence log leak")
			}
		}
	}
}
