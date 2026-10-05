package domain

const NFOWriteCommitAttemptLimit uint8 = 3

// Reservation includes legacy names and all three new namespaces. It is private
// durable intent, not authority over a filesystem or a retained unknown object.
type NFOWriteCommitAttemptReservation struct {
	OriginalBytes    int64    `json:"-"`
	ReplacementBytes int64    `json:"-"`
	OriginalHash     [32]byte `json:"-"`
	ReplacementHash  [32]byte `json:"-"`
	RetainedBytes    int64    `json:"-"`
}

func (NFOWriteCommitAttemptReservation) String() string { return "nfo attempt reservation (redacted)" }
func (NFOWriteCommitAttemptReservation) GoString() string {
	return "nfo attempt reservation (redacted)"
}
func NFOWriteCommitAttemptRetainedBytes(original, replacement int64) int64 {
	if original < 1 || replacement < 1 || original > 32<<20 || replacement > 32<<20 {
		return 0
	}
	return int64(NFOWriteCommitAttemptLimit+1) * (3*original + 2*replacement)
}
func ValidateNFOWriteCommitAttemptReservation(v NFOWriteCommitAttemptReservation) error {
	if n := NFOWriteCommitAttemptRetainedBytes(v.OriginalBytes, v.ReplacementBytes); n != 0 && v.RetainedBytes == n {
		return nil
	}
	return ErrInvalid
}

type NFOWriteCommitAttempt struct {
	Number             uint8                        `json:"-"`
	CheckpointRecorded bool                         `json:"-"`
	Checkpoint         NFOWriteCommitFileCheckpoint `json:"-"`
	ReadyRecorded      bool                         `json:"-"`
	Ready              NFOWriteCommitFilesReady     `json:"-"`
}

func (NFOWriteCommitAttempt) String() string   { return "nfo commit attempt (redacted)" }
func (NFOWriteCommitAttempt) GoString() string { return "nfo commit attempt (redacted)" }

type NFOWriteCommitAttempts struct {
	ReservationRecorded bool                             `json:"-"`
	Reservation         NFOWriteCommitAttemptReservation `json:"-"`
	Attempts            [3]NFOWriteCommitAttempt         `json:"-"`
}

func (NFOWriteCommitAttempts) String() string   { return "nfo commit attempts (redacted)" }
func (NFOWriteCommitAttempts) GoString() string { return "nfo commit attempts (redacted)" }
