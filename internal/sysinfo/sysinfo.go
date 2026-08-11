// Package sysinfo reports host network facts, currently the server's public
// IP addresses as seen by external echo services.
package sysinfo

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// cacheTTL keeps the dashboard from hitting the echo services on every
	// page view.
	cacheTTL = 10 * time.Minute
	// reqTimeout bounds each echo request so a blackholed outbound network
	// cannot stall dashboard rendering for long.
	reqTimeout = 2 * time.Second
	maxBody    = 64
)

var client = &http.Client{Timeout: reqTimeout}

// Endpoint lists are package vars so tests can point them at httptest
// servers. First reachable endpoint wins.
var (
	v4Endpoints = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com"}
	v6Endpoints = []string{"https://api6.ipify.org", "https://ipv6.icanhazip.com"}
)

var cache struct {
	sync.Mutex
	at     time.Time
	v4, v6 string
}

// PublicIPs returns the server's public IPv4 and IPv6 addresses. An empty
// string means "unknown": the echo services were unreachable or the server
// has no such address family. Results are cached for cacheTTL; when a
// refresh fails the previous value is kept.
func PublicIPs() (v4, v6 string) {
	cache.Lock()
	defer cache.Unlock()
	if time.Since(cache.at) < cacheTTL {
		return cache.v4, cache.v6
	}
	newV4 := fetchFirst(v4Endpoints, true)
	newV6 := fetchFirst(v6Endpoints, false)
	if newV4 != "" || newV6 != "" {
		cache.at = time.Now()
		cache.v4, cache.v6 = newV4, newV6
	}
	return cache.v4, cache.v6
}

// fetchFirst queries each endpoint in order and returns the first response
// that parses as an IP of the requested family. wantV4 selects the family.
func fetchFirst(endpoints []string, wantV4 bool) string {
	for _, url := range endpoints {
		if ip, err := fetchIP(url); err == nil && (ip.To4() != nil) == wantV4 {
			return ip.String()
		}
	}
	return ""
}

// fetchIP GETs an echo endpoint and parses the body as an IP address.
func fetchIP(url string) (net.IP, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return nil, &net.ParseError{Type: "IP address", Text: string(body)}
	}
	return ip, nil
}
