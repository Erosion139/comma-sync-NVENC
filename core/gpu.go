package main

// GPU (NVIDIA NVENC/NVDEC) rendering.
//
// Comma Sync builds every ffmpeg command as a plain []string and hands it to
// runFFmpegProgress. Rather than scatter encoder flags across combineVideo,
// equirect360Video and verticalVideo — three places that would drift apart the
// moment upstream adds a fourth output — this file is the single point where a
// software render is rewritten into a GPU one, and runFFmpegProgress calls it.
//
// Two knobs drive it, both set by the GUI:
//
//	USE_NVENC=1     render on the GPU instead of libx264
//	NVENC_GPU=1     which card to use (CUDA device index; 0 = first, 1 = second)
//
// WHAT IT DOES NOT TOUCH: the per-camera videos. Those are a stream copy
// (-c copy) of the comma's own HEVC — no encoding happens, so there is nothing
// for a GPU to do. NVENC affects the combined / 360° / vertical renders, which
// are the passes that actually burn CPU for minutes at a time.

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ---- configuration ---------------------------------------------------------

// useNVENC reports whether GPU rendering was asked for.
func useNVENC() bool { return os.Getenv("USE_NVENC") == "1" }

// nvencGPU is the CUDA device index the render runs on, as a string ready to
// hand to ffmpeg. On a two-card machine device 0 is normally the card driving
// your monitors; pointing this at 1 keeps that card free while long renders run
// on the second one.
func nvencGPU() string {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("NVENC_GPU"))); err == nil && n >= 0 {
		return strconv.Itoa(n)
	}
	return "0"
}

// useNVDEC hardware-decodes the source MP4s on the same card. On whenever NVENC
// is on; NVDEC=0 sends just the decode half back to the CPU, which is the first
// thing to try if a render misbehaves.
func useNVDEC() bool { return useNVENC() && envOr("NVDEC", "1") != "0" }

// nvencCodec picks the GPU encoder family: "h264" (default — plays everywhere)
// or "hevc" (smaller files, needs a newer player).
func nvencCodec() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NVENC_CODEC")), "hevc") {
		return "hevc"
	}
	return "h264"
}

// nvencPreset is NVENC's p1 (fastest) .. p7 (best). p5 sits close to libx264
// "veryfast" on quality while using almost no CPU.
func nvencPreset() string { return envOr("NVENC_PRESET", "p5") }

// ---- capability probing ----------------------------------------------------

var (
	nvencOnce sync.Once
	nvencOK   bool
)

// nvencAvailable asks the installed ffmpeg whether it was built with the NVENC
// encoder at all. Probed once per run; a build without it (some minimal Linux
// packages) simply renders on the CPU instead of failing every drive.
func nvencAvailable() bool {
	nvencOnce.Do(func() {
		out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
		if err != nil {
			return
		}
		nvencOK = strings.Contains(string(out), nvencCodec()+"_nvenc")
	})
	return nvencOK
}

// GPUInfo is one card as reported by nvidia-smi.
type GPUInfo struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// listGPUs enumerates the NVIDIA cards in this machine so the GUI can offer them
// by name instead of making you guess an index. Returns an empty list when
// nvidia-smi isn't present (no NVIDIA driver, or not on PATH) — the GUI then
// falls back to plain numbered entries.
func listGPUs() []GPUInfo {
	gpus := []GPUInfo{}
	out, err := exec.Command("nvidia-smi", "--query-gpu=index,name", "--format=csv,noheader").Output()
	if err != nil {
		return gpus
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		gpus = append(gpus, GPUInfo{Index: idx, Name: strings.TrimSpace(parts[1])})
	}
	return gpus
}

// ---- the rewrite -----------------------------------------------------------

// gpuizeFFmpegArgs turns a software render command into a GPU one, returning the
// new argument list and whether anything changed.
//
// It only ever rewrites a command that actually encodes with libx264. A stream
// copy (the per-camera mux) and macOS's VideoToolbox path are returned untouched,
// so this can sit in front of every ffmpeg call the core makes without having to
// know which one it is looking at.
//
// Decoding is moved to the card WITHOUT -hwaccel_output_format cuda, on purpose.
// Leaving the decoded frames in system memory means every existing filter —
// scale, overlay, hstack, vstack, alphamerge, the 360 crops — keeps working
// exactly as written. Forcing the frames to stay in GPU memory would mean
// rewriting all of them as scale_cuda/overlay_cuda, and overlay_cuda has no
// alphamerge equivalent, so the feathered road overlay could not be built at all.
// This way the two expensive halves (decode and encode) run on the GPU and only
// the cheap filtering stays on the CPU.
func gpuizeFFmpegArgs(args []string) ([]string, bool) {
	// macOS already hardware-encodes on VideoToolbox; there is no NVENC there.
	if !useNVENC() || runtime.GOOS == "darwin" {
		return args, false
	}

	softwareEncode := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c:v" && args[i+1] == "libx264" {
			softwareEncode = true
			break
		}
	}
	if !softwareEncode {
		return args, false // stream copy or another encoder — nothing to move
	}
	if !nvencAvailable() {
		logf("      GPU rendering is on, but this ffmpeg has no %s_nvenc — rendering on the CPU instead",
			nvencCodec())
		return args, false
	}

	// Carry the software quality target over to the GPU so the picture stays the
	// same as upstream intended for THIS output (the combined, 360 and vertical
	// renders each ask for a different CRF). NVENC_CQ overrides it outright.
	crf := "22"
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-crf" {
			crf = args[i+1]
		}
	}
	cq := envOr("NVENC_CQ", crf)

	gpu := nvencGPU()
	enc := []string{
		"-c:v", nvencCodec() + "_nvenc",
		"-gpu", gpu, // <- the whole point: which card encodes
		"-preset", nvencPreset(),
		"-rc", "vbr",
		"-cq", cq,
		"-b:v", "0", // constant-quality VBR, no bitrate cap
	}

	out := make([]string, 0, len(args)+16)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c:v" && i+1 < len(args) && args[i+1] == "libx264":
			out = append(out, enc...)
			i++
		case args[i] == "-preset" && i+1 < len(args):
			i++ // libx264's preset — replaced by the NVENC one above
		case args[i] == "-crf" && i+1 < len(args):
			i++ // replaced by -cq above
		case args[i] == "-i" && i+1 < len(args):
			// Decode the finished per-camera MP4s on the same card. Skipped for the
			// feather mask PNG and any lavfi source, which have nothing to decode.
			if useNVDEC() && strings.HasSuffix(strings.ToLower(args[i+1]), ".mp4") {
				out = append(out, "-hwaccel", "cuda", "-hwaccel_device", gpu)
			}
			out = append(out, args[i], args[i+1])
			i++
		default:
			out = append(out, args[i])
		}
	}
	return out, true
}

// gpuRenderNote describes the active setup for the log line.
func gpuRenderNote() string {
	s := "GPU " + nvencGPU() + " · " + nvencCodec() + "_nvenc"
	if useNVDEC() {
		s += " + nvdec"
	}
	return s
}
