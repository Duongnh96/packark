// Package api serves the JSON endpoints and the built Vue frontend.
package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"packark/internal/bundle"
	"packark/internal/distro"
)

// dist is filled by `npm run build` in ../../../frontend. Only .gitkeep is committed,
// so a binary built without the frontend still compiles and says so at /.
//
//go:embed all:dist
var dist embed.FS

func Handler(m *bundle.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/distros", func(w http.ResponseWriter, r *http.Request) {
		// Says whether this list came from the registry or is the built-in one.
		if t := distro.LastSync(); !t.IsZero() {
			w.Header().Set("X-Releases-Synced", t.UTC().Format(time.RFC3339))
		}
		writeJSON(w, distro.All())
	})
	// Search is a POST because it carries the extra repositories: the same term
	// against a vendor repo is a different question.
	mux.HandleFunc("POST /api/search", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Distro  string        `json:"distro"`
			Version string        `json:"version"`
			Query   string        `json:"q"`
			Repos   []distro.Repo `json:"repos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		d, err := target(req.Distro, req.Version, req.Repos)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := distro.ValidTerm(req.Query); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// A search is a job like a build: the first one for a release pulls an
		// image and refreshes package metadata, so the caller polls and watches.
		writeJSON(w, map[string]string{"id": m.StartSearch(d, req.Version, req.Query, req.Repos).ID})
	})
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Distro   string        `json:"distro"`
			Version  string        `json:"version"`
			Packages []string      `json:"packages"`
			Repos    []distro.Repo `json:"repos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		d, err := target(req.Distro, req.Version, req.Repos)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := distro.ValidPackages(req.Packages); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"id": m.Start(d, req.Version, req.Packages, req.Repos).ID})
	})
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j := m.Get(r.PathValue("id"))
		if j == nil {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		s := j.Snapshot()
		writeJSON(w, &s)
	})
	// Bundles are addressed by request key, not by job id: the job is gone after
	// a restart, the bundle is not.
	mux.HandleFunc("GET /api/bundles", func(w http.ResponseWriter, r *http.Request) {
		// ponytail: whole list, filtered and paged in the browser. Each row is a
		// tarball on disk, so the count stays small; page in SQL if it ever doesn't.
		list, err := m.List(5000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	})
	mux.HandleFunc("GET /api/bundles/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, ok := m.Bundle(r.PathValue("key"))
		if !ok {
			http.Error(w, "no such bundle", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=packark-%s-%s-%s.tar.gz", b.Distro, b.Version, b.Key))
		http.ServeFile(w, r, b.Path)
	})
	mux.HandleFunc("DELETE /api/bundles/{key}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Forget(r.PathValue("key")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", frontend())
	return mux
}

// target validates the three things every request names: which distribution,
// which release, and which extra repositories may be enabled inside it.
func target(id, version string, repos []distro.Repo) (distro.Distro, error) {
	d, ok := distro.Get(id)
	if !ok {
		return distro.Distro{}, fmt.Errorf("unknown distro")
	}
	if !d.HasVersion(version) {
		return distro.Distro{}, fmt.Errorf("unknown %s version", d.Name)
	}
	if err := distro.ValidRepos(d, repos); err != nil {
		return distro.Distro{}, err
	}
	return d, nil
}

func frontend() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			http.Error(w, "frontend not built: run `npm ci && npm run build` in frontend/",
				http.StatusNotImplemented)
			return
		}
		// Single-page app: unknown non-asset paths fall back to index.html.
		if !strings.Contains(r.URL.Path, ".") {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
