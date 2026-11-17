//go:build !windows || !amd64

package sandbox

import "context"

// directBuild selects the Windows direct launcher; see direct_windows.go.
const directBuild = false

func newDirect(context.Context, Profile, Policy) (*Launcher, error) { return nil, ErrUnsupported }
