package clients

import "encoding/base64"

// base64Encode std-base64 encodes the bytes for a data: URI.
func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
