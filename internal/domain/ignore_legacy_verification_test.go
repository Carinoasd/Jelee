package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLegacyVerificationPrivateEvidence(t *testing.T) {
	token := LegacyIgnoreVerificationToken{JobID: "private-job", Generation: 77, Sequence: 31, After: IgnoreProofCursor{RootID: "private-root", Directory: "private-directory"}}
	page := LegacyIgnoreVerificationPage{Token: token, Observations: []LegacyIgnoreObservation{legacyObservationFixture()}, Complete: true}
	for _, value := range []any{token, page} {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != "{}" {
			t.Fatal("verification evidence JSON leak")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			text := fmt.Sprintf(format, value)
			if strings.Contains(text, "private-") || !strings.Contains(text, "redacted") {
				t.Fatal("verification evidence format leak")
			}
		}
	}
}
