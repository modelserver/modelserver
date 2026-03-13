package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestBase62RoundTrip(t *testing.T) {
	data := make([]byte, 36)
	for i := range data {
		data[i] = byte(i)
	}
	encoded := Base62Encode(data, 49)
	if len(encoded) != 49 {
		t.Fatalf("expected length 49, got %d", len(encoded))
	}
	decoded, err := Base62Decode(encoded, 36)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !bytes.Equal(data, decoded) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestBase62AllZeros(t *testing.T) {
	data := make([]byte, 36)
	encoded := Base62Encode(data, 49)
	if len(encoded) != 49 {
		t.Fatalf("expected length 49, got %d", len(encoded))
	}
	for _, c := range encoded {
		if c != '0' {
			t.Fatalf("expected all zeros, got %q", encoded)
		}
	}
	decoded, err := Base62Decode(encoded, 36)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !bytes.Equal(data, decoded) {
		t.Fatalf("round-trip mismatch for zeros")
	}
}

func TestBase62RandomData(t *testing.T) {
	for i := 0; i < 100; i++ {
		data := make([]byte, 36)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		encoded := Base62Encode(data, 49)
		if len(encoded) != 49 {
			t.Fatalf("iteration %d: expected length 49, got %d", i, len(encoded))
		}
		// Verify all characters are base62.
		for _, c := range encoded {
			if base62Index(byte(c)) < 0 {
				t.Fatalf("iteration %d: invalid char %c in %q", i, c, encoded)
			}
		}
		decoded, err := Base62Decode(encoded, 36)
		if err != nil {
			t.Fatalf("iteration %d: decode error: %v", i, err)
		}
		if !bytes.Equal(data, decoded) {
			t.Fatalf("iteration %d: round-trip mismatch", i)
		}
	}
}

func TestBase62DecodeInvalidChar(t *testing.T) {
	_, err := Base62Decode("abc-def", 4)
	if err == nil {
		t.Fatal("expected error for invalid character")
	}
}

func TestBase62DecodeTooLong(t *testing.T) {
	// Encode max value that fits in 1 byte but use fixedLen=3 for the encoded string.
	_, err := Base62Decode("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", 36)
	if err == nil {
		t.Fatal("expected error for oversized decode")
	}
}
