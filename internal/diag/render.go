package diag

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// MarshalReport returns indented JSON.
func MarshalReport(report Report) ([]byte, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// RenderTable writes a human-readable report: one row per finding, followed
// by the remediation for every non-ok finding.
func RenderTable(w io.Writer, report Report) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSTATUS\tCODE\tSUBJECT\tMESSAGE")
	for _, r := range report.Results {
		for _, f := range r.Findings {
			message := f.Message
			if f.Detail != "" {
				message += ": " + f.Detail
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Check, f.Status, f.Code, dash(f.Subject), message)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fixes := false
	for _, r := range report.Results {
		for _, f := range r.Findings {
			if f.Status == StatusOK || f.Fix == "" {
				continue
			}
			if !fixes {
				fmt.Fprintln(w, "\nSuggested fixes (details: docs/troubleshooting.md):")
				fixes = true
			}
			fmt.Fprintf(w, "  [%s] %s: %s\n", f.Status, f.Code, f.Fix)
		}
	}
	_, err := fmt.Fprintf(w, "\nResult: %s (%d ok, %d warn, %d fail)\n", report.Status, report.Summary.OK, report.Summary.Warn, report.Summary.Fail)
	return err
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
