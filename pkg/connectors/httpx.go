package connectors

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"
)

// defaultTimeout bounds every outbound provider call so a hung gateway can't
// stall a request goroutine.
const defaultTimeout = 20 * time.Second

// httpDoer is the minimal surface adapters need from an HTTP client. Tests inject
// an httptest.Server's client (or a stub) through this seam.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newHTTPClient returns a client with a sane timeout for provider calls.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultTimeout}
}

// cfgGet reads key from cfg, returning "" if absent.
func cfgGet(cfg map[string]string, key string) string {
	if cfg == nil {
		return ""
	}
	return cfg[key]
}

// requireCfg returns an error naming every missing required key. It lets Init
// fail fast with an actionable message instead of a downstream 401.
func requireCfg(cfg map[string]string, keys ...string) error {
	var missing []string
	for _, k := range keys {
		if cfgGet(cfg, k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("connectors: missing required config: %v", missing)
	}
	return nil
}

// doJSON performs req and decodes a 2xx JSON body into out (out may be nil to
// discard). Non-2xx responses become an error carrying the status and body, so
// callers surface the provider's error verbatim.
func doJSON(client httpDoer, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("connectors: provider returned %d: %s", resp.StatusCode, string(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("connectors: decode response: %w", err)
		}
	}
	return nil
}

// jsonRequest builds a POST request with a JSON body and application/json header.
func jsonRequest(ctx context.Context, method, url string, payload any) (*http.Request, error) {
	var buf bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// hmacHex computes HMAC-<h>(message, key) and returns lowercase hex. It is the
// signing/verification primitive every provider webhook below shares.
func hmacHex(newHash func() hash.Hash, key, message []byte) string {
	mac := hmac.New(newHash, key)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// hmacSHA256Hex is the common HMAC-SHA256 hex helper (Razorpay, Twilio-style).
func hmacSHA256Hex(secret string, body []byte) string {
	return hmacHex(sha256.New, []byte(secret), body)
}

// hmacSHA512Hex is used by providers that sign with SHA-512 (PhonePe X-VERIFY,
// which appends "###" + keyIndex to the digest — see phonepe.go).
func hmacSHA512Hex(secret string, body []byte) string {
	return hmacHex(sha512.New, []byte(secret), body)
}

// constantTimeEqualHex compares two hex signatures without leaking timing. It
// tolerates case differences by comparing the decoded bytes.
func constantTimeEqualHex(a, b string) bool {
	ab, err1 := hex.DecodeString(a)
	bb, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil {
		// Fall back to constant-time string compare when either side isn't hex.
		return hmac.Equal([]byte(a), []byte(b))
	}
	return hmac.Equal(ab, bb)
}

// header does a case-insensitive lookup over a plain header map (webhook headers
// arrive as a flat map[string]string from connector-hub).
func header(headers map[string]string, name string) string {
	if headers == nil {
		return ""
	}
	if v, ok := headers[name]; ok {
		return v
	}
	for k, v := range headers {
		if http.CanonicalHeaderKey(k) == http.CanonicalHeaderKey(name) {
			return v
		}
	}
	return ""
}
