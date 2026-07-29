package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// A drive is stored as ~1-minute chunks per camera. Now and then a camera's chunk is
// missing, or was written short. Simply concatenating whatever is present makes THAT
// camera's video shorter than the others, and since each angle is stitched
// independently, everything after the gap shows the angles at DIFFERENT MOMENTS — the
// same stop sign or car appears twice in a composite.
//
// So instead of dropping anything, we hold the camera's last good frame for exactly as
// long as its footage is missing. Every angle then ends up the same length, stays
// frame-aligned throughout, and picks straight back up when the chunks resume.

// segGap is one camera's shortfall within one segment.
type segGap struct {
	seg    string
	cam    string
	frames int
}

// hevcGeom returns the pixel size of a raw HEVC chunk (0,0 if unreadable).
func hevcGeom(path string) (int, int) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, 0
	}
	f := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(f) < 2 {
		return 0, 0
	}
	w, _ := strconv.Atoi(f[0])
	h, _ := strconv.Atoi(f[1])
	return w, h
}

// fitPCM returns a PCM path of exactly `want` bytes: the original when it already
// matches, otherwise a temp copy cut or silence-padded at the end. The second return
// value removes that temp file (nil when the original was reused).
func fitPCM(path string, want int64) (string, func()) {
	fi, err := os.Stat(path)
	if err != nil || want <= 0 || fi.Size() == want {
		return path, nil
	}
	f, err := os.CreateTemp("", "comma_fit_*.pcm")
	if err != nil {
		return path, nil
	}
	name := f.Name()
	src, err := os.Open(path)
	if err != nil {
		f.Close()
		os.Remove(name)
		return path, nil
	}
	n := want
	if fi.Size() < want {
		n = fi.Size()
	}
	if _, err := io.CopyN(f, src, n); err != nil {
		src.Close()
		f.Close()
		os.Remove(name)
		return path, nil
	}
	src.Close()
	if pad := want - n; pad > 0 {
		buf := make([]byte, 32*1024)
		for pad > 0 {
			c := int64(len(buf))
			if pad < c {
				c = pad
			}
			if _, err := f.Write(buf[:c]); err != nil {
				break
			}
			pad -= c
		}
	}
	f.Close()
	return name, func() { os.Remove(name) }
}

// planFills works out how many frames each segment should have (the longest camera
// wins) and how far each camera falls short.
//
// It counts EVERY camera in EVERY segment. A cheaper "only look at segments that seem
// off" shortcut misses small differences — a couple of frames in a minute is invisible
// to any size check — and because each camera is concatenated independently those
// differences add up, segment after segment, into audible drift by the end of a long
// drive. Counting everything is the only way to guarantee all angles, and the audio
// built alongside them, share one timeline.
func planFills(segs, cams []string) (ref map[string]int, have map[string]map[string]int, gaps []segGap) {
	ref = map[string]int{}
	have = map[string]map[string]int{}

	// Counting frames means reading each chunk end to end (a raw .hevc carries no index),
	// which is I/O-bound and independent per file — so run them concurrently. Same exact
	// counts, a fraction of the wall time, and it matters most on an external drive.
	type job struct{ seg, cam string }
	var jobs []job
	for _, s := range segs {
		for _, c := range cams {
			jobs = append(jobs, job{s, c})
		}
	}
	counts := make([]int, len(jobs))
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	next := make(chan int)
	var done int64
	go func() {
		for i := range jobs {
			next <- i
		}
		close(next)
	}()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				p := filepath.Join(chunksDir(), jobs[i].seg, jobs[i].cam)
				if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
					counts[i] = countPackets(p)
				}
				n := atomic.AddInt64(&done, 1)
				emit(ProgressEvent{Type: "progress", Route: curRoute, Phase: "analyze",
					Percent: float64(n) / float64(len(jobs)) * 100, Message: "Checking chunk lengths"})
			}
		}()
	}
	wg.Wait()

	// Assemble in segment order so the result (and any warning) is deterministic.
	idx := map[job]int{}
	for i, j := range jobs {
		idx[j] = i
	}
	for _, s := range segs {
		per := map[string]int{}
		longest := 0
		for _, c := range cams {
			n := counts[idx[job{s, c}]]
			per[c] = n
			if n > longest {
				longest = n
			}
		}
		if longest == 0 {
			continue // nothing usable in this segment for any camera
		}
		ref[s] = longest
		have[s] = per
		for _, c := range cams {
			if d := longest - per[c]; d > 0 {
				gaps = append(gaps, segGap{seg: s, cam: c, frames: d})
			}
		}
	}
	return
}

