package distro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The release lists in this package are curated: they carry the labels that say
// which releases are supported and which are archived, and no registry knows
// that. What the registry does know is which tags exist, and that changes —
// Debian prunes old point releases, vendors publish new ones. So the tags are
// fetched and merged over the curated list: an entry whose tag is gone
// disappears from the picker, a tag nobody has listed yet is added.

// hubTags is Docker Hub's tag listing for an official image.
const hubTags = "https://hub.docker.com/v2/repositories/library/%s/tags"

// maxTagPages caps a listing at 500 tags, which is well past the numeric ones.
const maxTagPages = 5

// freshWindow decides which unlisted tags are worth adding. A registry keeps
// every tag it ever published, so version numbers alone cannot tell a release
// that shipped last month from one that died in 2021: oraclelinux:6.10 was last
// pushed in March 2021, ubuntu:26.10 five days ago. Only recently published
// tags join the picker; the curated entries stay regardless of their age.
const freshWindow = 18 * 30 * 24 * time.Hour

// Tag is a registry tag and when it was last pushed.
type Tag struct {
	Name    string
	Updated time.Time
}

// FetchTags lists the tags an image publishes, newest first.
func FetchTags(ctx context.Context, client *http.Client, image string) ([]Tag, error) {
	return fetchTagsFrom(ctx, client, hubTags, image)
}

// fetchTagsFrom is FetchTags against a given endpoint, so the tests can serve
// their own registry instead of reaching for Docker Hub.
func fetchTagsFrom(ctx context.Context, client *http.Client, endpoint, image string) ([]Tag, error) {
	var out []Tag
	for page := 1; page <= maxTagPages; page++ {
		u := fmt.Sprintf(endpoint, url.PathEscape(image)) +
			"?page_size=100&page=" + strconv.Itoa(page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Next    string `json:"next"`
			Results []struct {
				Name    string    `json:"name"`
				Updated time.Time `json:"last_updated"`
			} `json:"results"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s tags: %s", image, resp.Status)
		}
		if err != nil {
			return nil, fmt.Errorf("%s tags: %w", image, err)
		}
		for _, r := range body.Results {
			out = append(out, Tag{Name: r.Name, Updated: r.Updated})
		}
		if body.Next == "" {
			break
		}
	}
	return out, nil
}

// MergeReleases keeps the curated entries the registry still has, and appends
// the release tags nobody has listed yet. An empty or failed fetch leaves the
// curated list exactly as it is.
func MergeReleases(d Distro, tags []Tag) []Release {
	return mergeAt(d, tags, time.Now())
}

func mergeAt(d Distro, tags []Tag, now time.Time) []Release {
	if len(tags) == 0 {
		return d.Releases
	}
	have := make(map[string]bool, len(tags))
	for _, t := range tags {
		have[t.Name] = true
	}

	out := make([]Release, 0, len(d.Releases))
	listed := map[string]bool{}
	for _, r := range d.Releases {
		if !have[r.ImageTag()] {
			continue // pruned from the registry, so it would only fail at pull
		}
		out = append(out, r)
		listed[r.Version] = true
	}

	// Anything numeric and recent that the curated list does not mention yet.
	var fresh []Release
	for _, t := range tags {
		if listed[t.Name] || d.TagRe == nil || !d.TagRe.MatchString(t.Name) {
			continue
		}
		if now.Sub(t.Updated) > freshWindow {
			continue // published long ago and never listed: not a new release
		}
		fresh = append(fresh, Release{
			Version: t.Name, Name: releaseName(d, t.Name), Group: groupFor(out, t.Name, d),
		})
		listed[t.Name] = true
	}
	sort.Slice(fresh, func(i, j int) bool { return newerVersion(fresh[i].Version, fresh[j].Version) })

	// Slot each new entry next to its group, so point releases stay together
	// instead of piling up at the bottom of the picker.
	for _, r := range fresh {
		out = insertInGroup(out, r)
	}
	return out
}

// releaseName reuses the codename a sibling release already carries; a genuinely
// new release just gets its version until somebody labels it.
func releaseName(d Distro, tag string) string {
	major, _, _ := strings.Cut(tag, ".")
	for _, r := range d.Releases {
		if rm, _, _ := strings.Cut(r.Version, "."); rm != major {
			continue
		}
		// "Oracle Linux 8.10" -> "Oracle Linux ", "12 — Bookworm" -> "".
		if prefix, _, ok := strings.Cut(r.Name, r.Version); ok && prefix != "" {
			return prefix + tag
		}
	}
	return tag
}

// groupFor puts a new tag in the group its siblings already use.
func groupFor(listed []Release, tag string, d Distro) string {
	major, _, isPoint := strings.Cut(tag, ".")
	for _, r := range listed {
		rm, _, rIsPoint := strings.Cut(r.Version, ".")
		if rm == major && rIsPoint == isPoint {
			return r.Group
		}
	}
	if isPoint && d.ID != "ubuntu" {
		return d.Name + " " + major + " point releases"
	}
	return "Newly published"
}

func insertInGroup(list []Release, r Release) []Release {
	last := -1
	for i, e := range list {
		if e.Group == r.Group {
			last = i
		}
	}
	if last < 0 {
		return append(list, r)
	}
	// Inside a group the newest release goes first.
	at := last + 1
	for i, e := range list {
		if e.Group == r.Group && newerVersion(r.Version, e.Version) {
			at = i
			break
		}
	}
	list = append(list, Release{})
	copy(list[at+1:], list[at:])
	list[at] = r
	return list
}

// newerVersion compares dotted numeric versions: 13.10 is newer than 13.9.
func newerVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x > y
		}
	}
	return len(as) > len(bs)
}

// live holds what the last successful refresh found, per distro.
var (
	liveMu sync.RWMutex
	live   = map[string][]Release{}
	synced time.Time
)

// All is the picker's source of truth: the curated list, with whatever the last
// refresh learned from the registry merged in.
func All() []Distro {
	liveMu.RLock()
	defer liveMu.RUnlock()
	out := make([]Distro, len(builtin))
	copy(out, builtin)
	for i := range out {
		if rs, ok := live[out[i].ID]; ok && len(rs) > 0 {
			out[i].Releases = rs
		}
	}
	return out
}

// LastSync is when the registry was last read, zero until it succeeds once.
func LastSync() time.Time {
	liveMu.RLock()
	defer liveMu.RUnlock()
	return synced
}

// Refresh reads every distro's tags and merges them in. A distro whose fetch
// fails keeps whatever it had, so a registry outage or an offline host costs
// nothing but a stale list.
func Refresh(ctx context.Context, client *http.Client) error {
	return refreshFrom(ctx, client, hubTags)
}

func refreshFrom(ctx context.Context, client *http.Client, endpoint string) error {
	var failed []string
	found := map[string][]Release{}
	for _, d := range builtin {
		tags, err := fetchTagsFrom(ctx, client, endpoint, d.Image)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d.ID, err))
			continue
		}
		found[d.ID] = MergeReleases(d, tags)
	}

	liveMu.Lock()
	for id, rs := range found {
		live[id] = rs
	}
	if len(found) > 0 {
		synced = time.Now()
	}
	liveMu.Unlock()

	if len(failed) > 0 {
		return fmt.Errorf("release refresh: %s", strings.Join(failed, "; "))
	}
	return nil
}

// tagPattern is what a release tag looks like for these images: a version, and
// for Debian and Oracle Linux an optional point release. Codename tags are
// matched by the curated entries that name them, never added on their own.
var (
	dottedTag = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)
	ubuntuTag = regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}$`)
)
