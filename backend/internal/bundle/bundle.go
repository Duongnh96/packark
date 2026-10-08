// Package bundle runs the target release's package manager inside its own
// image, collects the downloaded packages, and records the result in the store.
package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"packark/internal/distro"
	"packark/internal/store"
)

type Status string

const (
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "error"
)

// Kind tells a build job from a search job; both stream a log the same way.
type Kind string

const (
	Build  Kind = "build"
	Lookup Kind = "search"
)

type Job struct {
	ID       string   `json:"id"`
	Kind     Kind     `json:"kind"`
	Key      string   `json:"key"`
	Distro   string   `json:"distro"`
	Version  string   `json:"version"`
	Packages []string `json:"packages"`
	Repos    []string `json:"repos,omitempty"`
	Status   Status   `json:"status"`
	Log      string   `json:"log"`
	Error    string   `json:"error,omitempty"`
	Cached   bool     `json:"cached"`
	Size     int64    `json:"size,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	// Hits is what a search found, once it is done.
	Hits []distro.Package `json:"hits,omitempty"`

	mu     sync.Mutex
	bundle string
}

// Write appends docker's output to the job log; Job is the exec.Cmd sink.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Log += string(p)
	return len(p), nil
}

// Snapshot is a lock-free copy safe to marshal.
func (j *Job) Snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Job{
		ID: j.ID, Kind: j.Kind, Key: j.Key, Distro: j.Distro, Version: j.Version,
		Packages: j.Packages, Repos: j.Repos,
		Status: j.Status, Log: j.Log, Error: j.Error, Cached: j.Cached,
		Size: j.Size, SHA256: j.SHA256, Hits: j.Hits,
	}
}

// BundlePath is the .tar.gz on disk, empty until the job succeeds.
func (j *Job) BundlePath() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.bundle
}

func (j *Job) finish(s Status, path string, size int64, sum string) {
	j.mu.Lock()
	j.Status, j.bundle, j.Size, j.SHA256 = s, path, size, sum
	j.mu.Unlock()
}

// fail records a one-line reason next to the log, so the UI can show what went
// wrong without the user reading a few hundred lines of package manager output.
func (j *Job) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(j, "\nFAILED: %s\n", msg)
	j.mu.Lock()
	j.Status, j.bundle, j.Error = Failed, "", msg
	j.mu.Unlock()
}

type Manager struct {
	dir   string
	store *store.Store
	mu    sync.Mutex
	jobs  map[string]*Job
}

// NewManager stores finished bundles under dir and their metadata in st.
func NewManager(dir string, st *store.Store) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{dir: dir, store: st, jobs: map[string]*Job{}}, nil
}

func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Bundle looks up a finished bundle by key, dropping the row if its file is
// gone — a bundle directory under /tmp does not outlive a reboot.
func (m *Manager) Bundle(key string) (store.Bundle, bool) {
	b, ok, err := m.store.Get(key)
	if err != nil || !ok {
		return store.Bundle{}, false
	}
	if _, err := os.Stat(b.Path); err != nil {
		m.store.Delete(key)
		return store.Bundle{}, false
	}
	return b, true
}

func (m *Manager) List(limit int) ([]store.Bundle, error) { return m.store.List(limit) }

// Forget drops a bundle and its file, so the next build of that request is a
// real build again.
func (m *Manager) Forget(key string) error {
	if b, ok, err := m.store.Get(key); err == nil && ok {
		os.Remove(b.Path)
	}
	return m.store.Delete(key)
}

// Start returns an already finished job when this exact request was built
// before and its file is still on disk; otherwise it builds in the background.
func (m *Manager) Start(d distro.Distro, version string, pkgs []string, repos []distro.Repo) *Job {
	urls := distro.URLs(repos)
	key := store.Key(d.ID, version, pkgs, urls)
	j := &Job{
		ID: fmt.Sprint(time.Now().UnixNano()), Kind: Build, Key: key,
		Distro: d.ID, Version: version, Packages: pkgs, Repos: urls, Status: Running,
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()

	if b, ok := m.Bundle(key); ok {
		j.Log = b.Log + "\nServed from cache: this exact request was already built on " +
			b.CreatedAt.Local().Format("2006-01-02 15:04") + ".\n"
		j.Cached = true
		j.finish(Done, b.Path, b.Size, b.SHA256)
		return j
	}
	go m.run(j, d, version, pkgs, repos)
	return j
}

// run builds the bundle inside the target distro's own image, so its package
// manager resolves dependencies exactly as the target machine would.
func (m *Manager) run(j *Job, d distro.Distro, version string, pkgs []string, repos []distro.Repo) {
	name := "packark-" + j.ID
	defer exec.Command("docker", "rm", "-f", name).Run()

	sh := d.BuildScript(pkgs, repos)
	build := exec.Command("docker", "run", "--name", name,
		"-e", "DEBIAN_FRONTEND=noninteractive",
		d.ImageRef(version), "sh", "-c", sh)
	build.Stdout, build.Stderr = j, j
	if err := build.Run(); err != nil {
		// "exit status 100" tells the user nothing; a rejected package name does.
		if missing := distro.NotFound(j.Snapshot().Log); len(missing) > 0 {
			j.fail("%s %s has no package named %s. Search for the name that release "+
				"actually ships before building.", d.Name, version, strings.Join(missing, ", "))
			return
		}
		j.fail("build failed: %v", err)
		return
	}

	out := filepath.Join(m.dir, j.Key+".tar.gz")
	cp := exec.Command("docker", "cp", name+":/bundle.tar.gz", out)
	cp.Stdout, cp.Stderr = j, j
	if err := cp.Run(); err != nil {
		j.fail("could not copy the bundle out of the container: %v", err)
		return
	}

	size, sum, err := checksum(out)
	if err != nil {
		j.fail("could not read the finished bundle: %v", err)
		return
	}
	fmt.Fprintf(j, "\nBundle ready: %s, sha256 %s\n", HumanSize(size), sum)
	j.finish(Done, out, size, sum)

	if err := m.store.Save(store.Bundle{
		Key: j.Key, Distro: d.ID, Version: version, Packages: pkgs, Repos: j.Repos,
		Path: out, Size: size, SHA256: sum, Log: j.Snapshot().Log, CreatedAt: time.Now(),
	}); err != nil {
		// The bundle is on disk and downloadable; only the reuse shortcut is lost.
		fmt.Fprintf(j, "Warning: could not record this bundle: %v\n", err)
	}
}

// searchTimeout covers a cold image pull plus a metadata refresh.
const searchTimeout = 3 * time.Minute

// StartSearch runs a search as a background job. A first search of a release
// pulls the image and refreshes the package manager metadata, which is minutes
// of work with nothing to look at, so the container's progress is streamed into
// the job log exactly like a build's.
func (m *Manager) StartSearch(d distro.Distro, version, term string, repos []distro.Repo) *Job {
	j := &Job{
		ID: fmt.Sprint(time.Now().UnixNano()), Kind: Lookup,
		Distro: d.ID, Version: version, Repos: distro.URLs(repos),
		Packages: []string{term}, Status: Running,
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	go m.search(j, d, version, term, repos)
	return j
}

func (m *Manager) search(j *Job, d distro.Distro, version, term string, repos []distro.Repo) {
	ref := d.ImageRef(version)
	if ref == "" {
		j.fail("unknown %s release %q", d.Name, version)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()

	sh := d.SearchScript(term, repos)
	// Metadata is cached per repository set: a different set is a different view
	// of what exists, and must not be answered from the plain release's cache.
	vol := "packark-meta-" + d.ID + "-" + version
	if urls := distro.URLs(repos); len(urls) > 0 {
		vol += "-" + store.Key(d.ID, version, nil, urls)
	}
	fmt.Fprintf(j, "%s %s: looking for %q\n", d.Name, version, term)

	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-e", "DEBIAN_FRONTEND=noninteractive",
		"-v", vol+":"+d.CacheDir,
		ref, "sh", "-c", sh)
	// The script keeps its progress on stderr and prints only hits on stdout,
	// so the log can stream while the results stay parseable.
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, j
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			j.fail("search timed out after %s", searchTimeout)
			return
		}
		j.fail("%s %s search failed: %s", d.Name, version, lastLine(j.Snapshot().Log))
		return
	}

	hits := distro.ParseSearch(out.String())
	j.mu.Lock()
	j.Hits, j.Status = hits, Done
	j.mu.Unlock()
	fmt.Fprintf(j, "%d package(s) matched.\n", len(hits))
}

// checksum is what the user verifies on the airgapped side before installing.
func checksum(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// lastLine keeps an error one line long; docker and apt are both chatty.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return "no output"
}
