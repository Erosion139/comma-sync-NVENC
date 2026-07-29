package main

import (
	"fmt"
	"math"
	"os/exec"
)

// In a composite every angle must show the same instant. Chunk drops, short writes and
// camera start-up skew can leave one camera running behind another, and it's very
// visible: the car reaches a stop sign in one view seconds before the other.
//
// Rather than assume a cause, we measure the offset straight from the footage. Both
// cameras see the same drive, so their MOTION over time is the same signal — it dips to
// nothing at a stop and spikes when turning — even though the views differ. Lining those
// two signals up gives the offset directly, and we shift the road accordingly.

const (
	lagSampleFPS  = 10   // motion is a slow signal; 10/s is plenty and keeps decoding cheap
	lagWindowSecs = 90.0 // how much of the drive to analyse
	lagMaxSecs    = 6.0  // never trust (or apply) an offset larger than this
	lagMinSecs    = 0.15 // below this it isn't worth correcting
	lagMinCorr    = 0.35 // peak must be at least this strong
	lagMinMargin  = 0.06 // ...and this much better than the background correlation
)

// motionSeries returns a per-frame motion measure for a window of a video: how much the
// picture changed from the previous frame, sampled at lagSampleFPS on a tiny greyscale
// version (so it's cheap and ignores the cameras' different fields of view).
func motionSeries(path string, start, dur float64) []float64 {
	const w, h = 64, 40
	cmd := exec.Command("ffmpeg", "-v", "error",
		"-ss", fmt.Sprintf("%.2f", start), "-t", fmt.Sprintf("%.2f", dur),
		"-i", path, "-an",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d,format=gray", lagSampleFPS, w, h),
		"-f", "rawvideo", "-")
	out, err := cmd.Output()
	if err != nil || len(out) < w*h*2 {
		return nil
	}
	n := len(out) / (w * h)
	s := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		a := out[(i-1)*w*h : i*w*h]
		b := out[i*w*h : (i+1)*w*h]
		var sum float64
		for j := range a {
			d := int(a[j]) - int(b[j])
			if d < 0 {
				d = -d
			}
			sum += float64(d)
		}
		s = append(s, sum/float64(w*h))
	}
	return s
}

func normalize(v []float64) []float64 {
	if len(v) == 0 {
		return v
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var sd float64
	for _, x := range v {
		sd += (x - mean) * (x - mean)
	}
	sd = math.Sqrt(sd/float64(len(v))) + 1e-9
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = (x - mean) / sd
	}
	return out
}

func corrAt(a, b []float64, lag int) (float64, bool) {
	n := len(a)
	var x, y []float64
	if lag < 0 {
		x, y = a[-lag:], b[:n+lag]
	} else {
		x, y = a[:n-lag], b[lag:]
	}
	if len(x) < lagSampleFPS*10 { // need at least ~10s of overlap to mean anything
		return 0, false
	}
	var s float64
	for i := range x {
		s += x[i] * y[i]
	}
	return s / float64(len(x)), true
}

// measureCamLag returns how far `camPath` runs BEHIND `widePath`, in seconds (negative =
// ahead), plus whether the measurement is trustworthy. It requires a clear single peak
// standing well above the background, so ambiguous footage (a long straight at constant
// speed, a night scene) simply reports "not confident" and nothing is changed.
//
// It works for the driver camera too: although that films the cabin rather than the
// road, the car's motion still shows up there (shake over bumps, light shifting through
// the windows when turning), which is enough to line the two signals up.
// measureCamLag samples SEVERAL windows spread across the drive and only trusts a result
// when they agree. One window can be fooled by a stretch of repetitive motion, and
// acting on a single bad estimate over-shifts the camera — which is worse than leaving
// it alone.
func measureCamLag(widePath, camPath string) (float64, bool) {
	dur := mp4Duration(widePath)
	if dur < 30 {
		return 0, false
	}
	win := lagWindowSecs
	if win > dur/2 {
		win = dur / 2
	}
	// Spread the samples over the drive, skipping the very start and end. A short drive
	// only has room for one window.
	var fracs []float64
	if dur > 3*win {
		fracs = []float64{0.2, 0.5, 0.8}
	} else {
		fracs = []float64{0.5}
	}
	var got []float64
	for i, f := range fracs {
		emit(ProgressEvent{Type: "progress", Route: curRoute, Phase: "analyze",
			Percent: float64(i) / float64(len(fracs)) * 100, Message: "Checking camera timing"})
		start := dur*f - win/2
		if start < 0 {
			start = 0
		}
		if start+win > dur {
			start = dur - win
		}
		if v, ok := measureWindow(widePath, camPath, start, win); ok {
			got = append(got, v)
		}
	}
	if len(got) == 0 {
		return 0, false
	}
	sortFloats(got)
	med := got[len(got)/2]
	// Every usable window must broadly agree, otherwise the signal isn't trustworthy.
	for _, v := range got {
		if math.Abs(v-med) > 0.5 {
			logf("      timing samples disagree (%s) — leaving this camera alone", fmtLags(got))
			return 0, false
		}
	}
	if len(fracs) > 1 && len(got) < 2 {
		return 0, false // too few usable windows to be confident
	}
	return med, true
}

