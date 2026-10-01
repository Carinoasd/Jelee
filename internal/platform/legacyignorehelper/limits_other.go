//go:build !linux && !windows

package legacyignorehelper

import "errors"

func applyLimits() error { return errors.New("legacy_helper_unsupported") }
