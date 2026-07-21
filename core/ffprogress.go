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

// runFFmpegProgress runs ffmpeg and reports live percentage for the long re-encodes
// (combined / 360 / vertical). Without it a multi-hour drive renders for tens of
// minutes emitting nothing at all, which is indistinguishable from the app hanging.
//
// ffmpeg's -progress stream is read from ITS OWN stdout via a pipe (never inherited),
// so it can't pollute the core's --json event stream on our stdout.
func runFFmpegProgress(args []string, totalSecs float64, label string) error {
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
		secs := -1.0
		// Both keys are reported in MICROseconds (out_time_ms is a long-standing
		// ffmpeg misnomer). Early lines can read "N/A", which simply fails to parse.
		for _, k := range []string{"out_time_us=", "out_time_ms="} {
			if strings.HasPrefix(line, k) {
				if v, e := strconv.ParseFloat(strings.TrimPrefix(line, k), 64); e == nil {
					secs = v / 1e6
				}
				break
			}
		}
		if secs < 0 || totalSecs <= 0 {
			continue
		}
		pct := secs / totalSecs * 100
		if pct > 99.5 {
			pct = 99.5 // the last 0.5% is the mux/finalize; 100 comes from the caller
		}
		if pct-last >= 0.5 {
			last = pct
			emit(ProgressEvent{Type: "progress", Route: curRoute, Phase: "render", Percent: pct, Message: label})
		}
	}
	return cmd.Wait()
}
