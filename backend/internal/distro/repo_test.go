package distro

import (
	"strings"
	"testing"
)

func TestValidReposRejectsShell(t *testing.T) {
	ol, _ := Get("oraclelinux")
	ub, _ := Get("ubuntu")

	if err := ValidRepos(ol, []Repo{{URL: "https://download.docker.com/linux/centos/docker-ce.repo"}}); err != nil {
		t.Errorf("the vendor shape must be accepted: %v", err)
	}
	if err := ValidRepos(ub, []Repo{{
		URL: "deb https://download.docker.com/linux/ubuntu $CODENAME stable",
		Key: "https://download.docker.com/linux/ubuntu/gpg",
	}}); err != nil {
		t.Errorf("the apt shape must be accepted: %v", err)
	}

	bad := []Repo{
		{URL: "https://x.test/a.repo; rm -rf /"},
		{URL: "https://x.test/$(id).repo"},
		{URL: "https://x.test/`id`.repo"},
		{URL: "https://x.test/a.repo\nrm -rf /"},
		{URL: `https://x.test/" ; rm -rf / ; echo ".repo`},
		{URL: "file:///etc/passwd"},
		{URL: "https://x.test/notarepo"},         // dnf needs a .repo file
		{URL: "$HOME/evil.repo"},                 // no scheme, shell variable
		{URL: "https://x.test/a.repo && curl x"}, // second command
	}
	for _, r := range bad {
		if err := ValidRepos(ol, []Repo{r}); err == nil {
			t.Errorf("slipped through: %q", r.URL)
		}
	}

	// apt lines are their own shape, and a key is not optional there.
	for _, r := range []Repo{
		{URL: "deb https://download.docker.com/linux/ubuntu noble stable"},
		{URL: "https://download.docker.com/linux/centos/docker-ce.repo", Key: "https://x.test/gpg"},
		{URL: "deb https://x.test/ubuntu noble stable", Key: "https://x.test/`id`"},
		{URL: "deb https://x.test/ubuntu noble stable; rm -rf /", Key: "https://x.test/gpg"},
	} {
		if err := ValidRepos(ub, []Repo{r}); err == nil {
			t.Errorf("apt: slipped through: %+v", r)
		}
	}

	many := make([]Repo, 9)
	for i := range many {
		many[i] = Repo{URL: "https://x.test/a.repo"}
	}
	if ValidRepos(ol, many) == nil {
		t.Error("nine repositories must be refused")
	}
}

func TestRepoSetup(t *testing.T) {
	ol, _ := Get("oraclelinux")
	ub, _ := Get("ubuntu")

	if got := RepoSetup(ol, nil); got != "" {
		t.Errorf("no repositories must add nothing, got %q", got)
	}

	dnf := RepoSetup(ol, []Repo{{URL: "https://download.docker.com/linux/centos/docker-ce.repo"}})
	if !strings.Contains(dnf, `-o /etc/yum.repos.d/packark-1.repo`) {
		t.Errorf("dnf setup: %s", dnf)
	}

	apt := RepoSetup(ub, []Repo{{
		URL: "deb https://download.docker.com/linux/ubuntu $CODENAME stable",
		Key: "https://download.docker.com/linux/ubuntu/gpg",
	}})
	if !strings.Contains(apt, "[signed-by=/etc/apt/keyrings/packark-1.gpg] https://download.docker.com") {
		t.Errorf("apt setup must pin the key it just downloaded: %s", apt)
	}
	if !strings.Contains(apt, "$CODENAME") {
		t.Error("apt setup must leave $CODENAME for the container to fill in")
	}
	// Vendors publish armored and binary keys; apt reads the extension, so a
	// binary key written to .asc reads as "repository is not signed".
	if !strings.Contains(apt, "gpg --dearmor") || strings.Contains(apt, "keyrings/packark-1.asc") {
		t.Errorf("keys must be normalised to a binary .gpg keyring: %s", apt)
	}

	// An existing option block survives and gains signed-by.
	withOpts := signedBy("deb [arch=amd64] https://x.test/ubuntu noble stable", "/k.asc")
	if withOpts != "deb [arch=amd64 signed-by=/k.asc] https://x.test/ubuntu noble stable" {
		t.Errorf("options lost: %s", withOpts)
	}
}

