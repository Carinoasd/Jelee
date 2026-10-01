package ignoresource

// directoryEntry metadata is obtained relative to the enumerated handle.
// Special files are reported as nodeOther and are never opened for content.
type directoryEntry struct {
	name  string
	state fileState
}

// Keep this capability separate from the rule-reading port: callers cannot
// substitute a pathname reader for a handle-bound native enumerator.
type scanDirectory interface {
	directory
	ReadEntries() ([]directoryEntry, error)
	OpenDirectoryOrAbsent(string) (directory, bool, error)
}

const directoryPageSize = 128
