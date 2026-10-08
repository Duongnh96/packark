package distro

import (
	"strings"
	"testing"
)

func TestValidPackages(t *testing.T) {
	ok := []string{"nginx", "postgresql-16", "libc6:amd64", "curl=8.5.0-2ubuntu10",
		"g++", "ImageMagick", "tree-1.8.0-10.el9.x86_64", "kernel-uek"}
	if err := ValidPackages(ok); err != nil {
		t.Fatalf("want ok, got %v", err)
	}
	for _, p := range []string{"nginx; rm -rf /", "nginx && curl x", "$(id)", "`id`",
		"--force-yes", "-y", "a b", "*", "../etc/passwd", ""} {
		if err := ValidPackages([]string{p}); err == nil {
			t.Errorf("injection slipped through: %q", p)
		}
	}
	if ValidPackages(nil) == nil {
		t.Error("empty list must fail")
	}
}

func TestDistros(t *testing.T) {
	for _, d := range All() {
		if !strings.Contains(d.Script, "{{PKGS}}") || !strings.Contains(d.Script, "{{REPOS}}") {
			t.Errorf("%s: build script needs {{PKGS}} and {{REPOS}}", d.ID)
		}
		if !strings.Contains(d.Search, "{{TERM}}") || !strings.Contains(d.Search, "{{REPOS}}") {
			t.Errorf("%s: search script needs {{TERM}} and {{REPOS}}", d.ID)
		}
		if d.CacheDir == "" || (d.RepoFamily != "apt" && d.RepoFamily != "dnf") {
			t.Errorf("%s: needs a cache dir and a repo family", d.ID)
		}
		if len(d.Releases) == 0 || d.Image == "" {
			t.Errorf("%s: incomplete distro", d.ID)
		}
	}
	u, ok := Get("ubuntu")
	if !ok || !u.HasVersion("24.04") || u.HasVersion("13.37") {
		t.Error("ubuntu release whitelist broken")
	}
	o, ok := Get("oraclelinux")
	if !ok || !o.HasVersion("9") || o.HasVersion("6") {
		t.Error("oracle release whitelist broken")
	}
	if _, ok := Get("windows"); ok {
		t.Error("unknown distro must not resolve")
	}
}

// The generated install.sh must treat the bundle as a repository and install
// only what was asked for. Passing every rpm to dnf makes it reconcile the
// whole base system and erase protected packages instead.
func TestInstallScriptUsesALocalRepo(t *testing.T) {
	ol, _ := Get("oraclelinux")
	sh := ol.BuildScript([]string{"docker-ce"}, nil)
	for _, want := range []string{"createrepo", "enablerepo=packark-local", "baseurl=file://"} {
		if !strings.Contains(sh, want) {
			t.Errorf("build script is missing %q", want)
		}
	}
	if strings.Contains(sh, "install -y --disablerepo='*' ./*.rpm") {
		t.Error("install.sh must not hand every rpm to the package manager")
	}
	// install.sh installs the requested names, which the container expands from
	// PKGS while it writes the file.
	if !strings.Contains(sh, "--enablerepo=packark-local $PKGS") {
		t.Error("install.sh must install the requested packages by name")
	}
	if !strings.Contains(sh, `PKGS="docker-ce"`) {
		t.Error("the requested packages must reach the script")
	}
	// These belong to the installing machine, so they must survive generation.
	for _, runtime := range []string{`cd "\$(dirname "\$0")"`, `baseurl=file://\$PWD`} {
		if !strings.Contains(sh, runtime) {
			t.Errorf("%s must stay unexpanded until install time", runtime)
		}
	}

	u, _ := Get("ubuntu")
	sh = u.BuildScript([]string{"nginx"}, nil)
	if strings.Contains(sh, "dpkg -i *.deb") {
		t.Error("apt install.sh must not install every deb in the bundle")
	}
	for _, want := range []string{"apt-ftparchive packages", `file:\$PWD ./`, "install -y $PKGS", `PKGS="nginx"`} {
		if !strings.Contains(sh, want) {
			t.Errorf("apt build script is missing %q", want)
		}
	}
}

