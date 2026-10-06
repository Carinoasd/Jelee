// Command go is the Jelee Go API example. It signs in, lists the libraries
// the account may see, lists items of the first library and prints the
// details of the first item. Configuration comes from the environment only:
//
//	JELEE_URL       server root, for example http://127.0.0.1:8097
//	JELEE_USER      account name
//	JELEE_PASSWORD  account password (never pass it as an argument)
//
// Run it from the repository root with `go run ./examples/go`.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/MoYuanCN/Jelee/examples/go/walkthrough"
)

func main() { os.Exit(run()) }

func run() int {
	cfg := walkthrough.Config{BaseURL: os.Getenv("JELEE_URL"), Name: os.Getenv("JELEE_USER"), Password: os.Getenv("JELEE_PASSWORD")}
	if cfg.BaseURL == "" || cfg.Name == "" || cfg.Password == "" {
		fmt.Fprintln(os.Stderr, "set JELEE_URL, JELEE_USER and JELEE_PASSWORD")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := walkthrough.Run(ctx, cfg, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
