package main

import (
	"fmt"
	"time"
)

// cmdSync is the default flow: find the comma, download each new drive (resiliently,
// with reconnect), and stitch it ONLY once its download is verified complete — a
// partially transferred drive is left for the next run, never stitched or marked done.
func cmdSync() error {
	defer keepAwake()()
	sweepStaleTemps()

	host, port, cleanup, err := target()
	if err != nil {
		logf("Could not reach the comma (%v) — stitching complete local drives only.", err)
	} else {
		defer cleanup()
		c, derr := dial(host, port, 10*time.Second)
		if derr != nil {
			logf("Could not connect (%v) — stitching complete local drives only.", derr)
		} else {
			defer c.Close()
			logf("==> Comma found at %s", host)
			allFirst := syncAllFirst()
			if allFirst {
				logf("==> Downloading every new drive first; stitching starts after the transfers.")
			}
			for _, d := range listDeviceWith(c) {
				if ledgerHas(d.Route) {
					continue
				}
				if skipLatest() && minAgeSecs() > 0 {
					if newest := remoteNewestMtime(c, d.Route); newest > 0 && time.Now().Unix()-newest < minAgeSecs() {
						logf("==> Skipping %s: still recording (re-run later).", d.Route)
						continue
					}
				}
				logf("==> Downloading %s", d.Route)
				if e := pullRouteResilient(d.Route, host, port); e != nil {
					emit(ProgressEvent{Type: "error", Route: d.Route,
						Message: "download didn't finish — left for the next run: " + e.Error()})
					continue // never stitch a partial drive
				}
				if allFirst {
					continue // stitch later, once every transfer is done
				}
				if e := stitchRoute(d.Route, false); e != nil {
					emit(ProgressEvent{Type: "error", Route: d.Route, Message: e.Error()})
					continue
				}
				ledgerAdd(d.Route)
				maybeCleanChunks(d.Route)
			}
		}
	}

	// Stitch every complete, unprocessed local drive. In all-first mode this is where
	// ALL the stitching happens (after the transfers); in per-drive mode it just picks
	// up leftovers (e.g. drives no longer on the comma). Partially downloaded drives
	// are skipped — never stitched half-finished.
	stitchCompleteLocalUnprocessed()
	emit(ProgressEvent{Type: "done", Message: "Done. Stitched drives are in: " + rootDir()})
	return nil
}

// maybeCleanChunks deletes a route's raw chunks only when CLEAN_RAW is on AND the
// stitched outputs are verified present/playable/complete — so the originals are never
// thrown away before the videos that replace them are confirmed to exist.
func maybeCleanChunks(route string) {
	if !cleanRaw() {
		return
	}
	if !stitchedOutputsOK(route) {
		logf("      keeping raw chunks for %s — stitched outputs not verified yet", route)
		return
	}
	removeRouteChunks(route)
	logf("      raw chunks deleted for %s (outputs verified)", route)
}

func stitchCompleteLocalUnprocessed() {
	for _, route := range localRoutes() {
		if ledgerHas(route) {
			continue
		}
		if !localRouteLooksComplete(route) {
			logf("==> Skipping %s: only partially downloaded — re-run with the comma connected to finish it.", route)
			continue
		}
		if err := stitchRoute(route, false); err != nil {
			emit(ProgressEvent{Type: "error", Route: route, Message: err.Error()})
			continue
		}
		ledgerAdd(route)
		maybeCleanChunks(route)
	}
}

// cmdDownload fetches one drive's chunks (resiliently, verified complete) WITHOUT
// stitching — the UIs use it to run a batch in two phases when "download all first"
// is on: every drive transfers while the comma is reachable, then the restitches run.
func cmdDownload(route string) error {
	defer keepAwake()()
	sweepStaleTemps()

	if host, port, cleanup, err := target(); err == nil {
		defer cleanup()
		onDev := false
		if c, derr := dial(host, port, 12*time.Second); derr == nil {
			onDev = routeOnDevice(c, route)
			c.Close()
		}
		if onDev {
			logf("==> Downloading %s", route)
			if e := pullRouteResilient(route, host, port); e != nil && !localRouteLooksComplete(route) {
				return fmt.Errorf("couldn't finish downloading %s: %w", route, e)
			}
		}
	}
	if len(localSegs(route)) == 0 {
		return fmt.Errorf("no local chunks for %s and the comma isn't reachable", route)
	}
	if !localRouteLooksComplete(route) {
		return fmt.Errorf("%s is only partially downloaded — connect the comma and try again", route)
	}
	emit(ProgressEvent{Type: "log", Route: route, Message: "==> " + route + " downloaded — stitching runs after all transfers"})
	return nil
}

// cmdRestitch re-processes one drive. It first tries to (re)fetch from the comma to fill
// any gaps — this is what lets a partially-synced drive recover instead of being stuck.
// It refuses to stitch a drive that's still incomplete and can't be finished, rather
// than silently producing a partial output.
func cmdRestitch(route string) error {
	defer keepAwake()()
	sweepStaleTemps()

	if host, port, cleanup, err := target(); err == nil {
		defer cleanup()
		onDev := false
		if c, derr := dial(host, port, 12*time.Second); derr == nil {
			onDev = routeOnDevice(c, route)
			c.Close()
		}
		if onDev {
			logf("==> Fetching any missing chunks for %s from the comma…", route)
			if e := pullRouteResilient(route, host, port); e != nil && !localRouteLooksComplete(route) {
				return fmt.Errorf("couldn't finish downloading %s from the comma: %w", route, e)
			}
		}
	}

	if len(localSegs(route)) == 0 {
		return fmt.Errorf("no local chunks for %s and the comma isn't reachable", route)
	}
	if !localRouteLooksComplete(route) {
		return fmt.Errorf("%s is only partially downloaded and the comma isn't reachable to finish it — connect and try again", route)
	}
	if err := stitchRoute(route, false); err != nil {
		return err
	}
	ledgerAdd(route)
	emit(ProgressEvent{Type: "done", Message: "Re-sync complete. Output in: " + rootDir()})
	return nil
}
