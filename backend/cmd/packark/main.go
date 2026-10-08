package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"packark/internal/api"
	"packark/internal/bundle"
	"packark/internal/distro"
	"packark/internal/store"
)

// refreshEvery is how often the release lists are re-read from the registry.
// Releases ship on the order of months, so this is about not going stale for
// weeks, not about being current to the minute.
const refreshEvery = 12 * time.Hour

func main() {
	addr := envOr("ADDR", ":8080")
	dir := envOr("BUNDLE_DIR", filepath.Join(os.TempDir(), "packark"))

	st, err := store.Open(filepath.Join(dir, "packark.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	m, err := bundle.NewManager(dir, st)
	if err != nil {
		log.Fatal(err)
	}
	go refreshReleases()

	log.Printf("packark on http://localhost%s (bundles in %s)", addr, dir)
	log.Fatal(http.ListenAndServe(addr, api.Handler(m)))
}

// refreshReleases keeps the picker in step with the registry: releases that
// were published appear, ones whose tags were pruned stop being offered. A
// failure just leaves the built-in list in place, which is what an airgapped
// host or a rate-limited registry gets.
func refreshReleases() {
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := distro.Refresh(ctx, client)
		cancel()
		if err != nil {
			log.Printf("release list: %v (keeping the built-in list)", err)
		} else {
			log.Printf("release list refreshed from the registry")
		}
		time.Sleep(refreshEvery)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