// reportGaps writes the dropped-chunk warning to the log, grouped per camera.
func reportGaps(gaps []segGap) {
	if len(gaps) == 0 {
		return
	}
	byCam := map[string][]segGap{}
	order := []string{}
	for _, g := range gaps {
		l := labelFor(g.cam)
		if _, seen := byCam[l]; !seen {
			order = append(order, l)
		}
		byCam[l] = append(byCam[l], g)
	}
	logf("      !! dropped chunks detected — some frames will be FROZEN to keep the angles in sync:")
	for _, lbl := range order {
		gs := byCam[lbl]
		total := 0
		segsTxt := make([]string, 0, len(gs))
		for _, g := range gs {
			total += g.frames
			segsTxt = append(segsTxt, fmt.Sprintf("%d (%.1fs)", segNum(g.seg), float64(g.frames)/fpsFloat()))
		}
		logf("         %s: %.1fs missing across %d segment(s) — minute %s",
			lbl, float64(total)/fpsFloat(), len(gs), strings.Join(segsTxt, ", "))
	}
	logf("         the affected camera holds its last frame for those stretches, then resumes.")
}

// lastFrameStill writes a chunk's final frame to a PNG — the image we hold during a gap.
func lastFrameStill(path string, frames int) string {
	if frames <= 0 {
		if frames = countPackets(path); frames <= 0 {
			return ""
		}
	}
	f, err := os.CreateTemp("", "csync_still_*.png")
	if err != nil {
		return ""
	}
	name := f.Name()
	f.Close()
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-i", path,
		"-vf", fmt.Sprintf("select=eq(n\\,%d)", frames-1),
		"-frames:v", "1", "-update", "1", name)
	if cmd.Run() != nil {
		os.Remove(name)
		return ""
	}
	if fi, err := os.Stat(name); err != nil || fi.Size() == 0 {
		os.Remove(name)
		return ""
	}
	return name
}

// freezeChunk encodes `frames` copies of a still as a raw HEVC stream that concatenates
// cleanly with the comma's own chunks. With no still (a gap right at the start of a
// drive) it emits black so the timeline still lines up.
func freezeChunk(still string, w, h, frames int) (string, error) {
	if frames <= 0 || w <= 0 || h <= 0 {
		return "", fmt.Errorf("bad freeze request")
	}
	f, err := os.CreateTemp("", "csync_freeze_*.hevc")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	args := []string{"-y", "-v", "error"}
	if still != "" {
		args = append(args, "-loop", "1", "-i", still)
	} else {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("color=c=black:s=%dx%d:r=%s", w, h, fps()))
	}
	args = append(args,
		"-frames:v", strconv.Itoa(frames), "-r", fps(),
		"-vf", fmt.Sprintf("scale=%d:%d,format=yuv420p", w, h),
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=none",
		"-f", "hevc", name)
	if err := exec.Command("ffmpeg", args...).Run(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// concatCameraFilled builds one camera's full stream, splicing in frozen frames wherever
// that camera's footage is missing or short, so it ends up exactly as long as the other
// angles. Missing chunks are never silently skipped.
func concatCameraFilled(dir string, segs []string, cam string,
	ref map[string]int, have map[string]map[string]int) (string, error) {

	tmp, err := os.CreateTemp("", "comma_*.hevc")
	if err != nil {
		return "", err
	}
	fail := func(e error) (string, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", e
	}

	// Geometry for any filler: take it from the first segment where this camera exists.
	w, h := 0, 0
	for _, s := range segs {
		p := filepath.Join(dir, s, cam)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			if w, h = hevcGeom(p); w > 0 {
				break
			}
		}
	}

	prevPath, prevFrames := "", 0
	still := ""
	defer func() {
		if still != "" {
			os.Remove(still)
		}
	}()

	for _, s := range segs {
		p := filepath.Join(dir, s, cam)
		real := false
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			f, err := os.Open(p)
			if err != nil {
				return fail(fmt.Errorf("%s in segment %s: %w", cam, s, err))
			}
			_, cerr := io.Copy(tmp, f)
			f.Close()
			if cerr != nil {
				return fail(fmt.Errorf("reading %s from segment %s: %w", cam, s, cerr))
			}
			real = true
		}

		need := 0
		if r, ok := ref[s]; ok {
			need = r - have[s][cam]
		} else if !real {
			return fail(fmt.Errorf("%s is missing from segment %s", cam, s))
		}

		if need > 0 && w > 0 {
			// Freeze on the last frame we actually have for this camera.
			if still != "" {
				os.Remove(still)
				still = ""
			}
			src, srcFrames := prevPath, prevFrames
			if real { // short chunk: hold ITS last frame, not the previous segment's
				src, srcFrames = p, have[s][cam]
			}
			if src != "" {
				still = lastFrameStill(src, srcFrames)
			}
			fc, ferr := freezeChunk(still, w, h, need)
			if ferr != nil {
				return fail(fmt.Errorf("building freeze frames for %s in segment %s: %w", cam, s, ferr))
			}
			ff, err := os.Open(fc)
			if err != nil {
				os.Remove(fc)
				return fail(err)
			}
			_, cerr := io.Copy(tmp, ff)
			ff.Close()
			os.Remove(fc)
			if cerr != nil {
				return fail(cerr)
			}
		}

		if real {
			prevPath = p
			prevFrames = 0
			if have[s] != nil {
				prevFrames = have[s][cam]
			}
		}
	}
	tmp.Close()
	return tmp.Name(), nil
}
