package domain

import "errors"

const (
	IgnoreModeJeleeignore      = "jeleeignore"
	IgnoreCaseSensitive        = "sensitive"
	IgnoreCaseASCIIInsensitive = "ascii-insensitive"
	IgnoreProgramVersion       = "jeleeignore-v1"
	IgnoreProofVersion         = "jeleeignore-proof-v1"
)

var ErrIgnoreUnavailable = errors.New("ignore_unavailable")

// IgnoreIntent freezes only the requested mode and case behavior. Its zero
// value is off; an enabled mode always requires an explicit supported case.
type IgnoreIntent struct {
	Mode     string
	CaseMode string
}

func ValidateIgnoreIntent(v IgnoreIntent) error {
	if v.Mode == "" && v.CaseMode == "" {
		return nil
	}
	if v.Mode != IgnoreModeJeleeignore || (v.CaseMode != IgnoreCaseSensitive && v.CaseMode != IgnoreCaseASCIIInsensitive) {
		return ErrInvalid
	}
	return nil
}

// IgnoreIdentity is fixed by server code. It describes the intended contract;
// a valid identity does not mean that ignore execution is available.
type IgnoreIdentity struct {
	ProgramVersion string
	ProofVersion   string
}

func DefaultIgnoreIdentity() IgnoreIdentity {
	return IgnoreIdentity{ProgramVersion: IgnoreProgramVersion, ProofVersion: IgnoreProofVersion}
}

func ValidateIgnoreIdentity(v IgnoreIdentity) error {
	if v != DefaultIgnoreIdentity() {
		return ErrInvalid
	}
	return nil
}

// IgnoreRequest retains an enabled job's immutable intent and trusted identity.
// It carries no rules, source proof, filesystem authority or public DTO fields.
type IgnoreRequest struct {
	JobID     string         `json:"-"`
	LibraryID string         `json:"-"`
	Intent    IgnoreIntent   `json:"-"`
	Identity  IgnoreIdentity `json:"-"`
}

func (IgnoreRequest) String() string   { return "ignore request (data redacted)" }
func (IgnoreRequest) GoString() string { return "ignore request (data redacted)" }

// ValidateIgnoreRequest checks the current contract, not an execution lease or
// historical replay. Persistence separately matches it to the job's frozen intent.
func ValidateIgnoreRequest(v IgnoreRequest) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) || v.Intent.Mode == "" ||
		ValidateIgnoreIntent(v.Intent) != nil || ValidateIgnoreIdentity(v.Identity) != nil {
		return ErrInvalid
	}
	return nil
}
