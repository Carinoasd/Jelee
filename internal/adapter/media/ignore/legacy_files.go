//go:build linux || windows

package ignoresource

// OpenLegacyRule opens only the fixed legacy leaf through the same native
// read-only/no-follow primitive. It does not select a rule family for a job.
func (f *nativeFile) OpenLegacyRule() (sourceFile, error) {
	opened, err := f.open(".ignore", false, true)
	if err != nil {
		return nil, err
	}
	return opened, nil
}
