package distro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func names(rs []Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func find(rs []Release, v string) (Release, bool) {
	for _, r := range rs {
		if r.Version == v {
			return r, true
		}
	}
	return Release{}, false
}

// curated turns the built-in list into tags a registry would report, all
// pushed recently, optionally leaving one out.
func curated(d Distro, drop string) []Tag {
	var out []Tag
	for _, r := range d.Releases {
		if r.ImageTag() == drop {
			continue
		}
		out = append(out, Tag{Name: r.ImageTag(), Updated: time.Now()})
	}
	return out
}

func TestMergeDropsPrunedTags(t *testing.T) {
	d, _ := Get("debian")
	// Debian prunes old point releases; 12.12 is the kind that goes first.
	tags := curated(d, "12.12")
	got := MergeReleases(d, tags)
	if _, ok := find(got, "12.12"); ok {
		t.Error("a tag the registry no longer has must leave the picker")
	}
	if _, ok := find(got, "12.13"); !ok {
		t.Error("the rest of the curated list must survive")
	}
	if len(got) != len(d.Releases)-1 {
		t.Errorf("only the pruned entry should go: %d -> %d", len(d.Releases), len(got))
	}
}

func TestMergeAddsNewTags(t *testing.T) {
	d, _ := Get("debian")
	tags := append(curated(d, ""),
		Tag{Name: "13.8", Updated: time.Now()},
		Tag{Name: "12.16", Updated: time.Now()})
	got := MergeReleases(d, tags)

	r, ok := find(got, "13.8")
	if !ok {
		t.Fatal("a new point release must appear")
	}
	if r.Group != "Debian 13 point releases" {
		t.Errorf("new release must join its siblings' group, got %q", r.Group)
	}
	if r.Name != "Debian 13.8" {
		t.Errorf("name should follow its siblings, got %q", r.Name)
	}
	// Newest first inside the group, so 13.8 lands above 13.7.
	list := names(got)
	if idx(list, "13.8") > idx(list, "13.7") {
		t.Errorf("new point release must sort above the older ones: %v", list)
	}
	if idx(list, "12.16") > idx(list, "12.15") {
		t.Errorf("12.16 must sort above 12.15: %v", list)
	}
}

func TestMergeIgnoresNonReleaseTags(t *testing.T) {
	d, _ := Get("debian")
	tags := curated(d, "")
	for _, junk := range []string{"latest", "sid", "testing", "13-slim", "bookworm-20240101", "unstable"} {
		tags = append(tags, Tag{Name: junk, Updated: time.Now()})
	}
	got := MergeReleases(d, tags)
	if len(got) != len(d.Releases) {
		t.Errorf("only version tags are releases, got %v", names(got))
	}

	// Ubuntu has no point-release tags, so a dotted-but-wrong tag stays out.
	u, _ := Get("ubuntu")
	utags := curated(u, "")
	for _, junk := range []string{"24.04.1", "devel", "rolling"} {
		utags = append(utags, Tag{Name: junk, Updated: time.Now()})
	}
	if got := MergeReleases(u, utags); len(got) != len(u.Releases) {
		t.Errorf("ubuntu picked up a tag it should not: %v", names(got))
	}
}

func TestMergeKeepsCuratedListWhenFetchIsEmpty(t *testing.T) {
	for _, d := range All() {
		if got := MergeReleases(d, nil); len(got) != len(d.Releases) {
			t.Errorf("%s: an empty fetch must change nothing", d.ID)
		}
	}
}

// A codename-only entry (Debian archives buster) must survive as long as the
// registry still has that tag, and disappear when it does not.
func TestMergeHandlesCodenameTags(t *testing.T) {
	d, _ := Get("debian")
	withBuster, withoutBuster := curated(d, ""), curated(d, "buster")
	if _, ok := find(MergeReleases(d, withBuster), "10"); !ok {
		t.Error("buster is tagged, so Debian 10 stays")
	}
	if _, ok := find(MergeReleases(d, withoutBuster), "10"); ok {
		t.Error("buster is gone, so Debian 10 must go")
	}
}

func TestFetchTagsPaginates(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		body := map[string]any{"next": "", "results": []map[string]string{{"name": "13"}}}
		if r.URL.Query().Get("page") == "1" {
			body["next"] = "more"
			body["results"] = []map[string]string{{"name": "13.7"}, {"name": "latest"}}
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	got, err := fetchTagsFrom(context.Background(), srv.Client(), srv.URL+"/%s/tags", "debian")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "13.7" || got[2].Name != "13" {
		t.Errorf("both pages should be read in order, got %+v", got)
	}
	if pages != 2 {
		t.Errorf("should stop once next is empty, read %d pages", pages)
	}
}

func TestFetchTagsReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	if _, err := fetchTagsFrom(context.Background(), srv.Client(), srv.URL+"/%s/tags", "debian"); err == nil {
		t.Error("a rate-limited registry must be an error, not an empty list")
	}
}

// A refresh that cannot reach the registry must leave the picker working.
func TestRefreshFailureKeepsTheCuratedList(t *testing.T) {
	before := len(All()[0].Releases)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := refreshFrom(ctx, srv.Client(), srv.URL+"/%s/tags"); err == nil {
		t.Error("a failed refresh must report the failure")
	}
	if got := len(All()[0].Releases); got != before {
		t.Errorf("releases changed after a failed refresh: %d -> %d", before, got)
	}
	if !LastSync().IsZero() {
		t.Error("a failed refresh must not count as a sync")
	}
}

func idx(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// A registry lists every tag it ever published. Version order cannot tell a
// release that shipped last month from one that died years ago — oraclelinux:6
// is numerically plausible and was last pushed in 2021 — so only recently
// published tags are added.
func TestMergeIgnoresLongDeadTags(t *testing.T) {
	d, _ := Get("oraclelinux")
	now := time.Now()
	tags := curated(d, "")
	tags = append(tags,
		Tag{Name: "6", Updated: now.AddDate(-5, 0, 0)},
		Tag{Name: "6.10", Updated: now.AddDate(-5, 0, 0)},
		Tag{Name: "5.11", Updated: now.AddDate(-9, 0, 0)},
		Tag{Name: "8.11", Updated: now.AddDate(0, -1, 0)},
	)
	got := mergeAt(d, tags, now)

	for _, dead := range []string{"6", "6.10", "5.11"} {
		if _, ok := find(got, dead); ok {
			t.Errorf("%s was last pushed years ago and must stay out", dead)
		}
	}
	r, ok := find(got, "8.11")
	if !ok {
		t.Fatal("a point release published last month must appear")
	}
	if r.Group != "Oracle Linux 8 point releases" {
		t.Errorf("8.11 belongs with the other 8.x, got %q", r.Group)
	}
	if list := names(got); idx(list, "8.11") > idx(list, "8.10") {
		t.Errorf("8.11 must sort above 8.10: %v", list)
	}
}

// Ubuntu numbers releases by date, so 20.10 is "newer" than the 20.04 LTS while
// being long dead. Only the date keeps it out.
func TestMergeIgnoresDeadUbuntuInterims(t *testing.T) {
	u, _ := Get("ubuntu")
	now := time.Now()
	tags := append(curated(u, ""),
		Tag{Name: "20.10", Updated: now.AddDate(-5, 0, 0)},
		Tag{Name: "19.10", Updated: now.AddDate(-6, 0, 0)},
		Tag{Name: "26.10", Updated: now.AddDate(0, 0, -5)},
	)
	got := mergeAt(u, tags, now)
	for _, dead := range []string{"20.10", "19.10"} {
		if _, ok := find(got, dead); ok {
			t.Errorf("%s is long dead and must not be offered", dead)
		}
	}
	if _, ok := find(got, "26.10"); !ok {
		t.Error("a release published this month must appear")
	}
}
