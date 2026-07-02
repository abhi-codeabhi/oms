// Package crypto is the ports.Crypto adapter: envelope encryption of secret
// connector config backed by a Key-Encryption-Key (KEK) from the environment
// (CONNECTOR_KEK). It delegates the actual AES-256-GCM to domain.Envelope (pure),
// keeping key sourcing / decoding at the adapter edge.
package crypto

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// Crypto implements ports.Crypto over a domain.Envelope.
type Crypto struct {
	env *domain.Envelope
}

var _ ports.Crypto = (*Crypto)(nil)

// New builds a Crypto from a raw 32-byte key.
func New(key []byte) (*Crypto, error) {
	env, err := domain.NewEnvelope(key)
	if err != nil {
		return nil, err
	}
	return &Crypto{env: env}, nil
}

// FromKEK decodes the CONNECTOR_KEK env value into a 32-byte AES-256 key. It
// accepts three encodings so operators can supply whatever their secret store
// emits: base64 (std), hex, or a raw 32-char string. An empty/short KEK is an
// error — the service refuses to start rather than encrypt with a weak key.
func FromKEK(kek string) (*Crypto, error) {
	key, err := decodeKEK(kek)
	if err != nil {
		return nil, err
	}
	return New(key)
}

func decodeKEK(kek string) ([]byte, error) {
	if kek == "" {
		return nil, fmt.Errorf("%w: CONNECTOR_KEK is required", domain.ErrCrypto)
	}
	// base64 (44 chars for 32 bytes, standard padding).
	if b, err := base64.StdEncoding.DecodeString(kek); err == nil && len(b) == 32 {
		return b, nil
	}
	// hex (64 chars).
	if b, err := hex.DecodeString(kek); err == nil && len(b) == 32 {
		return b, nil
	}
	// raw bytes.
	if len(kek) == 32 {
		return []byte(kek), nil
	}
	return nil, fmt.Errorf("%w: CONNECTOR_KEK must decode to 32 bytes (base64/hex/raw)", domain.ErrCrypto)
}

// EncryptMap implements ports.Crypto.
func (c *Crypto) EncryptMap(m map[string]string) ([]byte, error) { return c.env.EncryptMap(m) }

// DecryptMap implements ports.Crypto.
func (c *Crypto) DecryptMap(blob []byte) (map[string]string, error) { return c.env.DecryptMap(blob) }
