package geo

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func geoTestServer(t *testing.T, files map[string][]byte, badSum string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if strings.HasSuffix(name, ".sha256sum") {
			target := strings.TrimSuffix(name, ".sha256sum")
			data, ok := files[target]
			if !ok {
				http.NotFound(w, r)
				return
			}
			sum := sha256.Sum256(data)
			digest := fmt.Sprintf("%x", sum[:])
			if target == badSum {
				digest = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  %s\n", digest, target)
			return
		}
		data, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
}

func withGeoTestClient(t *testing.T, server *httptest.Server) {
	t.Helper()
	old := client
	client = server.Client()
	t.Cleanup(func() {
		client = old
		server.Close()
	})
}

func TestUpdateStreamsAndReplacesVerifiedPair(t *testing.T) {
	files := map[string][]byte{
		"geoip.dat":   []byte("new geoip"),
		"geosite.dat": []byte("new geosite"),
	}
	server := geoTestServer(t, files, "")
	withGeoTestClient(t, server)
	dir := t.TempDir()

	if err := Update(dir, server.URL); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s = %q, err=%v", name, got, err)
		}
	}
}

func TestUpdateChecksumFailureKeepsExistingPair(t *testing.T) {
	files := map[string][]byte{
		"geoip.dat":   []byte("new geoip"),
		"geosite.dat": []byte("new geosite"),
	}
	server := geoTestServer(t, files, "geosite.dat")
	withGeoTestClient(t, server)
	dir := t.TempDir()
	for _, name := range Files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := Update(dir, server.URL); err == nil {
		t.Fatal("expected checksum failure")
	}
	for _, name := range Files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != "old "+name {
			t.Fatalf("%s changed on staged update failure: %q, err=%v", name, got, err)
		}
	}
}
