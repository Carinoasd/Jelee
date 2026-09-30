//go:build !linux && !windows

package process

import "os"

func allowedProductionName(string) bool { return false }

func executableAllowed(string, os.FileInfo) bool { return false }
func platformEnvironment(env []string) []string  { return env }
func validInput(*os.File) bool                   { return false }
func startChild(string, []string, string, []string, *os.File, *os.File, *os.File) (child, error) {
	return nil, ErrUnsupported
}
