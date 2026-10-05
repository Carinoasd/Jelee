//go:build linux || windows

package ignoresource

import "errors"

// ruleFileNames lists the leaf names of one directory's own rule source in
// precedence order. The first entry is the native file. The other two are the
// protocol-compatible file names of the upstream media servers (owner decision
// E2, G22.2): plain aliases with the same gitignore syntax and semantics, used
// only when the native file is absent from that same directory. These are the
// only legacy brand strings of the ignore subsystem; keep them in this file.
var ruleFileNames = [...]string{".jeleeignore", ".jellyfinignore", ".embyignore"}

// RuleFileNames returns a copy of the accepted rule leaf names in precedence
// order, for documentation, diagnostics and tests in other packages.
func RuleFileNames() []string { return append([]string(nil), ruleFileNames[:]...) }

// OpenRule opens this directory's own rule source: the first present name of
// ruleFileNames. Only a confirmed absence falls through to the next name; an
// unsafe, unreadable or non-regular entry under a higher-precedence name is an
// error and never silently replaced by a lower one. The returned handle's
// identity, size, mtime and digest become the directory's source stamp, so a
// later appearance of a higher-precedence file (or removal of the chosen one)
// changes the stamp and invalidates retained proofs like any other edit.
func (f *nativeFile) OpenRule() (sourceFile, error) {
	for _, name := range ruleFileNames {
		opened, err := f.open(name, false, true)
		if errors.Is(err, errAbsent) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return opened, nil
	}
	return nil, errAbsent
}
