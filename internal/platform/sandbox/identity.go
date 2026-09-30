package sandbox

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// MetadataPolicyVersion must change when file grants, syscall permissions or
// helper resource bounds change. Cache identity cannot authorize a policy.
const MetadataPolicyVersion = "linux-metadata-sandbox-v1"

// MetadataArgumentsDigest describes the exact fixed metadata argv, including
// protocol and demuxer restrictions. It does not expose mutable arguments.
func MetadataArgumentsDigest() string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-metadata-argv-v1\x00"))
	for _, argument := range metadataArguments() {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(argument)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(argument))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
