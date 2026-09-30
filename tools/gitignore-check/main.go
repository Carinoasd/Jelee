package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() { os.Exit(run()) }
func run() int {
	probes := []string{".tools/check.exe", ".bin/go.cmd", ".testfixtures/video.mp4", ".testdata/store.db", ".cache/cache", "node_modules/package.json", "reports/report.json", ".env", "data/subtitles/cache", "private.key", "coverage.out", "web/dist/app.js"}
	for _, path := range probes {
		if err := exec.Command("git", "check-ignore", "--no-index", "-q", path).Run(); err != nil {
			fmt.Printf("not ignored: %s\n", path)
			return 1
		}
	}
	files, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot enumerate tracked files")
		return 2
	}
	violations := 0
	for _, file := range strings.Split(string(files), "\x00") {
		if file == "" {
			continue
		}
		for _, prefix := range []string{".tools/", ".bin/", ".testdata/", ".testfixtures/", ".cache/", "node_modules/", "data/"} {
			if strings.HasPrefix(file, prefix) {
				fmt.Printf("tracked generated artifact: %s\n", file)
				violations++
			}
		}
	}
	// Check newly authored files for secrets and binary/large outputs, including
	// untracked files. Existing upstream assets are inventoried separately.
	pending, err := exec.Command("git", "ls-files", "-z", "--others", "--exclude-standard").Output()
	if err != nil {
		return 2
	}
	modified, err := exec.Command("git", "diff", "--name-only", "-z", "HEAD").Output()
	if err != nil {
		return 2
	}
	seen := map[string]bool{}
	for _, file := range strings.Split(string(append(pending, modified...)), "\x00") {
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Printf("cannot inspect %s\n", file)
			violations++
			continue
		}
		if info.IsDir() {
			continue
		}
		extension := strings.ToLower(filepath.Ext(file))
		for _, ext := range []string{".exe", ".dll", ".zip", ".gz", ".mp4", ".mkv", ".db", ".sqlite", ".pem", ".key", ".p12"} {
			if extension == ext {
				fmt.Printf("new artifact needs review: %s\n", file)
				violations++
			}
		}
		if info.Size() > 1<<20 {
			fmt.Printf("new file exceeds 1 MiB: %s\n", file)
			violations++
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return 2
		}
		if bytes.IndexByte(data, 0) >= 0 {
			fmt.Printf("new binary needs allowlist: %s\n", file)
			violations++
		}
		for _, marker := range []string{"-----BEGIN " + "PRIVATE KEY-----", "-----BEGIN " + "RSA PRIVATE KEY-----"} {
			if bytes.Contains(data, []byte(marker)) {
				fmt.Printf("private key marker: %s\n", file)
				violations++
			}
		}
	}
	fmt.Printf("gitignore check: violations=%d; upstream binary inventory remains separate (see docs/binary-allowlist.md)\n", violations)
	if violations > 0 {
		return 1
	}
	return 0
}
