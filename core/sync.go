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
				if e := stitchRoute(d.Route, false); e != nil {
					emit(ProgressEvent{Type: "error", Route: d.Route, Message: e.Error()})
					continue
				}
				ledgerAdd(d.Route)
				if cleanRaw() {
					removeRouteChunks(d.Route)
				}
			}
		}
	}

	// Stitch any complete local drives not handled above (e.g. no longer on the comma).
	// Partially downloaded drives are skipped here too — never stitched half-finished.
	stitchCompleteLocalUnprocessed()
	emit(ProgressEvent{Type: "done", Message: "Done. Stitched drives are in: " + rootDir()})
	return nil
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
		if cleanRaw() {
			removeRouteChunks(route)
		}
	}
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
