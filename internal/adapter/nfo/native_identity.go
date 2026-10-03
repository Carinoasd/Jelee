package nfo

import (
	"encoding/binary"
	"errors"
)

var errNativeIdentity = errors.New("nfo_native_identity_unavailable")

// This is a private, versioned observation, not recovery authorization. File
// IDs may be reused and creation times are not an unforgeable ownership proof.
// Recovery must also retain and verify owned witnesses and the complete intent.
// No path, content, size or mutable mtime is part of this identity record.
type nfoNativeIdentity struct{ record [48]byte }

func (nfoNativeIdentity) String() string   { return "nfo native identity (redacted)" }
func (nfoNativeIdentity) GoString() string { return "nfo native identity (redacted)" }

func parseNFONativeIdentity(data []byte) (nfoNativeIdentity, error) {
	if len(data) != 48 || data[0] != 1 || (data[1] != 1 && data[1] != 2) || (data[2] != 1 && data[2] != 2) {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	for _, b := range append(append([]byte{}, data[3:8]...), data[44:48]...) {
		if b != 0 {
			return nfoNativeIdentity{}, errNativeIdentity
		}
	}
	if data[1] == 1 {
		if binary.LittleEndian.Uint32(data[40:44]) != 0 {
			return nfoNativeIdentity{}, errNativeIdentity
		}
	} else {
		if binary.LittleEndian.Uint64(data[24:32]) != 0 || binary.LittleEndian.Uint32(data[40:44]) >= 1_000_000_000 {
			return nfoNativeIdentity{}, errNativeIdentity
		}
	}
	var result nfoNativeIdentity
	copy(result.record[:], data)
	return result, nil
}
