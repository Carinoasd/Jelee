package auditarchive

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// maxLine bounds one record; audit states are capped at 64 KiB each.
const maxLine = 1 << 20

// Result is the verification of one file. Err is empty when the file is whole.
type Result struct {
	File
	Rows int    `json:"rows"`
	Err  string `json:"error,omitempty"`
}

// VerifyDir checks every archive file in dir (read only): the gzip stream must
// be complete and its checksum valid, every line one audit record whose ID
// rises, whose category and day match the file name, and whose first and last
// IDs are the name's range.
func VerifyDir(dir string) ([]Result, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	files, err := list(root)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(files))
	for _, f := range files {
		r := verifyFile(root, f.Name)
		r.File = f
		results = append(results, r)
	}
	return results, nil
}

func verifyFile(root *os.Root, name string) Result {
	var res Result
	want, ok := parseName(name)
	if !ok {
		res.Err = "unrecognized file name"
		return res
	}
	file, err := root.Open(name)
	if err != nil {
		res.Err = "cannot open file"
		return res
	}
	defer func() { _ = file.Close() }()
	zr, err := gzip.NewReader(file)
	if err != nil {
		res.Err = "not a gzip stream"
		return res
	}
	reader := bufio.NewReaderSize(zr, 64<<10)
	var first, last int64
	for {
		line, err := readLine(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Err = "gzip stream is truncated or corrupt, or a line is too long"
			return res
		}
		var rec domain.AuditRecord
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil || dec.More() || rec.ID < 1 {
			res.Err = "line is not an audit record"
			return res
		}
		if rec.Category != want.Category || rec.OccurredAt.UTC().Format("20060102") != want.Day || rec.ID <= last {
			res.Err = "record does not match the file name or is out of order"
			return res
		}
		if res.Rows == 0 {
			first = rec.ID
		}
		last = rec.ID
		res.Rows++
	}
	if res.Rows == 0 || first != want.FirstID || last != want.LastID {
		res.Err = "row range does not match the file name"
	}
	return res
}

// readLine returns one newline-terminated line without the newline. A final
// line without a newline is an error because every record is terminated.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, io.ErrShortBuffer
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(line, []byte("\n")), nil
		case err == bufio.ErrBufferFull:
			continue
		case err == io.EOF && len(line) == 0:
			return nil, io.EOF
		case err == io.EOF:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
