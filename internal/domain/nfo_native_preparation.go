package domain

import (
	"encoding/binary"
	"slices"
)

const NFONativePreparationMaxAncestors = 128
const NFONativePreparationReceiptMaxBytes = 8 + 48*(3+NFONativePreparationMaxAncestors)

// NFONativePreparationReceipt stores bounded observations without paths. The
// ancestor order must come from the deterministic scope path plan. Canonical
// bytes alone prove neither physical ownership nor filesystem authorization.
// File IDs may be reused and creation timestamps are not unforgeable.
type NFONativePreparationReceipt struct {
	root, media, nfoFile [48]byte
	ancestors            [][48]byte
}

func (NFONativePreparationReceipt) String() string   { return "nfo native preparation (redacted)" }
func (NFONativePreparationReceipt) GoString() string { return "nfo native preparation (redacted)" }

func NewNFONativePreparationReceipt(root, media, nfoFile [48]byte, ancestors [][48]byte) (NFONativePreparationReceipt, error) {
	if len(ancestors) < 1 || len(ancestors) > NFONativePreparationMaxAncestors {
		return NFONativePreparationReceipt{}, ErrInvalid
	}
	v := NFONativePreparationReceipt{root: root, media: media, nfoFile: nfoFile, ancestors: slices.Clone(ancestors)}
	if ValidateNFONativePreparationReceipt(v) != nil {
		return NFONativePreparationReceipt{}, ErrInvalid
	}
	return v, nil
}

func (v NFONativePreparationReceipt) Empty() bool {
	return v.root == ([48]byte{}) && v.media == ([48]byte{}) && v.nfoFile == ([48]byte{}) && len(v.ancestors) == 0
}
func (v NFONativePreparationReceipt) RootIdentity() [48]byte    { return v.root }
func (v NFONativePreparationReceipt) MediaIdentity() [48]byte   { return v.media }
func (v NFONativePreparationReceipt) NFOFileIdentity() [48]byte { return v.nfoFile }
func (v NFONativePreparationReceipt) AncestorIdentities() [][48]byte {
	return slices.Clone(v.ancestors)
}
func CloneNFONativePreparationReceipt(v NFONativePreparationReceipt) NFONativePreparationReceipt {
	v.ancestors = slices.Clone(v.ancestors)
	return v
}
func (v NFONativePreparationReceipt) Equal(other NFONativePreparationReceipt) bool {
	return v.root == other.root && v.media == other.media && v.nfoFile == other.nfoFile && slices.Equal(v.ancestors, other.ancestors)
}
func ValidateNFONativePreparationReceipt(v NFONativePreparationReceipt) error {
	if !ValidNFONativeIdentity(v.root, 2) || !ValidNFONativeIdentity(v.media, v.media[2]) || !ValidNFONativeIdentity(v.nfoFile, 1) || v.root[1] != v.media[1] || v.root[1] != v.nfoFile[1] || len(v.ancestors) < 1 || len(v.ancestors) > NFONativePreparationMaxAncestors {
		return ErrInvalid
	}
	// A file target must not be the same physical object as original media.
	if v.media[2] == 1 && v.media == v.nfoFile {
		return ErrInvalid
	}
	for _, id := range v.ancestors {
		if !ValidNFONativeIdentity(id, 2) || id[1] != v.root[1] {
			return ErrInvalid
		}
	}
	return nil
}

// MarshalBinary returns an owned, fixed-header encoding bounded by 6296 bytes.
// The header is version, platform, media kind, reserved, ancestor count (LE),
// and two reserved bytes, followed by root/media/NFO and ordered ancestors.
func (v NFONativePreparationReceipt) MarshalBinary() ([]byte, error) {
	if ValidateNFONativePreparationReceipt(v) != nil {
		return nil, ErrInvalid
	}
	data := make([]byte, 8+48*(3+len(v.ancestors)))
	data[0], data[1], data[2] = 1, v.root[1], v.media[2]
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(v.ancestors)))
	copy(data[8:56], v.root[:])
	copy(data[56:104], v.media[:])
	copy(data[104:152], v.nfoFile[:])
	for i, id := range v.ancestors {
		copy(data[152+i*48:200+i*48], id[:])
	}
	return data, nil
}

func ParseNFONativePreparationReceipt(data []byte) (NFONativePreparationReceipt, error) {
	if len(data) < 200 || len(data) > NFONativePreparationReceiptMaxBytes || data[0] != 1 || (data[1] != 1 && data[1] != 2) || (data[2] != 1 && data[2] != 2) || data[3] != 0 || data[6] != 0 || data[7] != 0 {
		return NFONativePreparationReceipt{}, ErrInvalid
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count < 1 || count > NFONativePreparationMaxAncestors || len(data) != 152+48*count {
		return NFONativePreparationReceipt{}, ErrInvalid
	}
	var root, media, nfoFile [48]byte
	copy(root[:], data[8:56])
	copy(media[:], data[56:104])
	copy(nfoFile[:], data[104:152])
	ancestors := make([][48]byte, count)
	for i := range ancestors {
		copy(ancestors[i][:], data[152+i*48:200+i*48])
	}
	v, err := NewNFONativePreparationReceipt(root, media, nfoFile, ancestors)
	if err != nil || root[1] != data[1] || media[2] != data[2] {
		return NFONativePreparationReceipt{}, ErrInvalid
	}
	return v, nil
}
