package legacyignore

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBatchRoundTripAndOwnership(t *testing.T) {
	want := Batch{Source: "*.mkv\n!😀.mkv", Paths: []string{"/media/😀.mkv", "C:/media/a.mkv"}}
	data, err := EncodeBatch(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBatch(context.Background(), data)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("round trip", err)
	}
	clear(data)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("decoded batch borrowed mutable frame")
	}
	if strings.Contains(fmt.Sprintf("%v %#v", got, got), "mkv") {
		t.Fatal("batch leaked content")
	}
}

func TestBatchRejectsMalformedFrames(t *testing.T) {
	valid, err := EncodeBatch(context.Background(), Batch{Paths: []string{"/a"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(valid); i++ {
		if _, err := DecodeBatch(context.Background(), valid[:i]); err != ErrBatch {
			t.Fatalf("truncation %d", i)
		}
	}
	cases := [][]byte{append(bytes.Clone(valid), 0), append(bytes.Clone(valid), valid...), bytes.Repeat([]byte{'x'}, MaxBatchBytes+1)}
	for _, offset := range []int{4, 8, 12} {
		data := bytes.Clone(valid)
		binary.LittleEndian.PutUint32(data[offset:], ^uint32(0))
		cases = append(cases, data)
	}
	wrongVersion := bytes.Clone(valid)
	wrongVersion[3] = '2'
	cases = append(cases, wrongVersion)
	for _, data := range cases {
		if _, err := DecodeBatch(context.Background(), data); err != ErrBatch {
			t.Fatal("malformed frame accepted")
		}
	}
	for _, b := range []Batch{
		{}, {Paths: []string{""}}, {Paths: []string{"x\x00y"}}, {Source: string([]byte{0xff}), Paths: []string{"/a"}},
		{Source: strings.Repeat("x", MaxBatchSourceBytes+1), Paths: []string{"/a"}},
		{Paths: []string{strings.Repeat("x", MaxBatchPathBytes+1)}}, {Paths: make([]string, MaxBatchPaths+1)},
	} {
		if _, err := EncodeBatch(context.Background(), b); err != ErrBatch {
			t.Fatal("invalid batch accepted")
		}
	}
}

func TestBatchCancellationAndBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EncodeBatch(ctx, Batch{}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := DecodeBatch(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := EncodeBatch(nil, Batch{}); err != ErrBatch {
		t.Fatal(err)
	}
	if _, err := DecodeBatch(nil, nil); err != ErrBatch {
		t.Fatal(err)
	}
	b := Batch{Source: strings.Repeat("x", MaxBatchSourceBytes), Paths: make([]string, MaxBatchPaths)}
	for i := range b.Paths {
		b.Paths[i] = strings.Repeat("x", MaxBatchPathBytes)
	}
	frame, err := EncodeBatch(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBatch(context.Background(), frame)
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Fatal("maximum valid batch rejected", err)
	}
}

func FuzzBatchDecoder(f *testing.F) {
	seed, _ := EncodeBatch(context.Background(), Batch{Source: "*.mkv", Paths: []string{"/a.mkv"}})
	f.Add(seed)
	f.Add([]byte("JIG1"))
	f.Fuzz(func(t *testing.T, data []byte) {
		batch, err := DecodeBatch(context.Background(), data)
		if err != nil {
			return
		}
		encoded, err := EncodeBatch(context.Background(), batch)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatal("accepted noncanonical frame")
		}
	})
}
