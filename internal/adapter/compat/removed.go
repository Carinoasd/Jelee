// Package compat isolates requests for removed legacy protocol surfaces and
// hosts the third-party client compatibility layer mounted under Prefix.
package compat

import "strings"

// RemovedFeaturePath identifies only the retired root controller families.
// It receives the decoded URL path, never the query or a raw request target.
// It does not create a listener, service, job or media operation.
//
// The families are recognised both at the server root and directly below the
// compatibility prefix (matched case-insensitively), so a compatibility client
// probing "/compat/LiveTv" gets the same refusal as "/LiveTv". Only one prefix
// segment is stripped; deeper nesting is not a removed-feature root.
func RemovedFeaturePath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	if rest, ok := trimPrefix(path); ok {
		path = rest
	}
	root, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch strings.ToLower(root) {
	case "livetv", "channels", "dlna":
		return true
	default:
		return false
	}
}
