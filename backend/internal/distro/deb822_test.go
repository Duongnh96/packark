package distro

import (
	"strings"
	"testing"
)

// The stanza set a Proxmox 9 host actually has, pasted as-is.
const pveSources = `Types: deb
URIs: http://download.proxmox.com/debian/pve
Suites: trixie
Components: pve-no-subscription
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg

Types: deb deb-src
URIs: http://deb.debian.org/debian/
Suites: trixie trixie-updates
Components: main non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb deb-src
URIs: http://security.debian.org/debian-security/
Suites: trixie-security
Components: main non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg`

func TestDeb822Accepted(t *testing.T) {
	deb, _ := Get("debian")
	if err := ValidRepos(deb, []Repo{{
		URL: pveSources,
		Key: "https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg",
	}}); err != nil {
		t.Fatalf("a pasted .sources set must be accepted: %v", err)
	}
	// A stanza naming a keyring the image already ships needs no key of its own.
	base := strings.SplitN(pveSources, "\n\n", 2)[1]
	if err := ValidRepos(deb, []Repo{{URL: base}}); err != nil {
		t.Errorf("Signed-By without a key URL must be allowed: %v", err)
	}
	// A stanza with neither is unverifiable, so it is refused.
	if err := ValidRepos(deb, []Repo{{URL: "Types: deb\nURIs: http://x.test/debian\nSuites: trixie\nComponents: main"}}); err == nil {
		t.Error("a stanza with no key and no Signed-By must be refused")
	}
	// dnf distros have no use for it.
	ol, _ := Get("oraclelinux")
	if err := ValidRepos(ol, []Repo{{URL: pveSources}}); err == nil {
		t.Error("a .sources stanza is not an Oracle Linux repository")
	}
}

func TestDeb822Rejects(t *testing.T) {
	deb, _ := Get("debian")
	key := "https://x.test/k.gpg"
	for _, bad := range []string{
		"Types: deb\nURIs: http://x.test/d; rm -rf /\nSuites: trixie\nComponents: main",
		"Types: deb\nURIs: http://x.test/d\nSuites: $(id)\nComponents: main",
		"Types: deb\nURIs: file:///etc/passwd\nSuites: trixie\nComponents: main",
		"Types: deb rpm\nURIs: http://x.test/d\nSuites: trixie\nComponents: main",
		"Types: deb\nURIs: http://x.test/d\nSuites: trixie\nComponents: main\nExec: /bin/sh",
		"Types: deb\nURIs: http://x.test/d\nSuites: trixie\nComponents: main\nSigned-By: ../../etc/shadow",
		"Types: deb\nURIs: http://x.test/d\nSuites: trixie\nComponents: main\n Inline: key",
		"Types: deb\nURIs: http://x.test/d\nSuites: trixie\nComponents: main\n" + sourcesDelim,
		"Types: deb\nSuites: trixie\nComponents: main",
	} {
		if err := ValidRepos(deb, []Repo{{URL: bad, Key: key}}); err == nil {
			t.Errorf("slipped through:\n%s", bad)
		}
	}
}

func TestDeb822Setup(t *testing.T) {
	deb, _ := Get("debian")
	sh := RepoSetup(deb, []Repo{{
		URL: pveSources,
		Key: "https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg",
	}})
	if !strings.Contains(sh, "cat > /etc/apt/sources.list.d/packark-1-1.sources <<"+sourcesDelim) {
		t.Errorf("a stanza must be written as a .sources file:\n%s", sh)
	}
	if !strings.Contains(sh, "Components: pve-no-subscription") {
		t.Error("the stanza must be written through verbatim")
	}
	// A pasted host file repeats the distribution's own repositories. The image
	// has those already, and apt refuses a source declared twice with a
	// different Signed-By — which is exactly what deb.debian.org does here,
	// since the Debian image signs it with a .pgp keyring, not .gpg.
	if !strings.Contains(sh, `grep -rqsF -e "deb.debian.org/debian"`) {
		t.Errorf("already-configured stanzas must be guarded:\n%s", sh)
	}
	if !strings.Contains(sh, "is already configured, skipping") {
		t.Error("a skipped stanza must say so in the build log")
	}
	if n := strings.Count(sh, "cat > /etc/apt/sources.list.d/"); n != 3 {
		t.Errorf("each stanza gets its own guarded file, got %d:\n%s", n, sh)
	}
	// The key goes to the first keyring the image does not already have, so the
	// Proxmox one is created and Debian's own archive keyring is left alone.
	if !strings.Contains(sh, `for p in /usr/share/keyrings/proxmox-archive-keyring.gpg /usr/share/keyrings/debian-archive-keyring.gpg`) {
		t.Errorf("key must be placed by Signed-By order:\n%s", sh)
	}
	if !strings.Contains(sh, `[ -e "$p" ] ||`) {
		t.Error("an existing keyring must not be overwritten")
	}

	// A stanza without Signed-By gets one pointing at the fetched key.
	plain := RepoSetup(deb, []Repo{{
		URL: "Types: deb\nURIs: http://x.test/debian\nSuites: trixie\nComponents: main",
		Key: "https://x.test/k.gpg",
	}})
	if !strings.Contains(plain, "Signed-By: /etc/apt/keyrings/packark-1.gpg") {
		t.Errorf("a stanza without Signed-By must gain one:\n%s", plain)
	}

	// One-line entries are untouched by any of this.
	oneline := RepoSetup(deb, []Repo{{
		URL: "deb https://download.docker.com/linux/debian $CODENAME stable",
		Key: "https://download.docker.com/linux/debian/gpg",
	}})
	if strings.Contains(oneline, ".sources") || !strings.Contains(oneline, "packark-1.list") {
		t.Errorf("one-line entries still write a .list:\n%s", oneline)
	}
}
