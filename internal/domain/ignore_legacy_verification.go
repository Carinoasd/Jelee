package domain

// A distinct token type prevents substituting the custom rule manifest's
// cursor for the legacy query cursor. Tokens are not publication authority.
type LegacyIgnoreVerificationToken IgnoreVerificationToken

func (LegacyIgnoreVerificationToken) String() string {
	return "legacy verification token (data redacted)"
}
func (LegacyIgnoreVerificationToken) GoString() string {
	return "legacy verification token (data redacted)"
}

type LegacyIgnoreVerificationPage struct {
	Token        LegacyIgnoreVerificationToken `json:"-"`
	Observations []LegacyIgnoreObservation     `json:"-"`
	Complete     bool                          `json:"-"`
}

func (LegacyIgnoreVerificationPage) String() string {
	return "legacy verification page (data redacted)"
}
func (LegacyIgnoreVerificationPage) GoString() string {
	return "legacy verification page (data redacted)"
}
