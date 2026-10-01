package legacyignore

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestDecodeSourceRuntimeOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/decode.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Runtime string
		Cases   []struct{ Name, RawHex, UTF8Hex string }
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Runtime != "10.0.11" || len(fixture.Cases) != 47 {
		t.Fatal("unexpected runtime oracle")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input, e := hex.DecodeString(c.RawHex)
			if e != nil {
				t.Fatal(e)
			}
			want, e := hex.DecodeString(c.UTF8Hex)
			if e != nil {
				t.Fatal(e)
			}
			got, e := DecodeSource(context.Background(), input)
			if e != nil || !bytes.Equal([]byte(got), want) {
				t.Fatalf("runtime differs: got %x want %x err=%v", got, want, e)
			}
		})
	}
}

func TestDecodeSourceLimitsAndCancellation(t *testing.T) {
	for _, raw := range [][]byte{bytes.Repeat([]byte{'a'}, MaxRawSourceBytes+1), bytes.Repeat([]byte{0xff}, MaxRawSourceBytes)} {
		if text, err := DecodeSource(context.Background(), raw); err != ErrBatch || text != "" {
			t.Fatal("limit returned partial text", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if text, err := DecodeSource(ctx, []byte("text")); err != context.Canceled || text != "" {
		t.Fatal("cancellation", err)
	}
}
