package main

import (
	"os"
	"testing"
)

func TestFixtureRefusesPublicDestinationWithoutMutation(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "jelee-fixture-contract-")
	if err != nil {
		t.Fatal("create fixture directory")
	}
	t.Cleanup(func() {
		if os.Remove(directory) != nil {
			t.Error("remove empty owned fixture directory")
		}
	})
	if os.Chmod(directory, 0755) != nil {
		t.Fatal("set public fixture permissions")
	}
	if _, err := generate(directory, 1000); err == nil {
		t.Fatal("public destination must be rejected")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected directory was modified")
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("rejected directory permissions were modified")
	}
}
