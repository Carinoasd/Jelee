// Command textcheck is the G01.5 encoding gate. Every tracked text file must
// be UTF-8 without a byte order mark and use LF line endings only, and every
// tracked binary file (a NUL byte in its first 8000 bytes, Git's own test)
// must resolve to the .gitattributes "binary" attributes so Git neither
// converts line endings nor diffs or merges it as text.
//
// By default it checks the work tree copy of every tracked file; with
// -staged it checks the index copy of added, copied, modified and renamed
// files (the pre-commit hook).
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("textcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	staged := flags.Bool("staged", false, "check the index copy of staged files instead of every tracked file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	files, err := listFiles(*root, *staged)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "textcheck: %v\n", err)
		return 2
	}
	attrs, err := attributes(*root, files)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "textcheck: %v\n", err)
		return 2
	}
	read := worktreeReader(*root)
	if *staged {
		batch, err := newIndexReader(*root)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "textcheck: %v\n", err)
			return 2
		}
		defer batch.close()
		read = batch.read
	}
	violations, binaries := 0, 0
	for _, f := range files {
		data, err := read(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "textcheck: %s: %v\n", f, err)
			return 2
		}
		problems := inspect(data, attrs[f])
		if isBinary(data) {
			binaries++
		}
		for _, p := range problems {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", f, p)
			violations++
		}
	}
	_, _ = fmt.Fprintf(stdout, "text check: files=%d binary=%d violations=%d\n", len(files), binaries, violations)
	if violations > 0 {
		_, _ = fmt.Fprintln(stdout, "text files must be UTF-8 without BOM with LF endings; binary files need a binary rule in .gitattributes")
		return 1
	}
	return 0
}

// isBinary applies Git's heuristic: a NUL byte in the first 8000 bytes.
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// attr holds the resolved text, eol and diff attributes of one path.
type attr struct {
	text, eol, diff string
}

// inspect returns the encoding problems of one file.
func inspect(data []byte, a attr) []string {
	if isBinary(data) {
		if a.text != "unset" || a.diff != "unset" {
			return []string{fmt.Sprintf("binary content but .gitattributes resolves text=%s diff=%s; add a `binary` rule", a.text, a.diff)}
		}
		return nil
	}
	var problems []string
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		problems = append(problems, "UTF-8 byte order mark")
	}
	if !utf8.Valid(data) {
		problems = append(problems, fmt.Sprintf("invalid UTF-8 at line %d", invalidLine(data)))
	}
	if i := bytes.IndexByte(data, '\r'); i >= 0 {
		problems = append(problems, fmt.Sprintf("CR line ending at line %d", bytes.Count(data[:i], []byte{'\n'})+1))
	}
	if a.text == "unset" {
		problems = append(problems, "text content marked binary (-text) in .gitattributes")
	} else if a.eol != "lf" && a.eol != "unspecified" {
		problems = append(problems, fmt.Sprintf(".gitattributes sets eol=%s; only lf is allowed", a.eol))
	}
	return problems
}

func invalidLine(data []byte) int {
	line := 1
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size <= 1 {
			return line
		}
		if r == '\n' {
			line++
		}
		data = data[size:]
	}
	return line
}

// listFiles returns tracked regular files, or the staged ones with -staged.
func listFiles(root string, staged bool) ([]string, error) {
	if staged {
		return gitZ(root, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	}
	lines, err := gitZ(root, "ls-files", "-z", "-s")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range lines {
		meta, p, _ := strings.Cut(l, "\t")
		if strings.HasPrefix(meta, "100") { // regular files; not symlinks (120000) or submodules (160000)
			files = append(files, p)
		}
	}
	return files, nil
}

// attributes resolves text, eol and diff for every file in one git call.
func attributes(root string, files []string) (map[string]attr, error) {
	result := make(map[string]attr, len(files))
	if len(files) == 0 {
		return result, nil
	}
	var input bytes.Buffer
	for _, f := range files {
		input.WriteString(f)
		input.WriteByte(0)
	}
	cmd := exec.Command("git", "-C", root, "check-attr", "-z", "--stdin", "text", "eol", "diff")
	cmd.Stdin = &input
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git check-attr: %v", err)
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		a := result[fields[i]]
		switch fields[i+1] {
		case "text":
			a.text = fields[i+2]
		case "eol":
			a.eol = fields[i+2]
		case "diff":
			a.diff = fields[i+2]
		}
		result[fields[i]] = a
	}
	return result, nil
}

func worktreeReader(root string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	}
}

// indexReader reads staged blobs through one `git cat-file --batch`.
type indexReader struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func newIndexReader(root string) (*indexReader, error) {
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &indexReader{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

func (r *indexReader) read(name string) ([]byte, error) {
	if _, err := fmt.Fprintf(r.in, ":%s\n", name); err != nil {
		return nil, err
	}
	header, err := r.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(header)
	if len(parts) == 2 && parts[1] == "missing" {
		return nil, os.ErrNotExist
	}
	if len(parts) != 3 {
		return nil, fmt.Errorf("unexpected cat-file header %q", header)
	}
	size, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, err
	}
	data := make([]byte, size+1) // content plus the trailing newline
	if _, err := io.ReadFull(r.out, data); err != nil {
		return nil, err
	}
	return data[:size], nil
}

func (r *indexReader) close() {
	_ = r.in.Close()
	_ = r.cmd.Wait()
}

// gitZ runs git in root and splits its NUL-terminated output.
func gitZ(root string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\x00") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
