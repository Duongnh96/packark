// Package store keeps what survives a restart: which bundles were built, what
// went into them, their checksum and their build log. One SQLite file next to
// the bundles themselves, so a rebuild of the same request is a download.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, so the binary still builds with CGO_ENABLED=0
)

type Bundle struct {
	Key       string    `json:"key"`
	Distro    string    `json:"distro"`
	Version   string    `json:"version"`
	Packages  []string  `json:"packages"`
	Repos     []string  `json:"repos,omitempty"`
	Path      string    `json:"-"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Log       string    `json:"log,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Key identifies a request, not a build: the same packages from the same
// repositories for the same release are the same bundle, whatever order they
// were typed in. Extra repositories change what a package name resolves to, so
// they are part of the identity.
func Key(distro, version string, pkgs, repos []string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		distro, version, joinSorted(pkgs), joinSorted(repos),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func joinSorted(in []string) string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return strings.Join(out, " ")
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS bundles (
  key        TEXT PRIMARY KEY,
  distro     TEXT NOT NULL,
  version    TEXT NOT NULL,
  packages   TEXT NOT NULL,
  path       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  log        TEXT NOT NULL,
  created_at TEXT NOT NULL,
  repos      TEXT NOT NULL DEFAULT ''
);`

// Open creates the database, and the directory holding it, if not there yet.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	// One writer at a time; the load here is a handful of rows per build.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	// Databases created before repositories existed are missing the column;
	// SQLite has no ADD COLUMN IF NOT EXISTS, so a duplicate here is the answer.
	if _, err := db.Exec(`ALTER TABLE bundles ADD COLUMN repos TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// dsn turns a filesystem path into a SQLite URI. A Windows path needs the
// slashes flipped and a leading one, or "E:\dir\x.db" parses as a URI scheme.
func dsn(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme:   "file",
		Path:     p,
		RawQuery: "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
	}
	return u.String()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(b Bundle) error {
	_, err := s.db.Exec(
		`INSERT INTO bundles (key, distro, version, packages, path, size, sha256, log, created_at, repos)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   path = excluded.path, size = excluded.size, sha256 = excluded.sha256,
		   log = excluded.log, created_at = excluded.created_at, repos = excluded.repos`,
		b.Key, b.Distro, b.Version, strings.Join(b.Packages, " "), b.Path,
		b.Size, b.SHA256, b.Log, b.CreatedAt.UTC().Format(time.RFC3339),
		encodeRepos(b.Repos))
	return err
}

func (s *Store) Get(key string) (Bundle, bool, error) {
	row := s.db.QueryRow(
		`SELECT key, distro, version, packages, path, size, sha256, log, created_at, repos
		 FROM bundles WHERE key = ?`, key)
	b, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bundle{}, false, nil
	}
	return b, err == nil, err
}

// List returns the newest bundles first, without their logs — the list view
// shows dozens of rows and a log can be a megabyte.
func (s *Store) List(limit int) ([]Bundle, error) {
	rows, err := s.db.Query(
		`SELECT key, distro, version, packages, path, size, sha256, '', created_at, repos
		 FROM bundles ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bundle{}
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Delete(key string) error {
	_, err := s.db.Exec(`DELETE FROM bundles WHERE key = ?`, key)
	return err
}

// Repositories are JSON in the column because a deb822 .sources stanza spans
// lines, which the newline-separated format this started as could not hold.
func encodeRepos(repos []string) string {
	if len(repos) == 0 {
		return ""
	}
	out, err := json.Marshal(repos)
	if err != nil {
		return ""
	}
	return string(out)
}

func decodeRepos(s string) []string {
	if s == "" {
		return nil
	}
	if !strings.HasPrefix(s, "[") {
		return strings.Split(s, "\n") // written before the column held JSON
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{s}
	}
	return out
}

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface{ Scan(...any) error }

func scan(r scanner) (Bundle, error) {
	var b Bundle
	var pkgs, created, repos string
	if err := r.Scan(&b.Key, &b.Distro, &b.Version, &pkgs, &b.Path,
		&b.Size, &b.SHA256, &b.Log, &created, &repos); err != nil {
		return Bundle{}, err
	}
	b.Packages = strings.Fields(pkgs)
	b.Repos = decodeRepos(repos)
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return b, nil
}
