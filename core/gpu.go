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
//	NVENC_GPU=1     which card to use (see "Which card is which" below)
//
// WHAT IT DOES NOT TOUCH: the per-camera videos. Those are a stream copy
// (-c copy) of the comma's own HEVC — no encoding happens, so there is nothing
// for a GPU to do. NVENC affects the combined / 360° / vertical renders, which
// are the passes that actually burn CPU for minutes at a time.

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ---- Which card is which ---------------------------------------------------
//
// A machine with two NVIDIA cards has at least three different numberings of
// them, and they do not have to agree:
//
//   - Task Manager's "GPU 0 / GPU 1"        — Windows' own display ordering
//   - nvidia-smi's index                    — PCI bus order
//   - CUDA / NVENC's device ordinal         — what ffmpeg's -gpu flag means
//
// Only the third one decides where a render actually lands. Naming the dropdown
// from nvidia-smi and then selecting with -gpu is how you end up asking for one
// card and loading the other.
//
// So this file never assumes the lists match. It asks NVENC itself: run a
// throwaway one-frame encode pinned to each index and read back the card name
// ffmpeg reports for it. That is the same code path, the same enumeration and
// the same driver the real render will use, so the answer cannot disagree with
// what happens later.
//
// As a second measure, CUDA is asked to enumerate in PCI bus order — which is
// what nvidia-smi uses — so the two line up where the driver honours it. The
// probe is still the source of truth; this just makes the common case sane.
// Set CUDA_DEVICE_ORDER yourself, or NVENC_PCI_ORDER=0, to leave it alone.
func init() {
	if os.Getenv("CUDA_DEVICE_ORDER") == "" && os.Getenv("NVENC_PCI_ORDER") != "0" {
		os.Setenv("CUDA_DEVICE_ORDER", "PCI_BUS_ID")
	}
}

// ---- configuration ---------------------------------------------------------

// useNVENC reports whether GPU rendering was asked for.
func useNVENC() bool { return os.Getenv("USE_NVENC") == "1" }

// nvencGPU is the device ordinal the render runs on, as a string ready to hand
// to ffmpeg. This is an NVENC/CUDA ordinal, not a Task Manager number — see the
// note above, and trust the name the GUI shows next to it.
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

// GPUInfo is one card, numbered the way ffmpeg's -gpu flag numbers them.
type GPUInfo struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// nvencGPULine matches ffmpeg's own report of a card, e.g.
//
//	[ GPU #1 - < Quadro RTX 5000 > has Compute SM 7.5 ]
//
// which nvenc prints at verbose level for whichever device it was pointed at.
var nvencGPULine = regexp.MustCompile(`GPU\s*#(\d+)\s*-\s*<\s*(.+?)\s*>`)

// probeNVENCDevice pins a one-frame throwaway encode to device idx and reads
// back the name ffmpeg reports for it. Costs a fraction of a second and writes
// nothing (-f null). Returns false when there is no such device.
func probeNVENCDevice(idx int) (string, bool) {
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-v", "verbose",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:r=1:d=1",
		"-frames:v", "1",
		"-c:v", nvencCodec()+"_nvenc", "-gpu", strconv.Itoa(idx),
		"-f", "null", "-").CombinedOutput()
	for _, m := range nvencGPULine.FindAllStringSubmatch(string(out), -1) {
		// nvenc logs the index it actually opened; only trust a line for OUR index.
		if n, err := strconv.Atoi(m[1]); err == nil && n == idx {
			if name := strings.TrimSpace(m[2]); name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// smiGPUs lists the cards the way nvidia-smi orders them. Used to know how many
// to probe for, and as the fallback list when probing isn't possible.
func smiGPUs() []GPUInfo {
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

var (
	gpuOnce sync.Once
	gpuList []GPUInfo
	gpuSrc  string
)

// gpus returns the cards numbered as ffmpeg's -gpu flag numbers them, plus where
// that numbering came from:
//
//	"nvenc"      — asked the encoder directly; the names are authoritative
//	"nvidia-smi" — couldn't ask, so this ordering may not match what renders
//	"none"       — no NVIDIA cards found
//
// Worked out once per run and reused.
func gpus() ([]GPUInfo, string) {
	gpuOnce.Do(func() {
		gpuList, gpuSrc = buildGPUList()
	})
	return gpuList, gpuSrc
}

func buildGPUList() ([]GPUInfo, string) {
	smi := smiGPUs()

	if nvencAvailable() {
		// Probe one past what nvidia-smi saw, in case it isn't installed or missed
		// a card; a non-existent index fails instantly, so overshooting is cheap.
		max := len(smi) + 1
		if max < 2 {
			max = 4
		}
		var probed []GPUInfo
		for i := 0; i < max; i++ {
			name, ok := probeNVENCDevice(i)
			if !ok {
				break // indices are contiguous; the first miss is the end
			}
			probed = append(probed, GPUInfo{Index: i, Name: name})
		}
		if len(probed) > 0 {
			return probed, "nvenc"
		}
	}

	if len(smi) > 0 {
		return smi, "nvidia-smi"
	}
	return []GPUInfo{}, "none"
}

// nvencDeviceName names the card a given ordinal refers to, or "" if unknown.
func nvencDeviceName(idx string) string {
	n, err := strconv.Atoi(idx)
	if err != nil {
		return ""
	}
	list, _ := gpus()
	for _, g := range list {
		if g.Index == n {
			return g.Name
		}
	}
	return ""
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

// gpuRenderNote describes the active setup for the log line — including the name
// of the card, so the log says which one is really doing the work rather than
// only which number was asked for.
func gpuRenderNote() string {
	s := "GPU " + nvencGPU()
	if name := nvencDeviceName(nvencGPU()); name != "" {
		s += " (" + name + ")"
	}
	s += " · " + nvencCodec() + "_nvenc"
	if useNVDEC() {
		s += " + nvdec"
	}
	return s
}
