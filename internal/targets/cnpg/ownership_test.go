package cnpg

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewOwnershipIDReturns128BitHexValue(t *testing.T) {
	value, err := NewOwnershipID()
	if err != nil {
		t.Fatalf("create ownership id: %v", err)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != ownershipIDBytes {
		t.Fatalf("invalid ownership id %q: bytes=%d err=%v", value, len(decoded), err)
	}
}

func TestNewOwnershipIDUsesFullReaderAndReportsShortInput(t *testing.T) {
	input := []byte("0123456789abcdef")
	value, err := newOwnershipID(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("create deterministic ownership id: %v", err)
	}
	if want := hex.EncodeToString(input); value != want {
		t.Fatalf("ownership id = %q, want %q", value, want)
	}

	if _, err := newOwnershipID(strings.NewReader("short")); err == nil || !strings.Contains(err.Error(), "read random ownership id") {
		t.Fatalf("expected short entropy error, got %v", err)
	}
}
