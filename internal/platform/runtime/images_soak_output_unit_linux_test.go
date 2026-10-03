//go:build jelee_probe_tests && linux

package runtime

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestImagesSoakOutputReopensSamePipeAndHonorsBackpressureDeadline(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output, err := openImagesSoakOutput(writer)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if _, err := output.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	var body [5]byte
	if n, err := reader.Read(body[:]); err != nil || n != len(body) || string(body[:]) != "probe" {
		t.Fatal("reopened output did not address the original pipe")
	}
	if err := output.SetWriteDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = output.Write(make([]byte, 2<<20))
	if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("blocked output failed to honor its deadline")
	}
	output.Close()
	if _, err := writer.Stat(); err != nil {
		t.Fatal("closing owned output closed original stdout")
	}
}

func TestImagesSoakOutputRejectsRegularFileAndMissingHandle(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, source := range []*os.File{nil, file} {
		if _, err := openImagesSoakOutput(source); err != errImagesSoakStream {
			t.Fatal("non-pipe accepted")
		}
	}
}
