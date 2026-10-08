package distro

import (
	"fmt"
	"regexp"
	"strings"
)

// Repo is an extra package repository to enable inside the build container —
// a vendor repo like Docker's, which no stock image has configured.
//
// On dnf distros URL is a .repo file, the shape vendors publish:
//
//	https://download.docker.com/linux/centos/docker-ce.repo
//
// On apt there is no such file, so URL is the sources.list line itself and Key
// is the GPG key to verify it with:
//
//	deb https://download.docker.com/linux/ubuntu $CODENAME stable
//	https://download.docker.com/linux/ubuntu/gpg
type Repo struct {
	URL string `json:"url"`
	Key string `json:"key,omitempty"`
}

// RepoPreset is a repository people reach for often enough that copying the
// line out of a vendor wiki is the worst part of the job. Presets go through
// exactly the same validation as anything typed by hand.
type RepoPreset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Key  string `json:"key,omitempty"`
	Note string `json:"note,omitempty"`
}

// Everything here ends up inside a shell script running as root in a container
// that then downloads and tars up whatever those repositories serve. The rules
// are deliberately narrow: an http(s) URL out of the usual URL characters, and
// nothing that could close a quote, start a command or expand to one.
var (
	urlRe = regexp.MustCompile(`^https?://[A-Za-z0-9._~-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~%/+=:@&?-]*)?$`)
	// An apt line: "deb", optional [options], the URL, a suite, then components.
	debRe = regexp.MustCompile(`^deb(-src)?( \[[A-Za-z0-9=,._:/+-]+\])? (https?://\S+) ([A-Za-z0-9._$-]+)((?: [A-Za-z0-9._-]+)+)$`)
	// $CODENAME and $RELEASEVER are filled in by the container, which knows its
	// own release; every other dollar sign is refused.
	allowedVars = strings.NewReplacer("$CODENAME", "", "$RELEASEVER", "")
)

// ValidRepos rejects anything that is not a plain URL or a plain apt line,
// before it reaches a shell.
func ValidRepos(d Distro, repos []Repo) error {
	if len(repos) > 8 {
		return fmt.Errorf("too many repositories (max 8)")
	}
	for _, r := range repos {
		u := strings.TrimSpace(r.URL)
		if u == "" {
			return fmt.Errorf("empty repository")
		}
		// A deb822 stanza is the one shape that spans lines; everything else
		// is a single line, and no shape may carry shell punctuation at all.
		flat := strings.ReplaceAll(u, "\n", " ")
		if strings.ContainsAny(flat+r.Key, "`'\"\\;|&<>\n\r\t") {
			return fmt.Errorf("bad repository: %q", u)
		}
		if strings.Contains(allowedVars.Replace(flat+r.Key), "$") {
			return fmt.Errorf("repository may only use $CODENAME and $RELEASEVER: %q", u)
		}
		// The container fills the allowed variables in, so validate the URL
		// shape without them: Proxmox keys live at proxmox-release-$CODENAME.gpg.
		if r.Key != "" && !urlRe.MatchString(allowedVars.Replace(r.Key)) {
			return fmt.Errorf("bad key URL: %q", r.Key)
		}
		switch d.RepoFamily {
		case "dnf":
			if !urlRe.MatchString(allowedVars.Replace(u)) {
				return fmt.Errorf("bad repository URL: %q", u)
			}
			if !strings.HasSuffix(u, ".repo") {
				return fmt.Errorf("%s wants a .repo file URL, like "+
					"https://download.docker.com/linux/centos/docker-ce.repo", d.Name)
			}
		case "apt":
			if looksLikeDeb822(u) {
				if err := validateDeb822(u); err != nil {
					return err
				}
				// A stanza pointing at a keyring the image already ships (the
				// Debian base repositories do) needs no key of its own.
				if r.Key == "" && len(signedByPaths(u)) == 0 {
					return fmt.Errorf("give a key URL, or a Signed-By keyring path in the stanza")
				}
				continue
			}
			if strings.Contains(u, "\n") {
				return fmt.Errorf("a one-line entry cannot span lines; a pasted .sources " +
					"stanza must start with Types:")
			}
			if !debRe.MatchString(u) {
				return fmt.Errorf("%s wants a sources.list line like "+
					"deb https://download.docker.com/linux/ubuntu $CODENAME stable, "+
					"or a deb822 stanza starting with Types:", d.Name)
			}
			if r.Key == "" {
				return fmt.Errorf("apt repositories need a GPG key URL")
			}
		default:
			return fmt.Errorf("%s does not support extra repositories", d.Name)
		}
	}
	return nil
}

