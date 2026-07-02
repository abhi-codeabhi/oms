package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
)

func mustKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := mustKeys(t)
	in := Claims{
		UserID:     "usr_123",
		Role:       commonv1.Role_ROLE_OWNER,
		Owner:      "own_1",
		Brand:      "brnd_1",
		Restaurant: "out_1",
	}
	tok, err := Sign(priv, in, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	out, err := Verify(pub, tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.UserID != in.UserID || out.Role != in.Role || out.Owner != in.Owner ||
		out.Brand != in.Brand || out.Restaurant != in.Restaurant {
		t.Fatalf("claims mismatch: got %+v want %+v", out, in)
	}
}

func TestVerifyTamperFails(t *testing.T) {
	pub, priv := mustKeys(t)
	tok, err := Sign(priv, Claims{UserID: "usr_1", Role: commonv1.Role_ROLE_MANAGER}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Flip a character in the signature segment.
	tampered := tok[:len(tok)-2] + flip(tok[len(tok)-2:])
	if _, err := Verify(pub, tampered); err == nil {
		t.Fatal("Verify accepted tampered token, want error")
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	_, priv := mustKeys(t)
	otherPub, _ := mustKeys(t)
	tok, _ := Sign(priv, Claims{UserID: "usr_1"}, time.Hour)
	if _, err := Verify(otherPub, tok); err == nil {
		t.Fatal("Verify accepted token signed by other key, want error")
	}
}

func TestVerifyExpiredFails(t *testing.T) {
	pub, priv := mustKeys(t)
	tok, _ := Sign(priv, Claims{UserID: "usr_1"}, -time.Minute)
	if _, err := Verify(pub, tok); err == nil {
		t.Fatal("Verify accepted expired token, want error")
	}
}

func flip(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
	}
	return string(b)
}
