//go:build !linux && !windows

package legacyignorehelper

import "errors"

func verifyAllocationDenied() error { return errors.New("unsupported") }
