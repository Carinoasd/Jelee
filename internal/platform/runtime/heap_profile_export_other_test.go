//go:build jelee_probe_tests && !linux

package runtime

import (
	"errors"
	"io"
)

func prepareHeapProfileExport() {}

func exportHeapProfile(string, string, io.Writer) error {
	return errors.New(heapProfileExportFailure)
}