func sortFloats(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func fmtLags(v []float64) string {
	s := ""
	for i, x := range v {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%+.2fs", x)
	}
	return s
}

// measureWindow does the correlation for one window of the drive.
func measureWindow(widePath, camPath string, start, win float64) (float64, bool) {
	wide := normalize(motionSeries(widePath, start, win))
	cam := normalize(motionSeries(camPath, start, win))
	n := len(wide)
	if len(cam) < n {
		n = len(cam)
	}
	if n < lagSampleFPS*15 {
		return 0, false
	}
	wide, cam = wide[:n], cam[:n]

	maxLag := int(lagMaxSecs * lagSampleFPS)
	best, bestLag := -2.0, 0
	for lag := -maxLag; lag <= maxLag; lag++ {
		if c, ok := corrAt(cam, wide, lag); ok && c > best {
			best, bestLag = c, lag
		}
	}
	// The peak must beat everything outside its immediate neighbourhood, otherwise the
	// signal is too ambiguous (e.g. periodic motion) to act on.
	bg := -2.0
	for lag := -maxLag; lag <= maxLag; lag++ {
		if lag > bestLag-lagSampleFPS/2 && lag < bestLag+lagSampleFPS/2 {
			continue
		}
		if c, ok := corrAt(cam, wide, lag); ok && c > bg {
			bg = c
		}
	}
	if best < lagMinCorr || best-bg < lagMinMargin {
		return 0, false
	}
	// corrAt lines cam[i] up against wide[i+lag], so a camera that is BEHIND matches at a
	// negative lag. Flip it, so the value reads "this camera is this far behind".
	return -float64(bestLag) / lagSampleFPS, true
}

// lagCache keeps one measurement per (wide, camera) pair so repeated renders and the
// vertical/360 outputs share it.
var lagCache = map[string]float64{}

// camLagSecs measures (once) how far a camera runs behind the wide and returns the shift
// to apply. 0 means leave it alone.
func camLagSecs(refPath, camPath, refLabel, label string) float64 {
	key := refPath + "\x00" + camPath
	if v, ok := lagCache[key]; ok {
		return v
	}
	// Cameras stitched in the same run are frame-locked by construction: every segment is
	// padded to the same frame count for every angle (see planFills), so identical length
	// means identical timing and there is nothing to correct. Checking that costs one
	// metadata read, where the correlation below decodes minutes of video to confirm the
	// same zero. Only mismatched lengths — videos that were NOT built together, e.g. one
	// rebuilt separately — can actually be offset, and those still take the full path.
	if a, b := mp4Duration(refPath), mp4Duration(camPath); a > 0 && b > 0 {
		if d := a - b; d < 0.1 && d > -0.1 {
			lagCache[key] = 0
			return 0
		}
		logf("      %s and %s differ in length by %.2fs — checking their timing", refLabel, label, a-b)
	}
	v := 0.0
	if lag, ok := measureCamLag(refPath, camPath); ok && math.Abs(lag) >= lagMinSecs {
		v = lag
		logf("      %s camera runs %+.2fs vs the %s — shifting it back into sync", label, lag, refLabel)
	} else if ok {
		logf("      %s/%s timing: within %.2fs, no shift needed", label, refLabel, lagMinSecs)
	} else {
		logf("      %s/%s timing: not enough distinct motion to measure — left as-is", label, refLabel)
	}
	lagCache[key] = v
	return v
}
