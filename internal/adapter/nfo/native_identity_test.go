package nfo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestNFONativeIdentityCodec(t *testing.T) {
	for _, platform := range []byte{1, 2} {
		data := make([]byte, 48)
		data[0], data[1], data[2], data[16] = 1, platform, 1, 7
		if platform == 1 {
			// All 128 file-ID bits must survive; truncating to 64 bits is invalid.
			data[31] = 0x80
		} else {
			binary.LittleEndian.PutUint32(data[40:44], 999_999_999)
		}
		identity, err := parseNFONativeIdentity(data)
		if err != nil || !bytes.Equal(identity.record[:], data) {
			t.Fatal("native identity did not round trip")
		}
		data[16]++
		if identity.record[16] != 7 {
			t.Fatal("identity aliases mutable serialized bytes")
		}
		for _, display := range []string{fmt.Sprint(identity), fmt.Sprintf("%#v", identity)} {
			if display != "nfo native identity (redacted)" {
				t.Fatal("native identity formatting exposed data")
			}
		}
		for _, index := range []int{0, 1, 2, 3, 7, 44, 47} {
			bad := append([]byte(nil), identity.record[:]...)
			bad[index] = 255
			if _, err := parseNFONativeIdentity(bad); err != errNativeIdentity {
				t.Fatal("invalid identity accepted", index)
			}
		}
	}
	for _, size := range []int{0, 47, 49, 1024} {
		if _, err := parseNFONativeIdentity(make([]byte, size)); err != errNativeIdentity {
			t.Fatal("unbounded identity length accepted")
		}
	}
	for _, platform := range []byte{1, 2} {
		bad := make([]byte, 48)
		bad[0], bad[1], bad[2] = 1, platform, 1
		binary.LittleEndian.PutUint32(bad[40:44], 1_000_000_000)
		if _, err := parseNFONativeIdentity(bad); err != errNativeIdentity {
			t.Fatal("invalid native timestamp encoding accepted")
		}
	}
	bad := make([]byte, 48)
	bad[0], bad[1], bad[2], bad[24] = 1, 2, 1, 1
	if _, err := parseNFONativeIdentity(bad); err != errNativeIdentity {
		t.Fatal("noncanonical Linux file ID accepted")
	}
}
