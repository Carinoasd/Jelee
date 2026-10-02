//go:build !windows && !linux

package runtime

import "testing"

func imagesIntegrationScratch(t *testing.T) string {
	t.Helper()
	t.Skip("local image private staging is supported only on Linux and Windows")
	return ""
}
