package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// verticalVideo builds a portrait, phone-friendly composite: the wide camera in one
// pane and the driver camera in the other, stacked vertically at native resolution
// (1928x2416). The sharp road camera is overlaid on the wide pane at its calibrated
// native-space position (444x278 @ 720,462 — the same wide↔road registration the 360
// output uses), so the center of the wide view is crisp and cars cross the overlay
// boundary without jumping. VERTICAL_DRIVER_POS picks whether the driver pane goes on
// the bottom (default — road view on top) or the top.
//
// Same conventions as the other extra outputs: hardware decode on macOS, atomic
// ".part" write validated before rename, and skip when an up-to-date output exists.
// Needs wide + driver; the road overlay is included when that camera exists too.
func verticalVideo(outdir, stamp, suffix string) {
	wide := filepath.Join(outdir, stamp+"__wide"+suffix+".mp4")
	driver := filepath.Join(outdir, stamp+"__driver"+suffix+".mp4")
	road := filepath.Join(outdir, stamp+"__road"+suffix+".mp4")
	if !mp4OK(wide) || !mp4OK(driver) {
		logf("      vertical: needs the wide + driver cameras — skipped for %s", stamp)
		return
	}
	haveRoad := mp4OK(road)

	pos := verticalDriverPos()
	inputs := []string{wide, driver}
	if haveRoad {
		inputs = append(inputs, road)
	}

	// Position-aware, like the combined layouts: each vertical file is stamped with its
	// driver position. Rendering with the OTHER position produces a second file
	// ("__vertical (2).mp4") instead of skipping or overwriting, so you can have one of
	// each in the drive's folder. A same-position file only re-renders if the source
	// videos changed since it was made.
	out := ""
	existing, _ := filepath.Glob(filepath.Join(outdir, stamp+"__vertical*.mp4"))
	for _, m := range existing {
		if v, ok := mp4CommentTag(m, "csync-vertical="); ok && v == pos {
			if outputFresh(m, inputs) {
				logf("      vertical [driver %s] already rendered — skipped re-encode: %s", pos, filepath.Base(m))
				return
			}
			out = m // stale same-position file — rebuild it in place
			break
		}
	}
	if out == "" {
		out = freeVariantPath(outdir, stamp, "vertical")
	}
	part := out + ".part"
	os.Remove(part)

	// [0]=wide [1]=driver [2]=road (optional). Sharpen the wide's center with the road
	// overlay first, then stack the panes in the chosen order.
	var fc string
	top := "[wtop]"
	if haveRoad {
		fc = "[2:v]scale=444:278[rd];[0:v][rd]overlay=720:462[wtop];"
	} else {
		fc = "[0:v]null[wtop];"
	}
	if pos == "top" {
		fc += "[1:v]" + top + "vstack=inputs=2[v]"
	} else {
		fc += top + "[1:v]vstack=inputs=2[v]"
	}

	args := []string{"-y", "-loglevel", "error"}
	for _, p := range inputs {
		if runtime.GOOS == "darwin" {
			args = append(args, "-hwaccel", "videotoolbox")
		}
		args = append(args, "-i", p)
	}
	args = append(args, "-filter_complex", fc, "-map", "[v]", "-map", "0:a?")
	if runtime.GOOS == "darwin" {
		args = append(args, "-c:v", "h264_videotoolbox", "-b:v", "20M")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p")
	}
	// Stamp the driver position so a later run can tell which arrangement this file is.
	args = append(args, "-c:a", "copy", "-movflags", "+faststart",
		"-metadata", "comment=csync-vertical="+pos, "-f", "mp4", part)

	if err := exec.Command("ffmpeg", args...).Run(); err != nil || !mp4OK(part) {
		os.Remove(part)
		emit(ProgressEvent{Type: "error", Message: "vertical video failed for " + stamp})
		return
	}
	os.Rename(part, out)
	tag := "wide+driver"
	if haveRoad {
		tag = "road-sharpened wide + driver"
	}
	logf("      vertical (%s, driver %s): %s", tag, pos, filepath.Base(out))
}