// RepoSetup is the shell that enables these repositories, spliced into both the
// download and the search script. Empty when there are none, so the scripts are
// byte for byte what they were before this feature existed.
func RepoSetup(d Distro, repos []Repo) string {
	if len(repos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n# extra repositories\n")
	switch d.RepoFamily {
	case "dnf":
		// curl ships in the Oracle Linux images; install it only if that changes.
		b.WriteString("command -v curl >/dev/null 2>&1 || " +
			"dnf install -y curl || yum install -y curl\n")
		for i, r := range repos {
			fmt.Fprintf(&b, "curl -fsSL %q -o /etc/yum.repos.d/packark-%d.repo\n", r.URL, i+1)
		}
	case "apt":
		b.WriteString("apt-get install -y --no-install-recommends ca-certificates curl gnupg\n")
		b.WriteString("install -m 0755 -d /etc/apt/keyrings\n")
		b.WriteString(`CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"` + "\n")
		for i, r := range repos {
			keyring := fmt.Sprintf("/etc/apt/keyrings/packark-%d.gpg", i+1)
			stanza := looksLikeDeb822(r.URL)

			if r.Key != "" {
				// Where the key has to land: a stanza names its own keyring, and
				// a pasted set may name several. Skip the ones the image already
				// ships (Debian's own archive keyring) and take the first gap.
				targets := []string{keyring}
				if stanza {
					if paths := signedByPaths(r.URL); len(paths) > 0 {
						targets = paths
					}
				}
				tmp := fmt.Sprintf("/tmp/packark-%d.key", i+1)
				fmt.Fprintf(&b, "curl -fsSL %q -o %s\n", r.Key, tmp)
				// apt picks its parser from the extension, and vendors publish both
				// shapes: Docker's key is ASCII-armored, Proxmox's is binary. Older
				// apt reads a binary key in a .asc file as "repository is not
				// signed", so normalise everything to a binary keyring.
				fmt.Fprintf(&b, "if grep -qa 'BEGIN PGP PUBLIC KEY' %s; "+
					"then gpg --dearmor < %s > %s.gpg; else cp %s %s.gpg; fi\n",
					tmp, tmp, tmp, tmp, tmp)
				fmt.Fprintf(&b, "for p in %s; do [ -e \"$p\" ] || "+
					"{ mkdir -p \"$(dirname \"$p\")\"; cp %s.gpg \"$p\"; chmod a+r \"$p\"; break; }; done\n",
					strings.Join(targets, " "), tmp)
				fmt.Fprintf(&b, "rm -f %s %s.gpg\n", tmp, tmp)
			}

			if stanza {
				// A pasted set is usually a whole host's sources, so it repeats
				// the distribution's own repositories. The image already has
				// those, and apt refuses a source declared twice with a
				// different Signed-By, so anything already configured is left
				// alone and only the new repositories are written.
				for j, st := range splitStanzas(withSignedBy(r.URL, keyring)) {
					file := fmt.Sprintf("/etc/apt/sources.list.d/packark-%d-%d.sources", i+1, j+1)
					needles := uriNeedles(st)
					if len(needles) == 0 {
						continue
					}
					var args strings.Builder
					for _, n := range needles {
						fmt.Fprintf(&args, " -e %q", n)
					}
					fmt.Fprintf(&b, "if grep -rqsF%s /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null; then\n",
						args.String())
					fmt.Fprintf(&b, "  echo \"packark: %s is already configured, skipping\"\nelse\n", needles[0])
					// The heredoc is unquoted so the container expands $CODENAME.
					fmt.Fprintf(&b, "  cat > %s <<%s\n%s\n%s\nfi\n", file, sourcesDelim, st, sourcesDelim)
				}
				continue
			}
			fmt.Fprintf(&b, "echo \"%s\" > /etc/apt/sources.list.d/packark-%d.list\n",
				signedBy(r.URL, keyring), i+1)
		}
		b.WriteString("apt-get update\n")
	}
	return b.String()
}

// signedBy points an apt line at the key we just downloaded. A line that
// already carries options keeps them and gains signed-by.
func signedBy(line, keyring string) string {
	opt := "signed-by=" + keyring
	m := debRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	if opts := strings.TrimSpace(m[2]); opts != "" {
		inner := strings.Trim(opts, "[]")
		if strings.Contains(inner, "signed-by=") {
			return line
		}
		return strings.Replace(line, opts, "["+inner+" "+opt+"]", 1)
	}
	return strings.Replace(line, m[1]+" ", m[1]+" ["+opt+"] ", 1)
}

// URLs is what gets stored and shown next to a bundle.
func URLs(repos []Repo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.URL)
	}
	return out
}
