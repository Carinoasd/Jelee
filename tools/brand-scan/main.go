// brand-scan reports legacy naming outside narrowly documented exceptions.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() { os.Exit(run()) }
func run() int {
	incremental := flag.Bool("new", false, "scan only the new Go service and its documentation; not full migration acceptance")
	flag.Parse()
	data, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot enumerate repository")
		return 2
	}
	allow, err := os.ReadFile("tools/brand-scan/allowlist.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read brand exceptions")
		return 2
	}
	exceptions := map[string]bool{}
	for _, line := range strings.Split(string(allow), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line != "" {
			exceptions[line] = true
		}
	}
	pattern := regexp.MustCompile(`(?i)Jellyfin|Emby|MediaBrowser`)
	seen := map[string]bool{}
	files := strings.Split(string(data), "\x00")
	sort.Strings(files)
	hits, allowed := 0, 0
	for _, file := range files {
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		if *incremental && !newService(file) {
			continue
		}
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot scan %s\n", file)
			return 2
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		lines := bufio.NewScanner(bytes.NewReader(data))
		lines.Buffer(make([]byte, 4096), 4<<20)
		line := 0
		for lines.Scan() {
			line++
			if pattern.Match(lines.Bytes()) {
				if exceptions[file] {
					allowed++
					continue
				}
				fmt.Printf("%s:%d: legacy name\n", file, line)
				hits++
			}
		}
		if lines.Err() != nil {
			fmt.Fprintf(os.Stderr, "scan limit exceeded: %s\n", file)
			return 2
		}
	}
	fmt.Printf("brand scan: mode=new:%t violations=%d allowed=%d\n", *incremental, hits, allowed)
	if hits > 0 {
		return 1
	}
	return 0
}
func newService(path string) bool {
	for _, prefix := range []string{"cmd/", "internal/", "tools/", "scripts/", "web/", "deploy/", "docs/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch path {
	case "README.md", "go.mod", "go.sum", "Makefile", "Dockerfile", ".env.example", ".github/workflows/jelee.yml":
		return true
	}
	return false
}
