package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Road→wide overlay registration. The default is the measured average for the comma 3X
// (road ≈ the central 23.03% of the wide, centered slightly left of frame center), but
// the true alignment wanders a few pixels per drive with scene depth and parallax — so
// calibrateRoadOverlay measures it per drive from that drive's own frames and the
// renderers use the measured values, falling back to these defaults if measurement
// isn't confident.
const (
	defRoadScale = 0.2303 // road size as a fraction of the wide, per axis
	defRoadX     = 720.0  // overlay top-left in native wide pixels
	defRoadY     = 462.0
)

type roadAlign struct {
	scale float64 // road->wide scale factor (per axis, native)
	x, y  float64 // overlay top-left, native wide pixels
}

func defaultAlign() roadAlign { return roadAlign{defRoadScale, defRoadX, defRoadY} }

// grayFrame decodes one frame at time t as w×h 8-bit grayscale raw pixels.
func grayFrame(path string, t float64, w, h int) []byte {
	out, err := exec.Command("ffmpeg", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", t), "-i", path, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d", w, h), "-pix_fmt", "gray",
		"-f", "rawvideo", "-").Output()
	if err != nil || len(out) < w*h {
		return nil
	}
	return out[:w*h]
}

// gradMag is a cheap |dx|+|dy| edge map — matching on gradients makes the search
// robust to the exposure/white-balance differences between the two cameras.
func gradMag(px []byte, w, h int) []float32 {
	g := make([]float32, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			dx := int(px[i+1]) - int(px[i-1])
			dy := int(px[i+w]) - int(px[i-w])
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			g[i] = float32(dx + dy)
		}
	}
	return g
}

// resizeGray does bilinear resize of a grayscale float32 image.
func resizeGray(src []float32, sw, sh, dw, dh int) []float32 {
	dst := make([]float32, dw*dh)
	for y := 0; y < dh; y++ {
		fy := (float64(y) + 0.5) * float64(sh) / float64(dh)
		y0 := int(fy)
		if y0 >= sh-1 {
			y0 = sh - 2
		}
		wy := float32(fy - float64(y0))
		for x := 0; x < dw; x++ {
			fx := (float64(x) + 0.5) * float64(sw) / float64(dw)
			x0 := int(fx)
			if x0 >= sw-1 {
				x0 = sw - 2
			}
			wx := float32(fx - float64(x0))
			i := y0*sw + x0
			dst[y*dw+x] = src[i]*(1-wx)*(1-wy) + src[i+1]*wx*(1-wy) +
				src[i+sw]*(1-wx)*wy + src[i+sw+1]*wx*wy
		}
	}
	return dst
}

// matchOne measures the road overlay on one frame pair at half resolution, searching
// scales and a window around the expected position. Returns align + a match score.
func matchOne(widePath, roadPath string, t float64) (roadAlign, float64) {
	const hw, hh = 964, 604 // half native
	wp := grayFrame(widePath, t, hw, hh)
	rp := grayFrame(roadPath, t, hw, hh)
	if wp == nil || rp == nil {
		return roadAlign{}, -1
	}
	W := gradMag(wp, hw, hh)
	R := gradMag(rp, hw, hh)

	// Template: the road frame's central band (avoids the hood and its parallax).
	cx0, cy0 := hw*15/100, hh*20/100
	cw, ch := hw*70/100, hh*52/100
	crop := make([]float32, cw*ch)
	for y := 0; y < ch; y++ {
		copy(crop[y*cw:(y+1)*cw], R[(cy0+y)*hw+cx0:(cy0+y)*hw+cx0+cw])
	}

	best := roadAlign{}
	bestScore := float32(-1)
	for f := 0.212; f <= 0.250; f += 0.002 {
		tw, th := int(float64(cw)*f), int(float64(ch)*f)
		if tw < 8 || th < 8 {
			continue
		}
		tmpl := resizeGray(crop, cw, ch, tw, th)
		var tnorm float32
		for _, v := range tmpl {
			tnorm += v * v
		}
		if tnorm == 0 {
			continue
		}
		// Expected template top-left at this scale (half-res coords).
		ex := (defRoadX + float64(cx0*2)*f) / 2 // cx0 is half-res already; *2 to native, *f, /2 back
		ey := (defRoadY + float64(cy0*2)*f) / 2
		for dy := -12; dy <= 12; dy++ {
			for dx := -16; dx <= 16; dx++ {
				px, py := int(ex)+dx, int(ey)+dy
				if px < 0 || py < 0 || px+tw >= hw || py+th >= hh {
					continue
				}
				var dot, wnorm float32
				for y := 0; y < th; y++ {
					wrow := W[(py+y)*hw+px : (py+y)*hw+px+tw]
					trow := tmpl[y*tw : (y+1)*tw]
					for x := 0; x < tw; x++ {
						dot += wrow[x] * trow[x]
						wnorm += wrow[x] * wrow[x]
					}
				}
				if wnorm == 0 {
					continue
				}
				score := dot * dot / (wnorm * tnorm) // squared cosine similarity
				if score > bestScore {
					bestScore = score
					// Back out the FULL road frame's top-left in native coords.
					nx := float64(px)*2 - float64(cx0*2)*f
					ny := float64(py)*2 - float64(cy0*2)*f
					best = roadAlign{scale: f, x: nx, y: ny}
				}
			}
		}
	}
	return best, float64(bestScore)
}

// alignCache lets the vertical and 360 renders of the same drive share one measurement.
var alignCache = map[string]roadAlign{}

// calibrateRoadOverlay measures the road↔wide registration for THIS drive by matching
// a few frames and taking the median, so the sharp road overlay lands exactly on the
// matching part of the wide view. Falls back to the measured defaults when the match
// isn't confident (night, blank scenes) or lands implausibly far from them.
func calibrateRoadOverlay(widePath, roadPath string) roadAlign {
	if a, ok := alignCache[widePath]; ok {
		return a
	}
	a := calibrateRoadOverlayUncached(widePath, roadPath)
	alignCache[widePath] = a
	return a
}

func calibrateRoadOverlayUncached(widePath, roadPath string) roadAlign {
	dur := 60.0
	if out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", widePath).Output(); err == nil {
		if d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && d > 0 {
			dur = d
		}
	}
	var xs, ys, fs []float64
	for _, frac := range []float64{0.25, 0.5, 0.75} {
		a, score := matchOne(widePath, roadPath, dur*frac)
		if score < 0.15 { // weak/failed match — ignore this frame
			continue
		}
		// plausibility clamp: within a small neighborhood of the known geometry
		if a.scale < 0.212 || a.scale > 0.250 ||
			a.x < defRoadX-30 || a.x > defRoadX+30 ||
			a.y < defRoadY-22 || a.y > defRoadY+22 {
			continue
		}
		xs = append(xs, a.x)
		ys = append(ys, a.y)
		fs = append(fs, a.scale)
	}
	if len(xs) == 0 {
		logf("      road overlay: using default alignment (no confident match)")
		return defaultAlign()
	}
	med := func(v []float64) float64 { sort.Float64s(v); return v[len(v)/2] }
	a := roadAlign{scale: med(fs), x: med(xs), y: med(ys)}
	logf("      road overlay calibrated for this drive: %.0fx%.0f @ (%.0f,%.0f)",
		1928*a.scale, 1208*a.scale, a.x, a.y)
	return a
}

// even rounds to the nearest even int (friendlier for video scalers).
func even(v float64) int {
	n := int(v + 0.5)
	return n - n%2
}
