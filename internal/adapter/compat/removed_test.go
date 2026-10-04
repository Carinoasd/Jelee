package compat

import "testing"

func TestRemovedFeaturePath(t *testing.T) {
	for path, want := range map[string]bool{
		// Root families keep their behaviour.
		"/LiveTv": true, "/livetv/Info": true, "/Channels": true, "/Dlna/x/description.xml": true,
		"/LiveTvExtra": false, "/other/LiveTv": false, "/api/v1/LiveTv": false, "LiveTv": false, "": false, "/": false,
		// The same families below the compatibility prefix, any letter case.
		"/compat/LiveTv": true, "/compat/livetv/Info": true, "/COMPAT/Channels/x/Items": true, "/Compat/DLNA": true,
		"/compat/LiveTvExtra": false, "/compat/System/Info": false, "/compat": false, "/compat/": false,
		"/compatx/LiveTv": false, "/compat/compat/LiveTv": false, "/compat/x/LiveTv": false,
	} {
		if got := RemovedFeaturePath(path); got != want {
			t.Fatalf("RemovedFeaturePath(%q) = %v, want %v", path, got, want)
		}
	}
}
