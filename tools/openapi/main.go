// openapi writes the OpenAPI 3.1 document served at /api/v1/openapi.json for
// the reference rollout (every optional feature enabled) to api/openapi.json.
// Run it from the repository root; -check only reports whether the committed
// file is stale.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
)

const defaultOutput = "api/openapi.json"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	flags := flag.NewFlagSet("openapi", flag.ContinueOnError)
	output := flags.String("o", defaultOutput, "output path relative to the repository root")
	check := flags.Bool("check", false, "fail instead of writing when the output is missing or stale")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	want, err := render()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot render OpenAPI specification:", err)
		return 2
	}
	if *check {
		got, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(got, want) {
			fmt.Fprintf(os.Stderr, "%s is stale; run `go run ./tools/openapi` (make openapi) and commit the result\n", *output)
			return 1
		}
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output directory:", err)
		return 2
	}
	if err := os.WriteFile(*output, want, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write specification:", err)
		return 2
	}
	return 0
}

func render() ([]byte, error) {
	cfg := httpapi.ReferenceConfig()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return httpapi.MarshalSpecification(cfg)
}
