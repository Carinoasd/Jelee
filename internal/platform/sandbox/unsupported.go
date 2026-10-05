//go:build !linux || !amd64

package sandbox

import "context"

const supportedBuild = false

func verifyProfile(context.Context, Profile, Policy) error { return ErrUnsupported }
func executeHelper(Profile, Policy, []string) error        { return ErrUnsupported }
func verifyHelperExecutable(string) error                  { return ErrUnsupported }

func executeTool(ToolProfile, ToolPolicy, Extraction) error { return ErrUnsupported }

func verifyDataFiles(context.Context, []PinnedFile, bool) error { return ErrUnsupported }
