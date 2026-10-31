package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/MoYuanCN/Jelee/internal/platform/auditarchive"
)

// auditUsage describes the offline audit archive commands (G36.3). They read
// the archive directory only and need neither the database nor a token.
const auditUsage = `usage: jelee-cli audit archive list [--dir DIR] [--json]
       jelee-cli audit archive verify [--dir DIR] [--json]
DIR defaults to JELEE_AUDIT_ARCHIVE_DIR. verify exits 3 when a file is damaged.`

// Exit code of "audit archive verify" when at least one file is damaged.
const auditArchiveDamagedExit = 3

func runAuditCLI(argv []string, stdout, stderr io.Writer) int {
	return runAuditCLIWith(argv, stdout, stderr, os.LookupEnv)
}

func runAuditCLIWith(argv []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) int {
	if len(argv) < 2 || argv[0] != "archive" || (argv[1] != "list" && argv[1] != "verify") {
		emit(stderr, "%s\n", auditUsage)
		return 2
	}
	command := argv[1]
	flags := flag.NewFlagSet("audit archive "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "archive directory")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(argv[2:]); err != nil || flags.NArg() != 0 {
		emit(stderr, "%s\n", auditUsage)
		return 2
	}
	if *dir == "" {
		if value, ok := lookup("JELEE_AUDIT_ARCHIVE_DIR"); ok {
			*dir = value
		}
	}
	if *dir == "" {
		emit(stderr, "%s\n", "archive directory: pass --dir or set JELEE_AUDIT_ARCHIVE_DIR")
		return 2
	}
	if command == "list" {
		files, err := auditarchive.ListDir(*dir)
		if err != nil {
			emit(stderr, "%s\n", "archive directory: cannot read")
			return 1
		}
		if *asJSON {
			return encodeAuditJSON(stdout, stderr, files)
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		emit(w, "%s\n", "FILE\tCATEGORY\tDAY\tFIRST ID\tLAST ID\tBYTES")
		for _, f := range files {
			emit(w, "%s\t%s\t%s\t%d\t%d\t%d\n", f.Name, f.Category, f.Day, f.FirstID, f.LastID, f.Bytes)
		}
		_ = w.Flush()
		return 0
	}
	results, err := auditarchive.VerifyDir(*dir)
	if err != nil {
		emit(stderr, "%s\n", "archive directory: cannot read")
		return 1
	}
	damaged := 0
	for _, r := range results {
		if r.Err != "" {
			damaged++
		}
	}
	if *asJSON {
		if code := encodeAuditJSON(stdout, stderr, results); code != 0 {
			return code
		}
	} else {
		rows := 0
		for _, r := range results {
			status := "ok"
			if r.Err != "" {
				status = "DAMAGED: " + r.Err
			}
			rows += r.Rows
			emit(stdout, "%s\t%d rows\t%s\n", r.Name, r.Rows, status)
		}
		emit(stdout, "%d files, %d rows, %d damaged\n", len(results), rows, damaged)
	}
	if damaged > 0 {
		return auditArchiveDamagedExit
	}
	return 0
}

func encodeAuditJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		emit(stderr, "%s\n", "cannot write output")
		return 1
	}
	return 0
}

// emit writes a diagnostic line; a closed output has nowhere to report to.
func emit(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
