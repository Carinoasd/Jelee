package domain

// ScanIntent is public intent after transport validation. Trusted parser/tool
// identities are separate arguments and never supplied by request JSON.
type ScanIntent struct {
	Probe ProbeIntent
	NFO   bool
}

// Capabilities only filter claims; they do not authorize an identity or source.
type ScanCapabilities struct {
	Probe bool
	NFO   bool
}

func ValidateScanIntent(v ScanIntent) error {
	if ValidateProbeIntent(v.Probe) != nil || v.NFO && v.Probe.Scope != "" && v.Probe.Scope != ProbeScopeIncremental {
		return ErrInvalid
	}
	return nil
}
