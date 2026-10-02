//go:build !windows && !linux

package images

import "testing"

func imageSourceTestBase(t *testing.T) string { return t.TempDir() }
