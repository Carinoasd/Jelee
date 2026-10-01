package domain

// A distinct token keeps the baseline lookup cursor separate from the existing
// source query cursor. Completion alone does not authorize publication.
type LegacyIgnoreBaselineVerificationToken IgnoreVerificationToken

func (LegacyIgnoreBaselineVerificationToken) String() string {
	return "legacy baseline verification token (data redacted)"
}
func (LegacyIgnoreBaselineVerificationToken) GoString() string {
	return "legacy baseline verification token (data redacted)"
}

type LegacyIgnoreBaselineVerificationPage struct {
	Token        LegacyIgnoreBaselineVerificationToken `json:"-"`
	Observations []LegacyIgnoreBaselineObservation     `json:"-"`
	Complete     bool                                  `json:"-"`
}

func (LegacyIgnoreBaselineVerificationPage) String() string {
	return "legacy baseline verification page (data redacted)"
}
func (LegacyIgnoreBaselineVerificationPage) GoString() string {
	return "legacy baseline verification page (data redacted)"
}
