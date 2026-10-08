package distro

import (
	"fmt"
	"regexp"
	"strings"
)

// deb822 is the .sources format apt has used since buster and that Proxmox
// documents for trixie:
//
//	Types: deb
//	URIs: http://download.proxmox.com/debian/pve
//	Suites: trixie
//	Components: pve-no-subscription
//	Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
//
// Several stanzas separated by blank lines go in one file, which is how the
// Proxmox wiki prints a host's whole set of sources.

// sourcesDelim ends the heredoc that writes the file. No stanza may contain it.
const sourcesDelim = "PACKARK_SOURCES_EOF"

var (
	fieldRe = regexp.MustCompile(`^([A-Za-z][A-Za-z-]*):[ \t]*(.*)$`)
	// Suites, components, architectures: plain tokens, plus the variables the
	// container fills in.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9._$/+~-]+$`)
	// Signed-By names a keyring file, so an absolute path and nothing else.
	keyPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

// deb822Fields maps the fields packark accepts to how their values are checked.
// Anything else is refused by name rather than silently written to the file.
var deb822Fields = map[string]string{
	"types":         "types",
	"uris":          "uris",
	"suites":        "tokens",
	"components":    "tokens",
	"architectures": "tokens",
	"languages":     "tokens",
	"targets":       "tokens",
	"signed-by":     "path",
	"enabled":       "yesno",
	"trusted":       "yesno",
	"pdiffs":        "yesno",
	"by-hash":       "tokens",
}

// looksLikeDeb822 tells a pasted stanza from a one-line "deb ..." entry.
func looksLikeDeb822(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "types:") {
			return true
		}
	}
	return false
}

// validateDeb822 checks every field and value before any of it reaches a file
// that apt will act on as root.
func validateDeb822(s string) error {
	var sawTypes, sawURIs bool
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue // stanza separator
		}
		if line == sourcesDelim {
			return fmt.Errorf("a repository may not contain the line %q", sourcesDelim)
		}
		if line != strings.TrimLeft(line, " \t") {
			return fmt.Errorf("indented lines are not supported: paste the key URL " +
				"in the key field instead of inlining it")
		}
		m := fieldRe.FindStringSubmatch(line)
		if m == nil {
			return fmt.Errorf("not a deb822 field: %q", line)
		}
		name, value := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		kind, ok := deb822Fields[name]
		if !ok {
			return fmt.Errorf("unsupported deb822 field: %q", m[1])
		}
		if value == "" {
			return fmt.Errorf("%s has no value", m[1])
		}
		if err := validateDeb822Value(name, kind, value); err != nil {
			return err
		}
		switch name {
		case "types":
			sawTypes = true
		case "uris":
			sawURIs = true
		}
	}
	if !sawTypes || !sawURIs {
		return fmt.Errorf("a deb822 repository needs at least Types and URIs")
	}
	return nil
}

func validateDeb822Value(name, kind, value string) error {
	for _, v := range strings.Fields(value) {
		switch kind {
		case "types":
			if v != "deb" && v != "deb-src" {
				return fmt.Errorf("Types may only be deb or deb-src, not %q", v)
			}
		case "uris":
			if !urlRe.MatchString(allowedVars.Replace(v)) {
				return fmt.Errorf("bad URI: %q", v)
			}
		case "path":
			if !keyPathRe.MatchString(v) {
				return fmt.Errorf("Signed-By must be an absolute keyring path, not %q", v)
			}
		case "yesno":
			if l := strings.ToLower(v); l != "yes" && l != "no" && l != "true" && l != "false" {
				return fmt.Errorf("%s must be yes or no, not %q", name, v)
			}
		default:
			if !tokenRe.MatchString(v) {
				return fmt.Errorf("bad value in %s: %q", name, v)
			}
		}
	}
	return nil
}

// signedByPaths lists the keyrings a stanza set names, in the order they appear.
func signedByPaths(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		m := fieldRe.FindStringSubmatch(strings.TrimRight(line, " \t"))
		if m == nil || strings.ToLower(m[1]) != "signed-by" {
			continue
		}
		out = append(out, strings.Fields(strings.TrimSpace(m[2]))...)
	}
	return out
}

// splitStanzas returns the blank-line separated stanzas, trimmed.
func splitStanzas(s string) []string {
	var out []string
	for _, st := range strings.Split(s, "\n\n") {
		if st = strings.TrimSpace(st); st != "" {
			out = append(out, st)
		}
	}
	return out
}

// uriNeedles reduces a stanza's URIs to something greppable in the sources the
// image already has: no scheme, no trailing slash, so "http://deb.debian.org/
// debian/" still matches the image's "http://deb.debian.org/debian".
func uriNeedles(stanza string) []string {
	var out []string
	for _, line := range strings.Split(stanza, "\n") {
		m := fieldRe.FindStringSubmatch(strings.TrimRight(line, " \t"))
		if m == nil || strings.ToLower(m[1]) != "uris" {
			continue
		}
		for _, u := range strings.Fields(strings.TrimSpace(m[2])) {
			u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
			out = append(out, strings.TrimSuffix(u, "/"))
		}
	}
	return out
}

// withSignedBy appends Signed-By to any stanza that has none, so a pasted
// stanza without one still verifies against the key packark fetched.
func withSignedBy(s, keyring string) string {
	var out []string
	for _, stanza := range strings.Split(s, "\n\n") {
		if strings.TrimSpace(stanza) == "" {
			continue
		}
		if len(signedByPaths(stanza)) == 0 {
			stanza = strings.TrimRight(stanza, "\n") + "\nSigned-By: " + keyring
		}
		out = append(out, strings.TrimRight(stanza, "\n"))
	}
	return strings.Join(out, "\n\n") + "\n"
}
