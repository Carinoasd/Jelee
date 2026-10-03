package domain

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestNFOWriteCommitCheckpointCodecAndPrivacy(t *testing.T) {
	for _, platform := range []byte{1, 2} {
		identity := func(tag byte) [48]byte {
			var value [48]byte
			value[0], value[1], value[2], value[16] = 1, platform, 1, tag
			return value
		}
		output, rollback := identity(5), identity(6)
		first := NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: output}
		complete := NFOWriteCommitFileCheckpoint{Phase: 2, OutputIdentity: output, RollbackIdentity: rollback}
		for _, value := range []NFOWriteCommitFileCheckpoint{first, complete} {
			if ValidateNFOWriteCommitFileCheckpoint(value) != nil {
				t.Fatal("valid checkpoint shape refused")
			}
			encoded, err := json.Marshal(value)
			if err != nil || string(encoded) != "{}" || fmt.Sprint(value) != "nfo commit checkpoint (redacted)" || fmt.Sprintf("%#v", value) != "nfo commit checkpoint (redacted)" {
				t.Fatal("checkpoint observation exposed")
			}
		}
		for _, bad := range []NFOWriteCommitFileCheckpoint{
			{}, {Phase: 3, OutputIdentity: output}, {Phase: 1, OutputIdentity: output, RollbackIdentity: rollback}, {Phase: 2, OutputIdentity: output}, {Phase: 2, OutputIdentity: output, RollbackIdentity: output},
		} {
			if ValidateNFOWriteCommitFileCheckpoint(bad) != ErrInvalid {
				t.Fatal("incomplete checkpoint shape admitted")
			}
		}
	}
}
