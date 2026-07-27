package main

import (
	"fmt"
	"os/exec"
)

// The microphone gap is read from qcamera.ts's packet timestamps. Those timestamps only
// describe qcamera's OWN streams, though — they say nothing about where that file sits
// relative to the camera chunk the video is actually built from. If qcamera's video
// stream begins later than the chunk (its encoder came up late, or the file was written
// that way), then a gap measured against it is short by exactly that much, and the sound
// ends up that far ahead of the picture for the entire drive.
//
// So rather than trust the container, match the PICTURES: qcamera.ts is a low-resolution
// copy of the road camera, so its motion over time is the same signal as the chunk's.
// Lining those up ties the audio to the real camera timeline. On good footage this
// matches at ~0.99 correlation, and it costs one cheap decode of each.

// motionOf builds a per-frame motion series for a video. rawHEVC forces an input frame
// rate, which a bare .hevc chunk needs because it carries no timing of its own.
func motionOf(path string, rawHEVC bool) []float64 {
	args := []string{"-v", "error"}
	if rawHEVC {
		args = append(args, "-framerate", fps())
	}
	args = append(args, "-i", path, "-an",
		"-vf", fmt.Sprintf("fps=%d,scale=64:40,format=gray", lagSampleFPS),
		"-f", "rawvideo", "-")
	out, err := exec.Command("ffmpeg", args...).Output()
	const px = 64 * 40
	if err != nil || len(out) < px*2 {
		return nil
	}
	n := len(out) / px
	s := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		a := out[(i-1)*px : i*px]
		b := out[i*px : (i+1)*px]
		var sum float64
		for j := range a {
			d := int(a[j]) - int(b[j])
			if d < 0 {
				d = -d
			}
			sum += float64(d)
		}
		s = append(s, sum/float64(px))
	}
	return normalize(s)
}

// qcamChunkOffset returns how many seconds later qcamera.ts's video starts than the
// camera chunk recorded beside it, so a gap measured inside qcamera.ts can be expressed
// on the chunk's timeline: chunkTime = qcamTime + offset.
//
// Returns 0 (change nothing) unless the two clearly match — a weak or ambiguous match
// means the footage can't support the measurement, and guessing would be worse than
// leaving the gap as the timestamps reported it.
func qcamChunkOffset(qts, chunk string) float64 {
	q := motionOf(qts, false)
	c := motionOf(chunk, true)
	n := len(q)
	if len(c) < n {
		n = len(c)
	}
	if n < lagSampleFPS*10 {
		return 0
	}
	q, c = q[:n], c[:n]

	maxLag := int(lagMaxSecs * lagSampleFPS)
	best, bestLag := -2.0, 0
	for lag := -maxLag; lag <= maxLag; lag++ {
		if v, ok := corrAt(q, c, lag); ok && v > best {
			best, bestLag = v, lag
		}
	}
	// Demand a strong, clear match: this shifts the whole drive's audio, so a shaky
	// measurement must not be acted on.
	if best < 0.5 {
		return 0
	}
	bg := -2.0
	for lag := -maxLag; lag <= maxLag; lag++ {
		if lag > bestLag-lagSampleFPS/2 && lag < bestLag+lagSampleFPS/2 {
			continue
		}
		if v, ok := corrAt(q, c, lag); ok && v > bg {
			bg = v
		}
	}
	if best-bg < 0.1 {
		return 0
	}
	return float64(bestLag) / lagSampleFPS
}
