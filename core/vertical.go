package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// verticalRenderVer identifies the current vertical renderer. Bump it whenever the
// picture itself changes, so already-rendered files are rebuilt in place instead of
// being skipped as up to date. "3" = tunable narrow feather. The feather width is
// appended to it, so changing the width also forces a rebuild.
const verticalRenderVer = "3"

func verticalRenderTag() string { return verticalRenderVer + "f" + strconv.Itoa(roadFeather()) }

// featherMaskPNG writes a grayscale PNG the size of the scaled road overlay: solid in
// the middle, fading to transparent over `feather` pixels at every edge.
//
// Why: the road and wide lenses sit a few centimetres apart, so how far an object
// shifts between them depends on how CLOSE it is (parallax). Measured on real footage,
// distant scenery lines up to ~0.4px but near objects are off by 6px or more, and no
// single alignment — not even a full perspective warp — can satisfy both at once. With
// a hard rectangular edge, a nearby car crossing the boundary visibly jumps by that
// amount. Fading the edge turns that jump into a short cross-fade instead.
func featherMaskPNG(w, h, feather int) (string, error) {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := x
			for _, v := range []int{w - 1 - x, y, h - 1 - y} {
				if v < d {
					d = v
				}
			}
			a := 255
			if feather > 0 && d < feather {
				a = d * 255 / feather
			}
			img.SetGray(x, y, color.Gray{Y: uint8(a)})
		}
	}
	f, err := os.CreateTemp("", "csync_mask_*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

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
			// Rebuild in place when the file predates the current renderer (e.g. it has
			// the old hard-edged overlay) as well as when its sources changed.
			ver, _ := mp4CommentTag(m, "csync-render=")
			if ver == verticalRenderTag() && outputFresh(m, inputs) {
				logf("      vertical [driver %s] already rendered — skipped re-encode: %s", pos, filepath.Base(m))
				return
			}
			out = m // stale — rebuild it in place
			break
		}
	}
	if out == "" {
		out = freeVariantPath(outdir, stamp, "vertical")
	}
	part := out + ".part"
	os.Remove(part)

	// [0]=wide [1]=driver [2]=road (optional). Sharpen the wide's center with the road
	// overlay first, then stack the panes in the chosen order. The overlay placement is
	// calibrated per drive (parallax/scene depth moves it a few pixels between drives).
	// The microphone is recorded alongside the ROAD camera — qcamera.ts, which the audio
	// is taken from, is the road view — so the sound sits on the ROAD's timeline. That
	// makes the road the reference: move the other two onto it. (Shifting the ROAD
	// instead lines the pictures up with each other but leaves the audio behind, which
	// is exactly what put the singing out of step with the driver.)
	var fc, maskPath string
	top := "[wtop]"
	wideShift, drvShift := "", ""
	if haveRoad {
		if lag := camLagSecs(road, wide, "road", "wide"); lag != 0 {
			wideShift = fmt.Sprintf("setpts=PTS-%.4f/TB,", lag)
		}
		if lag := camLagSecs(road, driver, "road", "driver"); lag != 0 {
			drvShift = fmt.Sprintf("setpts=PTS-%.4f/TB,", lag)
		}
	} else if lag := camLagSecs(wide, driver, "wide", "driver"); lag != 0 {
		drvShift = fmt.Sprintf("setpts=PTS-%.4f/TB,", lag)
	}
	fc = fmt.Sprintf("[0:v]%snull[wd];", wideShift)

	if haveRoad {
		a := calibrateRoadOverlay(wide, road)
		rw, rh := even(1928*a.scale), even(1208*a.scale)
		ox, oy := int(a.x+0.5), int(a.y+0.5)
		// Blend the road in over a soft edge so parallax on nearby objects shows up as a
		// brief cross-fade rather than a hard jump at the boundary. Falls back to the
		// plain hard-edged overlay if the mask can't be written.
		if fw := roadFeather(); fw > 0 {
			if m, err := featherMaskPNG(rw, rh, fw); err == nil {
				maskPath = m
			}
		}
		if maskPath != "" {
			fc += fmt.Sprintf(
				"[2:v]scale=%d:%d,format=yuva420p[rdc];[3:v]format=gray[mk];"+
					"[rdc][mk]alphamerge[rd];[wd][rd]overlay=%d:%d:shortest=1[wtop];",
				rw, rh, ox, oy)
		} else {
			// Hard edge: VERTICAL_FEATHER=0, or the mask couldn't be written.
			fc += fmt.Sprintf("[2:v]scale=%d:%d[rd];[wd][rd]overlay=%d:%d[wtop];",
				rw, rh, ox, oy)
		}
	} else {
		fc += "[wd]null[wtop];"
	}
	fc += fmt.Sprintf("[1:v]%snull[dv];", drvShift)
	if pos == "top" {
		fc += "[dv]" + top + "vstack=inputs=2[v]"
	} else {
		fc += top + "[dv]vstack=inputs=2[v]"
	}

	args := []string{"-y", "-loglevel", "error"}
	for _, p := range inputs {
		if runtime.GOOS == "darwin" {
			args = append(args, "-hwaccel", "videotoolbox")
		}
		args = append(args, "-i", p)
	}
	// The blend mask is a still image, so loop it to cover the whole render. It must be
	// input 3 (after wide/driver/road) to match the filter graph above.
	if maskPath != "" {
		defer os.Remove(maskPath)
		args = append(args, "-loop", "1", "-i", maskPath)
	}
	args = append(args, "-filter_complex", fc, "-map", "[v]", "-map", "0:a?")
	if runtime.GOOS == "darwin" {
		args = append(args, "-c:v", "h264_videotoolbox", "-b:v", "20M")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p")
	}
	// Stamp the driver position so a later run can tell which arrangement this file is.
	args = append(args, "-c:a", "copy", "-movflags", "+faststart",
		"-metadata", "comment=csync-vertical="+pos+";csync-render="+verticalRenderTag(),
		"-f", "mp4", part)

	if err := runFFmpegProgress(args, mp4Duration(wide), 0, "vertical video"); err != nil || !mp4OK(part) {
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
