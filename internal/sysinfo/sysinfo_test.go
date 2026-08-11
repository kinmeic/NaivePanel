package sysinfo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "203.0.113.7")
	}))
	defer srv.Close()
	if got := fetchFirst([]string{srv.URL}, true); got != "203.0.113.7" {
		t.Fatalf("got %q", got)
	}
	// IPv4 body must not satisfy an IPv6 request.
	if got := fetchFirst([]string{srv.URL}, false); got != "" {
		t.Fatalf("family mismatch accepted: %q", got)
	}
}

func TestFetchFirstSkipsGarbage(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "not an ip")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "2001:db8::1")
	}))
	defer good.Close()
	if got := fetchFirst([]string{bad.URL, good.URL}, false); got != "2001:db8::1" {
		t.Fatalf("got %q", got)
	}
}

func TestPublicIPsCachesAndKeepsStaleOnFailure(t *testing.T) {
	cache.Lock()
	cache.at, cache.v4, cache.v6 = time.Time{}, "", ""
	cache.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "198.51.100.9")
	}))
	defer srv.Close()

	oldV4, oldV6 := v4Endpoints, v6Endpoints
	defer func() { v4Endpoints, v6Endpoints = oldV4, oldV6 }()
	v4Endpoints = []string{srv.URL}
	v6Endpoints = []string{srv.URL} // serves v4, so v6 lookup comes back empty

	v4, _ := PublicIPs()
	if v4 != "198.51.100.9" {
		t.Fatalf("first fetch got %q", v4)
	}

	// Endpoints dead now: cached value must survive a failed refresh.
	v4Endpoints, v6Endpoints = []string{"http://127.0.0.1:1"}, []string{"http://127.0.0.1:1"}
	cache.Lock()
	cache.at = time.Now().Add(-2 * cacheTTL)
	cache.Unlock()
	v4, _ = PublicIPs()
	if v4 != "198.51.100.9" {
		t.Fatalf("stale value lost: %q", v4)
	}

	// Within TTL the cache is used as-is.
	cache.Lock()
	cache.at = time.Now()
	cache.v4 = "192.0.2.1"
	cache.Unlock()
	v4, _ = PublicIPs()
	if v4 != "192.0.2.1" {
		t.Fatalf("TTL cache miss: %q", v4)
	}
}
