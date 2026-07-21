package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// stampDirRe matches an output folder named for a drive's recording start
// (YYYY-MM-DD_HH-MM-SS) — the folders listStitched scans.
var stampDirRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`)

// knownCamLabels are the standard per-camera output labels. The derived outputs
// (combined/vr360/vertical) use other labels, so matching only these picks out exactly
// the individual camera videos we can rebuild from.
var knownCamLabels = []string{"road", "wide", "driver"}

// stitchedCameras returns which per-camera videos (base name, no collision suffix) are
// present and playable in a drive's output folder — i.e. the individual angles we can
// rebuild derived outputs from even when the raw chunks are gone.
func stitchedCameras(stamp string) []string {
	outdir := filepath.Join(rootDir(), stamp)
	var cams []string
	for _, lbl := range knownCamLabels {
		if mp4OK(filepath.Join(outdir, stamp+"__"+lbl+".mp4")) {
			cams = append(cams, lbl)
		}
	}
	return cams
}

// mp4Minutes returns an MP4's rounded length in minutes (≈ its segment count), for
// displaying stitched-only drives in the index where the chunk count is long gone.
func mp4Minutes(path string) int {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nk=1:nw=1", path).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return int(d/60 + 0.5)
}

// rebuildExtrasFromStitched (re)builds the enabled derived outputs (combined / 360 /
// vertical) for a drive we only still have as stitched per-camera videos — the raw
// chunks are gone and it's no longer on the comma. This is what lets you go back and
// make new output types from old drives. Returns true when the drive's individual
// videos were found (so callers treat it as handled), even if nothing was enabled to
// build; false means there's nothing on disk to rebuild from.
func rebuildExtrasFromStitched(stamp string) bool {
	cams := stitchedCameras(stamp)
	if len(cams) == 0 {
		return false
	}
	outdir := filepath.Join(rootDir(), stamp)
	curRoute = stamp // rebuilds are keyed by stamp; tag render progress with it
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	logf("==> %s: rebuilding from stitched videos (%s) — extra outputs: combined=%s · 360=%s · vertical=%s",
		stamp, joinCams(cams), onOff(withCombined()), onOff(with360()), onOff(withVertical()))
	if !withCombined() && !with360() && !withVertical() {
		logf("      nothing to rebuild — turn on a combined / 360 / vertical output first")
		return true
	}
	if withCombined() {
		combineVideo(outdir, stamp, "")
	}
	if with360() {
		equirect360Video(outdir, stamp, "")
	}
	if withVertical() {
		verticalVideo(outdir, stamp, "")
	}
	return true
}

// listStitched finds drives we only have as stitched per-camera videos: output folders
// holding individual angle MP4s whose raw chunks are gone. They can still gain new
// derived outputs (combined/360/vertical), so the index surfaces them too.
func listStitched() []Drive {
	root := rootDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var drives []Drive
	for _, e := range entries {
		if !e.IsDir() || !stampDirRe.MatchString(e.Name()) {
			continue
		}
		stamp := e.Name()
		cams := stitchedCameras(stamp)
		if len(cams) == 0 {
			continue
		}
		outdir := filepath.Join(root, stamp)
		var sizeKB int64
		for _, lbl := range cams {
			if fi, err := os.Stat(filepath.Join(outdir, stamp+"__"+lbl+".mp4")); err == nil {
				sizeKB += fi.Size() / 1024
			}
		}
		audio := hasAudioFile(filepath.Join(outdir, stamp+"__"+cams[0]+".mp4"))
		drives = append(drives, Drive{
			Route: stamp, Stamp: stamp, Cameras: cams, HasAudio: &audio,
			SizeKB: sizeKB, Segments: mp4Minutes(filepath.Join(outdir, stamp+"__"+cams[0]+".mp4")),
			Location: "stitched",
		})
	}
	return drives
}
