package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// mp4CommentTag returns the value after `prefix` in an MP4's `comment` metadata tag —
// we stamp per-camera videos with "csync-segs=N" and combined videos with
// "csync-layout=..." — or ("", false) if it isn't there.
func mp4CommentTag(path, prefix string) (string, bool) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=comment",
		"-of", "default=nk=1:nw=1", path).Output()
	if err != nil {
		return "", false
	}
	// The comment may carry several ";"-separated fields (e.g.
	// "csync-vertical=bottom;csync-render=2"); match any of them. A single-field
	// comment written by an older version still works.
	for _, f := range strings.Split(strings.TrimSpace(string(out)), ";") {
		if f = strings.TrimSpace(f); strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix), true
		}
	}
	return "", false
}

// individualSegs returns the segment count a per-camera video was stitched from (its
// "csync-segs=N" tag), or -1 if untagged. Lets a reuse check notice that a drive has
// since had more segments downloaded, so a stale/partial output gets re-stitched
// instead of reused.
func individualSegs(path string) int {
	if v, ok := mp4CommentTag(path, "csync-segs="); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return -1
}

// outputFresh reports whether the output at `out` exists and is at least as new as
// every input — used so a combined/360 video is rebuilt when its source per-camera
// videos have been re-stitched (e.g. after more chunks were downloaded).
func outputFresh(out string, inputs []string) bool {
	oi, err := os.Stat(out)
	if err != nil {
		return false
	}
	for _, in := range inputs {
		if ii, err := os.Stat(in); err == nil && ii.ModTime().After(oi.ModTime()) {
			return false
		}
	}
	return true
}

// stitchedOutputsOK verifies a route's stitched per-camera videos actually exist, are
// playable, and were built from every segment currently on disk. This is the gate for
// deleting raw chunks: chunks are only removed once the outputs they'd be needed to
// re-create are confirmed present and complete.
func stitchedOutputsOK(route string) bool {
	segs := localSegs(route)
	if len(segs) == 0 {
		return false
	}
	cams := camerasOf(segs)
	if len(cams) == 0 {
		return false
	}
	stamp := routeStamp(route, segs)
	outdir := filepath.Join(rootDir(), stamp)
	for _, cam := range cams {
		p := filepath.Join(outdir, stamp+"__"+labelFor(cam)+".mp4")
		if !mp4OK(p) || individualSegs(p) != len(segs) {
			return false
		}
	}
	return true
}

// localRouteLooksComplete is the offline completeness heuristic, used only when the
// comma can't be reached to confirm against it. A mid-transfer drop leaves the last
// segment missing some cameras (or a gap in the segment numbering), so we require:
// at least one segment, contiguous segment numbers, and every segment carrying the
// same non-empty set of camera .hevc files with no zero-byte files. The online path
// verifies file-for-file against the device instead (see pullRoute).
func localRouteLooksComplete(route string) bool {
	segs := localSegs(route) // sorted ascending by segment number
	if len(segs) == 0 {
		return false
	}
	var want map[string]bool
	for i, s := range segs {
		if i > 0 && segNum(segs[i]) != segNum(segs[i-1])+1 {
			return false // gap in the segment numbering
		}
		cams := map[string]bool{}
		files, _ := os.ReadDir(filepath.Join(chunksDir(), s))
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".hevc") {
				continue
			}
			info, err := f.Info()
			if err != nil || info.Size() == 0 {
				return false
			}
			cams[f.Name()] = true
		}
		if len(cams) == 0 {
			return false
		}
		if i == 0 {
			want = cams
			continue
		}
		if len(cams) != len(want) {
			return false
		}
		for c := range want {
			if !cams[c] {
				return false
			}
		}
	}
	return true
}
