package domain

import (
	"encoding/binary"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// These are bounded, private persistence data, never filesystem authorization.
type NFOWriteCommitRecord struct {
	JobID      string    `json:"-"`
	Sequence   int       `json:"-"`
	Generation int64     `json:"-"`
	Token      string    `json:"-"`
	Owner      string    `json:"-"`
	RecordedAt time.Time `json:"-"`
	LeaseUntil time.Time `json:"-"`
}

func (NFOWriteCommitRecord) String() string   { return "nfo commit record (data redacted)" }
func (NFOWriteCommitRecord) GoString() string { return "nfo commit record (data redacted)" }

type NFOWriteCommitFilePlan struct {
	Version        uint8    `json:"-"`
	TargetName     string   `json:"-"`
	ParentIdentity [48]byte `json:"-"`
	TargetIdentity [48]byte `json:"-"`
}

// Phase 1 is the complete output/witness pair; phase 2 also retains rollback.
// Each phase is immutable first evidence, not filesystem authorization.
type NFOWriteCommitFileCheckpoint struct {
	Phase            uint8    `json:"-"`
	OutputIdentity   [48]byte `json:"-"`
	RollbackIdentity [48]byte `json:"-"`
}

func (NFOWriteCommitFileCheckpoint) String() string   { return "nfo commit checkpoint (redacted)" }
func (NFOWriteCommitFileCheckpoint) GoString() string { return "nfo commit checkpoint (redacted)" }

func ValidateNFOWriteCommitFileCheckpoint(v NFOWriteCommitFileCheckpoint) error {
	if v.Phase == 1 && ValidNFONativeIdentity(v.OutputIdentity, 1) && v.RollbackIdentity == ([48]byte{}) {
		return nil
	}
	if v.Phase == 2 {
		return ValidateNFOWriteCommitFilesReady(NFOWriteCommitFilesReady{OutputIdentity: v.OutputIdentity, RollbackIdentity: v.RollbackIdentity})
	}
	return ErrInvalid
}

type NFOWriteCommitFilesReady struct {
	OutputIdentity   [48]byte `json:"-"`
	RollbackIdentity [48]byte `json:"-"`
}

// Evidence is a bounded observation of retained preparation, never proof that
// a target Rename committed or authorization for a filesystem change.
type NFOWriteCommitFileEvidence struct {
	Record             NFOWriteCommitRecord         `json:"-"`
	PlanRecorded       bool                         `json:"-"`
	ReadyRecorded      bool                         `json:"-"`
	CheckpointRecorded bool                         `json:"-"`
	Checkpoint         NFOWriteCommitFileCheckpoint `json:"-"`
	Plan               NFOWriteCommitFilePlan       `json:"-"`
	Ready              NFOWriteCommitFilesReady     `json:"-"`
}

func (NFOWriteCommitFileEvidence) String() string   { return "nfo commit file evidence (redacted)" }
func (NFOWriteCommitFileEvidence) GoString() string { return "nfo commit file evidence (redacted)" }

func (NFOWriteCommitFilePlan) String() string     { return "nfo commit file plan (redacted)" }
func (NFOWriteCommitFilePlan) GoString() string   { return "nfo commit file plan (redacted)" }
func (NFOWriteCommitFilesReady) String() string   { return "nfo commit files (redacted)" }
func (NFOWriteCommitFilesReady) GoString() string { return "nfo commit files (redacted)" }

// This checks the portable record's canonical encoding, not physical ownership.
// Platform adapters must observe actual held handles and verify retained files.
func ValidNFONativeIdentity(data [48]byte, kind byte) bool {
	if data[0] != 1 || (data[1] != 1 && data[1] != 2) || data[2] != kind || kind != 1 && kind != 2 {
		return false
	}
	for _, b := range data[3:8] {
		if b != 0 {
			return false
		}
	}
	for _, b := range data[44:48] {
		if b != 0 {
			return false
		}
	}
	if data[1] == 1 {
		return binary.LittleEndian.Uint32(data[40:44]) == 0
	}
	return binary.LittleEndian.Uint64(data[24:32]) == 0 && binary.LittleEndian.Uint32(data[40:44]) < 1_000_000_000
}

func ValidateNFOWriteCommitFilePlan(v NFOWriteCommitFilePlan) error {
	if v.Version != 1 || len(v.TargetName) < 1 || len(v.TargetName) > 1024 || !utf8.ValidString(v.TargetName) || !strings.HasSuffix(strings.ToLower(v.TargetName), ".nfo") || strings.ContainsAny(v.TargetName, "/\\:") || strings.ContainsFunc(v.TargetName, unicode.IsControl) || !ValidNFONativeIdentity(v.ParentIdentity, 2) || !ValidNFONativeIdentity(v.TargetIdentity, 1) || v.ParentIdentity[1] != v.TargetIdentity[1] {
		return ErrInvalid
	}
	return nil
}

func ValidateNFOWriteCommitFilesReady(v NFOWriteCommitFilesReady) error {
	if !ValidNFONativeIdentity(v.OutputIdentity, 1) || !ValidNFONativeIdentity(v.RollbackIdentity, 1) || v.OutputIdentity[1] != v.RollbackIdentity[1] || v.OutputIdentity == v.RollbackIdentity {
		return ErrInvalid
	}
	return nil
}
