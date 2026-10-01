package domain

// Verification tokens bind one exact manifest prefix to the current lease.
// They never authorize publication independently of the repository fence.
type IgnoreVerificationToken struct {
	JobID      string            `json:"-"`
	Generation int64             `json:"-"`
	Sequence   int64             `json:"-"`
	After      IgnoreProofCursor `json:"-"`
}

func (IgnoreVerificationToken) String() string   { return "ignore verification token (data redacted)" }
func (IgnoreVerificationToken) GoString() string { return "ignore verification token (data redacted)" }

type IgnoreVerificationPage struct {
	Token    IgnoreVerificationToken `json:"-"`
	Proofs   []IgnoreDirectoryProof  `json:"-"`
	Complete bool                    `json:"-"`
}

func (IgnoreVerificationPage) String() string   { return "ignore verification page (data redacted)" }
func (IgnoreVerificationPage) GoString() string { return "ignore verification page (data redacted)" }
