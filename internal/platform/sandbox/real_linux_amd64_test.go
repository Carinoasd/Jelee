package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealFFprobeInSandboxWithExplicitDeveloperProfile(t *testing.T) {
	path := os.Getenv("JELEE_SANDBOX_REAL_PROFILE")
	if path == "" {
		t.Skip("real ffprobe requires an explicit developer test profile; no host libraries are automatically trusted")
	}
	requireNative(t)
	var fixture struct {
		Profile Profile
		Policy  Policy
		Inputs  []string
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &fixture) != nil || len(fixture.Inputs) == 0 {
		t.Fatal("invalid explicit real-tool fixture profile")
	}
	launcher, err := New(context.Background(), fixture.Profile, fixture.Policy)
	if err != nil {
		t.Fatalf("real executable/dependency verification failed: %v", err)
	}
	for _, missing := range []string{"ld-linux-x86-64.so.2", "libm.so.6"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			policy := fixture.Policy
			policy.Libraries = nil
			for _, library := range fixture.Policy.Libraries {
				if filepath.Base(library.Path) != missing {
					policy.Libraries = append(policy.Libraries, library)
				}
			}
			if len(policy.Libraries) == len(fixture.Policy.Libraries) {
				t.Fatal("missing dependency fixture not exercised")
			}
			if _, err := New(context.Background(), fixture.Profile, policy); err != ErrUnavailable {
				t.Fatal("incomplete interpreter/dependency closure accepted")
			}
		})
	}
	t.Run("duplicate_soname", func(t *testing.T) {
		library := fixture.Policy.Libraries[len(fixture.Policy.Libraries)-1]
		duplicate := filepath.Join(t.TempDir(), "duplicate-library")
		copyFixture(t, library.Path, duplicate)
		policy := fixture.Policy
		policy.RequireProtectedFiles = false // Test duplicate sonames independently of ownership.
		policy.Libraries = append(append([]PinnedFile(nil), policy.Libraries...), PinnedFile{Path: duplicate, SHA256: library.SHA256})
		if _, err := New(context.Background(), fixture.Profile, policy); err != ErrUnavailable {
			t.Fatal("duplicate library soname accepted")
		}
	})
	helper := fixtureHelper(t)
	policy, _ := json.Marshal(fixture.Policy)
	run := func(t *testing.T, source string) ([]byte, error) {
		t.Helper()
		before := fileDigest(t, source)
		file, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, helper, "--test-helper", launcher.HelperArguments()[1])
		command.Env = []string{"JELEE_TEST_SANDBOX_POLICY=" + string(policy)}
		command.Stdin = file
		command.Dir = t.TempDir()
		output, err := command.CombinedOutput()
		if fileDigest(t, source) != before {
			t.Fatal("probe changed source bytes")
		}
		return output, err
	}
	for index, source := range fixture.Inputs {
		t.Run(filepath.Base(source), func(t *testing.T) {
			output, err := run(t, source)
			if err != nil {
				t.Fatalf("real probe failed for fixture %d: %v; %s", index, err, output)
			}
			var metadata struct {
				Streams []json.RawMessage `json:"streams"`
				Format  map[string]any    `json:"format"`
			}
			if json.Unmarshal(output, &metadata) != nil || len(metadata.Streams) == 0 || metadata.Format["filename"] != "fd:" {
				t.Fatal("real ffprobe did not return streams from fd input")
			}
		})
	}
	var calls atomic.Int64
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(418) })}
	defer server.Close()
	go server.Serve(listener)
	root := t.TempDir()
	for name, text := range map[string]string{
		"remote.m3u8":   "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nhttp://" + listener.Addr().String() + "/private\n#EXT-X-ENDLIST\n",
		"local.concat":  "ffconcat version 1.0\nfile 'file:/etc/passwd'\n",
		"nested.concat": "ffconcat version 1.0\nfile 'concat:crypto:file:/etc/passwd'\n",
	} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(root, name)
			if os.WriteFile(source, []byte(text), 0600) != nil {
				t.Fatal("create reference fixture")
			}
			if _, err := run(t, source); err == nil {
				t.Fatal("reference-based demuxer was accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("real ffprobe contacted the loopback reference")
	}
}
