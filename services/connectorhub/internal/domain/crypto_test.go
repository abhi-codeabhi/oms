package domain

import (
	"errors"
	"testing"
)

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 7)
	}
	return k
}

func TestEnvelope_RoundTrip(t *testing.T) {
	env, err := NewEnvelope(testKey())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   map[string]string
	}{
		{name: "empty -> nil ciphertext", in: map[string]string{}},
		{name: "single secret", in: map[string]string{"key_secret": "s3cr3t"}},
		{name: "multiple secrets", in: map[string]string{"a": "1", "b": "two", "c": "𝔲𝔫𝔦𝔠𝔬𝔡𝔢"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := env.EncryptMap(tc.in)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if len(tc.in) == 0 {
				if ct != nil {
					t.Fatal("empty map must encrypt to nil")
				}
				return
			}
			got, err := env.DecryptMap(ct)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if len(got) != len(tc.in) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.in))
			}
			for k, v := range tc.in {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestEnvelope_TamperDetected(t *testing.T) {
	env, _ := NewEnvelope(testKey())
	ct, err := env.EncryptMap(map[string]string{"key_secret": "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the ciphertext body (after the nonce): GCM auth tag must reject.
	ct[len(ct)-1] ^= 0xFF
	if _, err := env.DecryptMap(ct); !errors.Is(err, ErrCrypto) {
		t.Fatalf("tampered decrypt err = %v, want ErrCrypto", err)
	}
}

func TestEnvelope_WrongKeyFails(t *testing.T) {
	a, _ := NewEnvelope(testKey())
	other := make([]byte, 32) // all-zero key
	b, _ := NewEnvelope(other)
	ct, _ := a.EncryptMap(map[string]string{"x": "y"})
	if _, err := b.DecryptMap(ct); !errors.Is(err, ErrCrypto) {
		t.Fatalf("cross-key decrypt err = %v, want ErrCrypto", err)
	}
}

func TestNewEnvelope_RejectsShortKey(t *testing.T) {
	if _, err := NewEnvelope([]byte("too-short")); !errors.Is(err, ErrCrypto) {
		t.Fatalf("err = %v, want ErrCrypto", err)
	}
}
