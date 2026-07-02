package cache

import (
	"testing"
	"time"

	"github.com/restorna/platform/services/settings/internal/domain"
)

func sv(raw string) domain.SettingValue {
	return domain.SettingValue{Key: "billing.gst_pct", Value: domain.Value{Type: domain.TypeInt, Raw: raw}, SourceScope: domain.ScopeOwner}
}

func TestGetSetHit(t *testing.T) {
	c := New(time.Minute)
	if _, ok := c.Get("own_1|||k"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("own_1|||k", sv("5"))
	got, ok := c.Get("own_1|||k")
	if !ok || got.Value.Raw != "5" {
		t.Fatalf("expected hit 5, got %+v ok=%v", got, ok)
	}
}

func TestExpiry(t *testing.T) {
	c := New(time.Hour)
	base := time.Now()
	c.now = func() time.Time { return base }
	c.Set("own_1|||k", sv("5"))

	// Advance past the TTL: the entry must be evicted on read.
	c.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, ok := c.Get("own_1|||k"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestInvalidateOwnerDropsOnlyThatOwner(t *testing.T) {
	c := New(time.Minute)
	c.Set("own_1|||a", sv("1"))
	c.Set("own_1|brnd_1||b", sv("2"))
	c.Set("own_2|||c", sv("3"))

	c.InvalidateOwner("own_1")

	if _, ok := c.Get("own_1|||a"); ok {
		t.Error("own_1 entry a should be gone")
	}
	if _, ok := c.Get("own_1|brnd_1||b"); ok {
		t.Error("own_1 entry b should be gone")
	}
	if _, ok := c.Get("own_2|||c"); !ok {
		t.Error("own_2 entry must survive")
	}
}
