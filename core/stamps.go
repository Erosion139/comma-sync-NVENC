package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// The stamp registry (ROOT/.route_stamps) pins each route's recording-start stamp the
// first time it's known — from the comma itself (authoritative) or computed at stitch
// time. Every later listing and stitch reuses the recorded stamp, so the index shows
// the same time online or offline, it can never drift if chunk-file mtimes are
// imperfect, and it always matches the output folder's name.

func stampsPath() string { return filepath.Join(rootDir(), ".route_stamps") }

func loadStamps() map[string]string {
	m := map[string]string{}
	f, err := os.Open(stampsPath())
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}

// recordStamp pins route→stamp if not already recorded (first writer wins, so the
// device's authoritative stamp can't be replaced by a locally-derived one later).
func recordStamp(route, stamp string) {
	if route == "" || stamp == "" || stamp == route {
		return
	}
	if _, exists := loadStamps()[route]; exists {
		return
	}
	_ = os.MkdirAll(rootDir(), 0o755)
	f, err := os.OpenFile(stampsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(route + " " + stamp + "\n")
}

func recordedStamp(route string) string { return loadStamps()[route] }
