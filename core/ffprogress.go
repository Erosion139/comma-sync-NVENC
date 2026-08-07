package main

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
)

// mp4Duration returns an MP4's duration in seconds (0 if unknown).
func mp4Duration(path string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nk=1:nw=1", path).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

// runFFmpegProgress runs one render pass, on the GPU when that's turned on.
//
// Every ffmpeg command the core builds passes through here, which makes it the
// one place that can decide where a render happens (see gpu.go). A GPU attempt
// that fails for ANY reason — a driver hiccup, an ffmpeg build without NVENC, a
// card busy with a game, a filter graph the encoder won't accept — is retried
// immediately on the CPU with the original, untouched command. A GPU problem can
// therefore cost you time on one render, never a drive.
func runFFmpegProgress(args []string, totalSecs float64, totalFrames int, label string) error {
	if gpuArgs, changed := gpuizeFFmpegArgs(args); changed {
		logf("      rendering %s on the %s", label, gpuRenderNote())
		err := runFFmpegOnce(gpuArgs, totalSecs, totalFrames, label)
		if err == nil {
			return nil
		}
		logf("      GPU render failed (%v) — redoing this one on the CPU", err)
	}
	return runFFmpegOnce(args, totalSecs, totalFrames, label)
}

// runFFmpegOnce runs ffmpeg and reports live percentage for every render pass —
// the per-camera mux AND the long re-encodes (combined / 360 / vertical). Without it a
// multi-hour drive renders for many minutes emitting nothing at all, which is
// indistinguishable from the app hanging.
//
// It tracks progress two ways because ffmpeg reports differently per pass:
//   - re-encodes report a valid out_time (elapsed media seconds) → percent vs totalSecs.
//   - a stream copy of raw HEVC reports out_time=N/A but a valid frame= counter →
//     percent vs totalFrames.
//
// Whichever is available is used, so both the fast copy pass and the slow encode pass
// get a moving bar. Pass 0 for a total that doesn't apply.
//
// ffmpeg's -progress stream is read from ITS OWN stdout via a pipe (never inherited),
// so it can't pollute the core's --json event stream on our stdout.
func runFFmpegOnce(args []string, totalSecs float64, totalFrames int, label string) error {
	full := append([]string{"-progress", "pipe:1", "-nostats"}, args...)
	cmd := exec.Command("ffmpeg", full...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	emit(ProgressEvent{Type: "progress", Route: curRoute, Phase: "render", Percent: 0, Message: label})

	sc := bufio.NewScanner(pipe)
	last := -1.0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		pct := -1.0
		switch {
		case strings.HasPrefix(line, "out_time_us="), strings.HasPrefix(line, "out_time_ms="):
			// Both keys are reported in MICROseconds (out_time_ms is a long-standing
			// ffmpeg misnomer). Reads "N/A" during a stream copy — then we fall to frame=.
			v := strings.SplitN(line, "=", 2)[1]
			if secs, e := strconv.ParseFloat(v, 64); e == nil && totalSecs > 0 {
				pct = secs / 1e6 / totalSecs * 100
			}
		case strings.HasPrefix(line, "frame="):
			if f, e := strconv.Atoi(strings.TrimPrefix(line, "frame=")); e == nil && totalFrames > 0 {
				pct = float64(f) / float64(totalFrames) * 100
			}
		default:
			continue
		}
		if pct < 0 {
			continue
		}
		if pct > 99.5 {
			pct = 99.5 // the last bit is mux/finalize; 100 comes from the caller
		}
		if pct-last >= 0.5 {
			last = pct
			emit(ProgressEvent{Type: "progress", Route: curRoute, Phase: "render", Percent: pct, Message: label})
		}
	}
	return cmd.Wait()
}
