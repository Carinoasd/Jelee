package domain

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestNFOCommitAttemptCapacityAndPrivacy(t *testing.T) {
	for _, sizes := range [][2]int64{{1, 1}, {32 << 20, 32 << 20}} {
		n := NFOWriteCommitAttemptRetainedBytes(sizes[0], sizes[1])
		v := NFOWriteCommitAttemptReservation{OriginalBytes: sizes[0], ReplacementBytes: sizes[1], RetainedBytes: n}
		if n != 4*(3*sizes[0]+2*sizes[1]) || ValidateNFOWriteCommitAttemptReservation(v) != nil {
			t.Fatal("legacy and new namespace capacity omitted")
		}
		v.RetainedBytes--
		if ValidateNFOWriteCommitAttemptReservation(v) == nil {
			t.Fatal("undercharged reservation accepted")
		}
	}
	for _, sizes := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}, {1, 33 << 20}, {33 << 20, 1}} {
		if NFOWriteCommitAttemptRetainedBytes(sizes[0], sizes[1]) != 0 {
			t.Fatal("unbounded payload accepted")
		}
	}
	for _, v := range []any{NFOWriteCommitAttemptReservation{}, NFOWriteCommitAttempt{}, NFOWriteCommitAttempts{}} {
		data, err := json.Marshal(v)
		if err != nil || string(data) != "{}" {
			t.Fatal("private attempt data exposed through JSON")
		}
		if len(fmt.Sprintf("%#v", v)) > 64 {
			t.Fatal("private attempt data exposed through formatting")
		}
	}
}