func TestScriptsSpliceRepos(t *testing.T) {
	ol, _ := Get("oraclelinux")
	repos := []Repo{{URL: "https://download.docker.com/linux/centos/docker-ce.repo"}}

	build := ol.BuildScript([]string{"docker-ce"}, repos)
	if strings.Contains(build, "{{") {
		t.Errorf("unfilled placeholder: %s", build)
	}
	if !strings.Contains(build, "docker-ce") || !strings.Contains(build, "yum.repos.d/packark-1.repo") {
		t.Errorf("build script missing packages or repo: %s", build)
	}
	// The repo must be in place before anything is downloaded.
	if strings.Index(build, "packark-1.repo") > strings.Index(build, "dnf download") {
		t.Error("repositories must be added before the download runs")
	}

	search := ol.SearchScript("docker-ce", repos)
	if strings.Contains(search, "{{") || !strings.Contains(search, "packark-1.repo") {
		t.Errorf("search script: %s", search)
	}

	// Without repos nothing extra is enabled. ("packark-local" is the offline
	// repo every bundle ships with, so look for the added-repo file instead.)
	plain := ol.BuildScript([]string{"tree"}, nil)
	if strings.Contains(plain, "yum.repos.d/packark-1.repo") || strings.Contains(plain, "{{") {
		t.Errorf("plain build script must not add a repository: %s", plain)
	}
}

// Presets are shipped copies of vendor documentation, so they rot quietly.
// This fails the moment one stops being something packark would accept.
func TestPresetsAreValid(t *testing.T) {
	seen := 0
	for _, d := range All() {
		for _, p := range d.Presets {
			if p.Name == "" {
				t.Errorf("%s: preset without a name", d.ID)
			}
			if err := ValidRepos(d, []Repo{{URL: p.URL, Key: p.Key}}); err != nil {
				t.Errorf("%s preset %q is not accepted by its own distro: %v", d.ID, p.Name, err)
			}
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("no presets to check")
	}
}

func TestProxmoxRepoShape(t *testing.T) {
	deb, ok := Get("debian")
	if !ok {
		t.Fatal("debian is missing")
	}
	var pve *RepoPreset
	for i, p := range deb.Presets {
		if strings.HasPrefix(p.Name, "Proxmox VE") {
			pve = &deb.Presets[i]
		}
	}
	if pve == nil {
		t.Fatal("no Proxmox VE preset")
	}
	// The key URL carries the codename too, which is the reason key URLs are
	// validated with the variables stripped rather than rejected outright.
	if !strings.Contains(pve.Key, "$CODENAME") {
		t.Error("the Proxmox key is per codename")
	}

	sh := RepoSetup(deb, []Repo{{URL: pve.URL, Key: pve.Key}})
	if !strings.Contains(sh, `CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"`) {
		t.Error("the container must resolve its own codename")
	}
	for _, want := range []string{
		`curl -fsSL "https://enterprise.proxmox.com/debian/proxmox-release-$CODENAME.gpg"`,
		"[signed-by=/etc/apt/keyrings/packark-1.gpg] http://download.proxmox.com/debian/pve $CODENAME pve-no-subscription",
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("missing %q in:\n%s", want, sh)
		}
	}
}

// Adding a repository installs curl and gnupg into the build image. If the
// download then resolves against that polluted image, every dependency they
// dragged in is left out of the bundle and the offline install fails with
// "depends on libssl3 ... however:".
func TestAptResolvesAgainstThePristineImage(t *testing.T) {
	for _, id := range []string{"ubuntu", "debian"} {
		d, _ := Get(id)
		sh := d.BuildScript([]string{"libpve-common-perl"}, nil)
		snap := strings.Index(sh, "cp /var/lib/dpkg/status /tmp/packark-status.pre")
		setup := strings.Index(sh, "{{REPOS}}")
		dl := strings.Index(sh, "Dir::State::status=/tmp/packark-status.pre")
		if snap < 0 || dl < 0 {
			t.Fatalf("%s: build script must snapshot the status file and resolve against it", id)
		}
		if setup >= 0 && snap > setup {
			t.Errorf("%s: the snapshot must be taken before the repository setup runs", id)
		}
	}
}
