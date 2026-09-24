package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func mkReq(method, path string) *http.Request {
	return httptest.NewRequest(method, path, nil)
}

func startLimiter(t *testing.T, lim *RateLimiter) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lim.Start(ctx)
	return func() {
		cancel()
		lim.Stop()
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		trustXFF   bool
		want       string
	}{
		{name: "trusted honors XFF", remoteAddr: "203.0.113.9:5432", xff: "198.51.100.7, 10.0.0.1", trustXFF: true, want: "198.51.100.7"},
		{name: "untrusted ignores XFF", remoteAddr: "203.0.113.9:5432", xff: "198.51.100.7, 10.0.0.1", want: "203.0.113.9"},
		{name: "trusted falls back to remote without XFF", remoteAddr: "203.0.113.9:5432", trustXFF: true, want: "203.0.113.9"},
		{name: "trusted skips invalid XFF parts", remoteAddr: "203.0.113.9:5432", xff: "not-an-ip, 198.51.100.7", trustXFF: true, want: "198.51.100.7"},
		{name: "bare IP remote without port", remoteAddr: "203.0.113.9", trustXFF: true, want: "203.0.113.9"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r, c.trustXFF); got != c.want {
				t.Fatalf("clientIP() = %q, want %q (trustXFF=%v, XFF=%q)", got, c.want, c.trustXFF, c.xff)
			}
		})
	}
}

// Table test for the rate-limit bucket key (issue #766). Two distinct
// callers sharing a key means one can exhaust the other's allowance, so the
// key must separate callers and stay stable per caller.
func TestClientKey(t *testing.T) {
	l := NewRateLimiter(1, 1, false)

	keyFor := func(mutate func(r *http.Request)) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		if mutate != nil {
			mutate(r)
		}
		return l.clientKey(r)
	}

	t.Run("two different clients produce different keys", func(t *testing.T) {
		a := keyFor(func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1111" })
		b := keyFor(func(r *http.Request) { r.RemoteAddr = "198.51.100.7:2222" })
		assert.Equal(t, "203.0.113.9", a)
		assert.Equal(t, "198.51.100.7", b)
		assert.NotEqual(t, a, b)
	})

	t.Run("the same client produces a stable key across requests", func(t *testing.T) {
		// Only the ephemeral port differs between requests.
		a := keyFor(func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1111" })
		b := keyFor(func(r *http.Request) { r.RemoteAddr = "203.0.113.9:9999" })
		assert.Equal(t, a, b)
	})

	t.Run("credential-bearing caller keys by identity rather than address", func(t *testing.T) {
		sameCallerDifferentAddrs := func(header, headerValue, wantCredential string) {
			a := keyFor(func(r *http.Request) {
				r.Header.Set(header, headerValue)
				r.RemoteAddr = "203.0.113.9:1111"
			})
			b := keyFor(func(r *http.Request) {
				r.Header.Set(header, headerValue)
				r.RemoteAddr = "198.51.100.7:2222"
			})
			assert.Equal(t, "api:"+wantCredential, a)
			assert.Equal(t, a, b, "same credential from different addresses must share one bucket")
		}
		sameCallerDifferentAddrs("X-API-Key", "st_abc123_secret", "st_abc123_secret")
		sameCallerDifferentAddrs("Authorization", "Bearer st_abc123_secret", "st_abc123_secret")

		// Different credentials from one address must not collide.
		a := keyFor(func(r *http.Request) { r.Header.Set("X-API-Key", "key-one") })
		b := keyFor(func(r *http.Request) { r.Header.Set("X-API-Key", "key-two") })
		assert.NotEqual(t, a, b)

		// Bearer wins when both headers are present.
		both := keyFor(func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer bearer-key")
			r.Header.Set("X-API-Key", "xapi-key")
		})
		assert.Equal(t, "api:bearer-key", both)
	})

	t.Run("anonymous caller falls back to the documented key", func(t *testing.T) {
		// RemoteAddr host, port stripped.
		assert.Equal(t, "203.0.113.9", keyFor(nil))
		// No usable address at all: the documented "unknown" bucket.
		assert.Equal(t, "unknown", keyFor(func(r *http.Request) { r.RemoteAddr = "" }))
		assert.Equal(t, "unknown", keyFor(func(r *http.Request) { r.RemoteAddr = "not-an-address" }))
	})

	t.Run("the key contains no unbounded caller-supplied text", func(t *testing.T) {
		// A spoofed or malformed X-Forwarded-For must never land in the
		// key: unparseable parts are skipped, and the key stays the parsed
		// source address.
		r := keyFor(func(r *http.Request) {
			r.Header.Set("X-Forwarded-For", "not-an-ip, definitely-not-an-ip-either")
		})
		assert.Equal(t, "203.0.113.9", r)
	})
}

func TestCeilSeconds(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{0, time.Second},
		{1, time.Second},
		{999 * time.Millisecond, time.Second},
		{time.Second, time.Second},
		{time.Second + time.Millisecond, 2 * time.Second},
		{2 * time.Second, 2 * time.Second},
	}
	for _, c := range cases {
		if got := ceilSeconds(c.in); got != c.want {
			t.Fatalf("ceilSeconds(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestBucketEntryForReturnsSameInstance(t *testing.T) {
	l := NewRateLimiter(1, 1, false)
	e1 := l.bucketEntryFor("k1", 1, 1)
	if e1 == nil {
		t.Fatal("bucketEntryFor() returned nil")
	}
	if e2 := l.bucketEntryFor("k1", 1, 1); e2 != e1 {
		t.Fatal("bucketEntryFor() did not return the same bucket instance")
	}
}
