// Package blob is a cloud-agnostic BlobStore implementation for branding assets.
// It writes bytes to a backing "store" and returns a domain.Asset with a public
// URL. The default store is local-filesystem; swapping to S3/GCS/MinIO is a matter
// of implementing the small `objectStore` interface — no AWS SDK leaks into the
// domain, satisfying the cloud-agnostic rule (ARCHITECTURE.md §10).
package blob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/tenant/internal/domain"
	"github.com/restorna/platform/services/tenant/internal/ports"
)

// objectStore is the minimal object-put contract. A filesystem impl ships here; an
// S3/GCS impl can satisfy the same interface without touching the domain or app.
type objectStore interface {
	// put writes data under key and returns the object's public URL.
	put(ctx context.Context, key string, data []byte, contentType string) (url string, err error)
}

// Store implements ports.BlobStore over any objectStore backend.
type Store struct {
	backend objectStore
}

var _ ports.BlobStore = (*Store)(nil)

// Put stores bytes and returns an Asset reference. The key is a type-prefixed ULID
// plus the content-type's extension; the URL is whatever the backend exposes.
func (s *Store) Put(ctx context.Context, data []byte, contentType string) (domain.Asset, error) {
	if len(data) == 0 {
		return domain.Asset{}, fmt.Errorf("%w: empty asset", domain.ErrInvalid)
	}
	id := ids.New(domain.PrefixAsset)
	key := id + extFor(contentType)
	url, err := s.backend.put(ctx, key, data, contentType)
	if err != nil {
		return domain.Asset{}, err
	}
	return domain.Asset{ID: id, URL: url, ContentType: contentType}, nil
}

// NewFilesystem returns a Store backed by a local directory. publicBaseURL is the
// URL prefix the files are served under (e.g. a CDN or static host). Used for local
// dev and as the reference impl; production wires an S3-backed store of the same
// shape.
func NewFilesystem(rootDir, publicBaseURL string) (*Store, error) {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	return &Store{backend: &fsBackend{root: rootDir, baseURL: strings.TrimRight(publicBaseURL, "/")}}, nil
}

type fsBackend struct {
	root    string
	baseURL string
}

func (b *fsBackend) put(_ context.Context, key string, data []byte, _ string) (string, error) {
	dst := filepath.Join(b.root, key)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("write asset: %w", err)
	}
	base := b.baseURL
	if base == "" {
		base = "file://" + b.root
	}
	return base + "/" + key, nil
}

func extFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}
