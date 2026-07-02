package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/restorna/platform/pkg/tenancy"
)

// Limiter is the rate-limit port: Allow reports whether a request keyed by `key`
// may proceed now. The default impl is an in-memory token bucket; a Redis-backed
// impl satisfying the same interface can be swapped in for multi-replica deploys.
type Limiter interface {
	Allow(key string) bool
}

// RateLimit wraps next, rejecting (429) requests once the per-key token bucket is
// empty. The key is the caller's user id when authenticated, else the client IP.
type RateLimit struct {
	lim Limiter
}

// NewRateLimit builds the middleware around a Limiter.
func NewRateLimit(lim Limiter) *RateLimit { return &RateLimit{lim: lim} }

// Wrap applies the limit to next.
func (rl *RateLimit) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.lim.Allow(rateKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateKey prefers the authenticated user id (stable across IPs) and falls back to
// the remote IP for unauthenticated/public calls.
func rateKey(r *http.Request) string {
	if s, ok := tenancy.From(r.Context()); ok && s.UserID != "" {
		return "uid:" + s.UserID
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// TokenBucket is a simple in-memory token-bucket Limiter. Each key gets a bucket
// of `burst` tokens refilling at `rate` tokens/second. Safe for concurrent use.
// Good enough for a single replica; for horizontal scale, back this with Redis
// behind the Limiter interface.
type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // max tokens
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket builds a token bucket allowing `burst` requests immediately and
// refilling at `ratePerSec` per second.
func NewTokenBucket(ratePerSec, burst float64) *TokenBucket {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &TokenBucket{
		buckets: map[string]*bucket{},
		rate:    ratePerSec,
		burst:   burst,
		now:     time.Now,
	}
}

// Allow consumes a token for key, refilling first based on elapsed time.
func (tb *TokenBucket) Allow(key string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := tb.now()
	b, ok := tb.buckets[key]
	if !ok {
		tb.buckets[key] = &bucket{tokens: tb.burst - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * tb.rate
	if b.tokens > tb.burst {
		b.tokens = tb.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
