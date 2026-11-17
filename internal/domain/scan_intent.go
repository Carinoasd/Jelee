package domain

// ScanIntent is public intent after transport validation. Trusted identities
// come from server code or separate arguments, never request JSON.
type ScanIntent struct {
	Probe  ProbeIntent
	NFO    bool
	Ignore IgnoreIntent
}

// Capabilities only filter claims; they do not authorize an identity or source.
type ScanCapabilities struct {
	CatalogImport bool
	CatalogSync   bool
	Probe         bool
	NFO           bool
	Ignore        bool
	FamilyIgnore  bool
	// NFOWrite lets a worker with a configured writer claim nfo_write jobs.
	NFOWrite bool
	// ConsistencyCheck lets a worker with a configured checker claim
	// consistency_check jobs.
	ConsistencyCheck bool
	// NFOApply lets a worker with a configured applier claim nfo_apply jobs.
	NFOApply bool
	// NFOExport lets a worker with a configured exporter claim nfo_export
	// jobs whose child write job, if any, has finished.
	NFOExport bool
	// ImageRefresh lets a worker with a configured refresher claim
	// image_refresh jobs.
	ImageRefresh bool
	// MetadataRefresh lets a worker claim metadata_refresh jobs; one without
	// a configured provider fails them.
	MetadataRefresh bool
	// Kinds are further job kinds the worker claims (maintenance jobs,
	// G13.6, G14.7); each has a self-contained worker.
	Kinds []string
}

func ValidateScanIntent(v ScanIntent) error {
	return validateScanIntent(v, false)
}

// ValidateScanIntentWithFamilyIgnore requires an admission path that separately
// checks availability for each family and freezes a server-owned identity.
func ValidateScanIntentWithFamilyIgnore(v ScanIntent) error {
	return validateScanIntent(v, true)
}

func validateScanIntent(v ScanIntent, familyAllowed bool) error {
	if ValidateProbeIntent(v.Probe) != nil || (ValidateIgnoreIntent(v.Ignore) != nil && !(familyAllowed && ValidateFamilyIgnoreIntent(v.Ignore) == nil)) {
		return ErrInvalid
	}
	if (v.NFO || v.Ignore.Mode != "") && v.Probe.Scope != "" && v.Probe.Scope != ProbeScopeIncremental {
		return ErrInvalid
	}
	return nil
}
