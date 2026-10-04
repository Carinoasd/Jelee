package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestVariantProberReadsTheLiveGeneration(t *testing.T) {
	root := t.TempDir()
	source, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
	write := func(generation string) {
		directory := filepath.Join(root, "variants", generation, source)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, key), []byte("v"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := NewVariantProber(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = p.VariantExists(ctx, source+"/"+key); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("store without variants answered", err)
	}
	write("0000000000000001")
	if ok, err := p.VariantExists(ctx, source+"/"+key); err != nil || !ok {
		t.Fatalf("live variant: %t %v", ok, err)
	}
	// A clear moves to a newer, empty generation: the old file is stale.
	if err = os.MkdirAll(filepath.Join(root, "variants", "0000000000000002"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.VariantExists(ctx, source+"/"+key); err != nil || ok {
		t.Fatalf("stale generation counted: %t %v", ok, err)
	}
	write("0000000000000002")
	if ok, err := p.VariantExists(ctx, source+"/"+key); err != nil || !ok {
		t.Fatalf("new generation: %t %v", ok, err)
	}
	for _, object := range []string{"", source, source + "/" + key[:63], "../" + source + "/" + key, strings.ToUpper(source) + "/" + key} {
		if _, err := p.VariantExists(ctx, object); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("object %q accepted", object)
		}
	}
	if _, err := p.VariantExists(nil, source+"/"+key); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.VariantExists(cancelled, source+"/"+key); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled probe ran", err)
	}
	if _, err := NewVariantProber("relative"); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("relative store accepted")
	}
	missing, err := NewVariantProber(filepath.Join(root, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.VariantExists(ctx, source+"/"+key); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("missing store answered", err)
	}
	var none *VariantProber
	if _, err := none.VariantExists(ctx, source+"/"+key); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil prober answered")
	}
}
