package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrCrypto is returned for any encryption/decryption failure (bad key, tampered
// ciphertext). The grpc adapter maps it to Internal.
var ErrCrypto = errors.New("crypto failure")

// Envelope is the pure envelope-encryption helper used to protect secret connector
// config at rest. It performs single-layer AES-256-GCM under a Key-Encryption-Key
// (KEK) sourced from the environment (CONNECTOR_KEK). The design is envelope-ready:
// swapping to per-installation data keys wrapped by the KEK is additive and does
// not change the Crypto port. This type is pure (stdlib only) so it lives in the
// domain.
type Envelope struct {
	gcm cipher.AEAD
}

// NewEnvelope builds an Envelope from a 32-byte (AES-256) key. Keys shorter than
// 32 bytes are rejected so a weak/empty KEK cannot slip through.
func NewEnvelope(key []byte) (*Envelope, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: KEK must be exactly 32 bytes (AES-256), got %d", ErrCrypto, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCrypto, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCrypto, err)
	}
	return &Envelope{gcm: gcm}, nil
}

// EncryptMap marshals a secret config map to JSON and seals it with AES-GCM. The
// output is nonce||ciphertext||tag. An empty/nil map returns (nil, nil) so callers
// can persist "no secrets" as NULL.
func (e *Envelope) EncryptMap(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	plain, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal secret config: %v", ErrCrypto, err)
	}
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("%w: nonce: %v", ErrCrypto, err)
	}
	// Seal appends ciphertext+tag to nonce, giving a self-describing blob.
	return e.gcm.Seal(nonce, nonce, plain, nil), nil
}

// DecryptMap opens a blob produced by EncryptMap and unmarshals the secret config
// map. A nil/empty blob yields an empty map. A tampered blob (auth tag mismatch)
// fails with ErrCrypto — this is what makes at-rest tampering detectable.
func (e *Envelope) DecryptMap(blob []byte) (map[string]string, error) {
	if len(blob) == 0 {
		return map[string]string{}, nil
	}
	ns := e.gcm.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("%w: ciphertext too short", ErrCrypto)
	}
	nonce, ct := blob[:ns], blob[ns:]
	plain, err := e.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: open: %v", ErrCrypto, err)
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("%w: unmarshal secret config: %v", ErrCrypto, err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}