func TestNotFound(t *testing.T) {
	apt := `Reading package lists...
Package docker is not available, but is referred to by another package.
However the following packages replace it:
  wmdocker

E: Package 'docker' has no installation candidate
E: Unable to locate package nginx-full`
	if got := NotFound(apt); strings.Join(got, ",") != "docker,nginx-full" {
		t.Errorf("apt: got %v", got)
	}
	dnf := "No match for argument: docker\nError: Unable to find a match"
	if got := NotFound(dnf); strings.Join(got, ",") != "docker" {
		t.Errorf("dnf: got %v", got)
	}
	if got := NotFound("yum: No package htop available."); strings.Join(got, ",") != "htop" {
		t.Errorf("yum: got %v", got)
	}
	if got := NotFound("Get:1 http://archive.ubuntu.com noble InRelease"); len(got) != 0 {
		t.Errorf("clean log must yield nothing, got %v", got)
	}
}

func TestImageRefs(t *testing.T) {
	o, _ := Get("oraclelinux")
	if ref := o.ImageRef("8.9"); ref != "oraclelinux:8.9" {
		t.Errorf("point release tag broken: %q", ref)
	}
	if ref := o.ImageRef("8.99"); ref != "" {
		t.Errorf("unknown release must not resolve: %q", ref)
	}
	u, _ := Get("ubuntu")
	if ref := u.ImageRef("24.04"); ref != "ubuntu:24.04" {
		t.Errorf("ubuntu tag broken: %q", ref)
	}
	for _, d := range All() {
		for _, r := range d.Releases {
			if r.Group == "" || r.Name == "" {
				t.Errorf("%s %s: release needs a name and a group", d.ID, r.Version)
			}
		}
	}
}

func TestValidTerm(t *testing.T) {
	for _, q := range []string{"docker", "python3", "lib.so", "g", "docker-ce"} {
		if err := ValidTerm(q); err != nil {
			t.Errorf("%q should be searchable: %v", q, err)
		}
	}
	for _, q := range []string{"", "-rf", "a b", "a;b", "$(id)", "`id`", "a|b", "a*", "a'b", strings.Repeat("a", 65)} {
		if ValidTerm(q) == nil {
			t.Errorf("injection slipped through: %q", q)
		}
	}
}

func TestParseSearch(t *testing.T) {
	apt := "docker.io - Linux container runtime\ndocker-compose - define and run multi-container Docker applications\nSorted by relevance"
	got := ParseSearch(apt)
	if len(got) != 2 || got[0].Name != "docker.io" || got[0].Summary != "Linux container runtime" {
		t.Fatalf("apt parse: %+v", got)
	}
	dnf := "====== Name Matched: docker ======\npodman-docker.noarch : Emulate Docker CLI using podman\npodman-docker.x86_64 : Emulate Docker CLI using podman"
	got = ParseSearch(dnf)
	if len(got) != 1 || got[0].Name != "podman-docker" {
		t.Fatalf("dnf parse (arch strip + dedupe): %+v", got)
	}
	madison := "nginx - small, powerful, scalable web/proxy server\n" +
		"     nginx | 1.24.0-2ubuntu7.1 | http://archive.ubuntu.com/ubuntu noble-updates/main amd64 Packages\n" +
		"     nginx | 1.24.0-2ubuntu7 | http://archive.ubuntu.com/ubuntu noble/main amd64 Packages\n" +
		"     nginx | 1.24.0-2ubuntu7 | http://archive.ubuntu.com/ubuntu noble/main Sources\n"
	got = ParseSearch(madison)
	if len(got) != 1 || strings.Join(got[0].Versions, ",") != "1.24.0-2ubuntu7.1,1.24.0-2ubuntu7" {
		t.Fatalf("apt versions (newest first, no sources, no dupes): %+v", got)
	}
	repoquery := "docker-ce.x86_64 : The open-source application container engine\n" +
		"docker-ce | 3:27.3.1-1.el9 | docker-ce-stable\n\ndocker-ce | 3:26.1.4-1.el9 | docker-ce-stable\n"
	got = ParseSearch(repoquery)
	if len(got) != 1 || strings.Join(got[0].Versions, ",") != "3:27.3.1-1.el9,3:26.1.4-1.el9" {
		t.Fatalf("dnf versions: %+v", got)
	}
	if got := ParseSearch(""); len(got) != 0 {
		t.Errorf("empty output must yield nothing, got %v", got)
	}
}
