package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestKeyIgnoresOrder(t *testing.T) {
	a := Key("ubuntu", "24.04", []string{"nginx", "curl"}, nil)
	b := Key("ubuntu", "24.04", []string{"curl", "nginx"}, nil)
	if a != b {
		t.Errorf("same request must hash the same: %s vs %s", a, b)
	}
	for _, other := range [][]any{
		{"ubuntu", "22.04", []string{"nginx", "curl"}, []string(nil)},
		{"oraclelinux", "24.04", []string{"nginx", "curl"}, []string(nil)},
		{"ubuntu", "24.04", []string{"nginx"}, []string(nil)},
		// A different repository can resolve the same name to a different package.
		{"ubuntu", "24.04", []string{"nginx", "curl"}, []string{"https://x.test/a.repo"}},
	} {
		got := Key(other[0].(string), other[1].(string), other[2].([]string), other[3].([]string))
		if got == a {
			t.Errorf("%v must not collide with the base request", other)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b := Bundle{
		Key: "abc123", Distro: "ubuntu", Version: "24.04",
		Packages: []string{"nginx", "curl"}, Repos: []string{"https://x.test/docker.repo"},
		Path: "/tmp/abc123.tar.gz",
		Size: 4096, SHA256: "deadbeef", Log: "built fine", CreatedAt: time.Now(),
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("abc123")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "https://x.test/docker.repo" {
		t.Errorf("repositories lost in round trip: %+v", got.Repos)
	}
	if len(got.Packages) != 2 || got.Packages[0] != "nginx" || got.SHA256 != "deadbeef" {
		t.Errorf("round trip lost data: %+v", got)
	}

	// A rebuild of the same request replaces the row instead of duplicating it.
	b.Size = 8192
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(10)
	if err != nil || len(list) != 1 || list[0].Size != 8192 {
		t.Errorf("save must upsert: %+v err=%v", list, err)
	}
	if list[0].Log != "" {
		t.Error("List must not carry logs")
	}

	if err := s.Delete("abc123"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("abc123"); ok {
		t.Error("deleted bundle still readable")
	}
	if _, ok, err := s.Get("nope"); ok || err != nil {
		t.Errorf("missing key must be a clean miss: ok=%v err=%v", ok, err)
	}
}

// A deb822 stanza spans lines, so the repos column cannot be newline-separated.
func TestReposSurviveMultilineStanzas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	stanza := "Types: deb\nURIs: http://download.proxmox.com/debian/pve\nSuites: trixie\n" +
		"Components: pve-no-subscription"
	b := Bundle{
		Key: "deb822", Distro: "debian", Version: "13", Packages: []string{"pve-manager"},
		Repos: []string{stanza, "deb https://x.test/debian trixie main"},
		Path:  "/tmp/x.tar.gz", Size: 1, SHA256: "a", CreatedAt: time.Now(),
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("deb822")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("a multi-line stanza must stay one entry, got %d: %q", len(got.Repos), got.Repos)
	}
	if got.Repos[0] != stanza {
		t.Errorf("stanza came back changed:\n%q", got.Repos[0])
	}
}

func TestDecodeReposReadsLegacyRows(t *testing.T) {
	// Rows written before the column held JSON are newline-separated.
	got := decodeRepos("https://a.test/a.repo\nhttps://b.test/b.repo")
	if len(got) != 2 || got[0] != "https://a.test/a.repo" {
		t.Errorf("legacy rows must still read: %q", got)
	}
	if got := decodeRepos(""); got != nil {
		t.Errorf("empty means none, got %q", got)
	}
}
