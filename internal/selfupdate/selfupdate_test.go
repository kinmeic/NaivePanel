package selfupdate

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.1.0", "v1.0.0", true},
		{"v0.1.9", "v0.1.10", true},
		{"0.1.0", "0.2.0", true},
		{"v0.2.0", "v0.1.0", false},
		{"v0.2.0", "v0.2.0", false},
		{"dev", "v0.2.0", false},
		{"v0.1.0", "not-a-version", false},
		{"v0.1.0", "", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestSha256ForAsset(t *testing.T) {
	sums := []byte("aaa  naivepanel-linux-amd64.tar.gz\n" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  naivepanel-linux-arm64.tar.gz\n")
	got, err := sha256ForAsset(sums, "naivepanel-linux-arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("unexpected digest %q", got)
	}
	if _, err := sha256ForAsset(sums, "naivepanel-linux-amd64.tar.gz"); err == nil {
		t.Error("short digest should be rejected")
	}
	if _, err := sha256ForAsset(sums, "missing.tar.gz"); err == nil {
		t.Error("missing asset should error")
	}
}
