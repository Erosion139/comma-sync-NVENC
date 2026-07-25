package main

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// clockSkewWindow — the comma boots with a stale RTC and only corrects once it gets a
// time fix, so the FIRST segment of a drive is sometimes written months in the past.
// A real drive is contiguous and at most a few hours long, so any timestamp more than
// this far before the newest one is pre-sync garbage, not the recording start.
const clockSkewWindow = 24 * 60 * 60

// earliestSaneMtime returns the earliest timestamp that isn't pre-clock-sync garbage:
// the smallest value within clockSkewWindow of the newest. On a healthy drive that is
// simply the minimum (behavior unchanged); on a drive whose first segment predated the
// clock sync it skips that outlier, which would otherwise date the whole drive months
// early and bury it at the bottom of the index.
func earliestSaneMtime(mts []int64) int64 {
	if len(mts) == 0 {
		return 0
	}
	s := append([]int64(nil), mts...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	newest := s[len(s)-1]
	for _, m := range s {
		if newest-m < clockSkewWindow {
			return m
		}
	}
	return newest
}

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

// pinDeviceStamp records the device's stamp and, unlike plain recordStamp, REPAIRS a
// previously pinned stamp that is clearly pre-clock-sync garbage — i.e. the pin is more
// than a day OLDER than what the comma reports now. Without this a drive mis-dated by a
// stale RTC stays mis-dated forever (first writer wins), sorting to the bottom of the
// index as if it were missing. Ordinary small differences never repin: that's the whole
// point of pinning. It also refuses to repin once something has been stitched under the
// old stamp, so an existing output folder is never orphaned.
func pinDeviceStamp(route, stamp string) {
	old, exists := loadStamps()[route]
	if !exists {
		recordStamp(route, stamp)
		return
	}
	if old == stamp {
		return
	}
	ot, e1 := time.Parse("2006-01-02_15-04-05", old)
	nt, e2 := time.Parse("2006-01-02_15-04-05", stamp)
	if e1 != nil || e2 != nil {
		return
	}
	if nt.Sub(ot) < clockSkewWindow*time.Second {
		return // normal drift — keep the pin
	}
	if _, err := os.Stat(filepath.Join(rootDir(), old)); err == nil {
		return // already stitched under the old stamp; don't orphan that folder
	}
	rewriteStamp(route, stamp)
	logf("      re-dated %s: %s -> %s (its first segment predated the comma's clock sync)",
		route, old, stamp)
}

// rewriteStamp replaces one route's pin, rewriting the registry atomically.
func rewriteStamp(route, stamp string) {
	m := loadStamps()
	m[route] = stamp
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + " " + m[k] + "\n")
	}
	tmp := stampsPath() + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o644) == nil {
		_ = os.Rename(tmp, stampsPath())
	}
}
