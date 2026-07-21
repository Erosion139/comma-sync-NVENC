package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// equirect360Video composites the three cameras into one 4096x2048 equirectangular
// (360°) MP4 for viewing in a VR headset: the wide camera fills the front hemisphere,
// the driver camera wraps the rear, and the sharp road camera is overlaid dead-on in
// the center of the front so the only visible seam is the quality difference. The road
// placement (472px @ 1788,784 in the 4096-wide canvas) is calibrated to the comma 3X's
// wide/road cameras so trees and cars line up across the boundary.
//
// Like the other outputs: hardware-decodes on macOS, writes atomically to a ".part"
// file it only renames once ffprobe confirms it's playable, and skips if it already
// exists. Needs all three cameras; a 2-camera drive is skipped.
func equirect360Video(outdir, stamp, suffix string) {
	wide := filepath.Join(outdir, stamp+"__wide"+suffix+".mp4")
	driver := filepath.Join(outdir, stamp+"__driver"+suffix+".mp4")
	road := filepath.Join(outdir, stamp+"__road"+suffix+".mp4")
	if !mp4OK(wide) || !mp4OK(driver) || !mp4OK(road) {
		logf("      360: needs road + wide + driver — skipped for %s", stamp)
		return
	}

	out := filepath.Join(outdir, stamp+"__vr360"+suffix+".mp4")
	if mp4OK(out) && outputFresh(out, []string{wide, driver, road}) {
		logf("      360 already rendered — skipped re-encode: %s", filepath.Base(out))
		return
	}
	part := out + ".part"
	os.Remove(part)

	// The road overlay's placement is calibrated per drive in native wide space, then
	// converted into the stretched 2048x2048 front-hemisphere space (where the road
	// region is square because the wide is stretched non-uniformly).
	a := calibrateRoadOverlay(wide, road)
	rs := even(2048 * a.scale)
	rx := 1024 + int(a.x*2048/1928+0.5)
	ry := int(a.y*2048/1208 + 0.5)
	fc := "color=c=black:s=4096x2048:r=" + fps() + "[bg];" +
		"[0:v]scale=2048:2048[front];" + // wide -> front hemisphere
		"[1:v]scale=2048:2048,split[dv1][dv2];" + // driver -> rear (split so it wraps the seam)
		"[dv1]crop=1024:2048:0:0[rearL];" +
		"[dv2]crop=1024:2048:1024:0[rearR];" +
		fmt.Sprintf("[2:v]scale=%d:%d[roadhi];", rs, rs) + // road -> sharp center overlay
		"[bg][front]overlay=1024:0[b1];" +
		"[b1][rearL]overlay=3072:0[b2];" +
		"[b2][rearR]overlay=0:0[b3];" +
		fmt.Sprintf("[b3][roadhi]overlay=%d:%d:shortest=1[v]", rx, ry)

	args := []string{"-y", "-loglevel", "error"}
	for _, p := range []string{wide, driver, road} {
		if runtime.GOOS == "darwin" {
			args = append(args, "-hwaccel", "videotoolbox")
		}
		args = append(args, "-i", p)
	}
	args = append(args, "-filter_complex", fc, "-map", "[v]", "-map", "0:a?")
	if runtime.GOOS == "darwin" {
		args = append(args, "-c:v", "h264_videotoolbox", "-b:v", "30M")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p")
	}
	// No +faststart on purpose: we keep moov at the END of the file so inject360 can
	// grow it without shifting mdat (which would invalidate the sample chunk offsets).
	args = append(args, "-c:a", "copy", "-f", "mp4", part)

	if err := runFFmpegProgress(args, mp4Duration(wide), "360 video"); err != nil || !mp4OK(part) {
		os.Remove(part)
		emit(ProgressEvent{Type: "error", Message: "360 video failed for " + stamp})
		return
	}
	if err := inject360(part); err != nil {
		// The composite is still a valid equirectangular video; it just won't be
		// auto-detected as 360 (the viewer can set the projection manually).
		logf("      360: metadata injection skipped (%v) — set projection to equirectangular in your viewer", err)
	}
	os.Rename(part, out)
	logf("      360 (equirectangular, headset-ready): %s", filepath.Base(out))
}

// sphericalXML is the Google Spherical Video V1 payload (equirectangular, mono) that
// VR players read to auto-detect a 360 clip — byte-for-byte the metadata verified
// working in a headset.
const sphericalXML = "<rdf:SphericalVideo\n" +
	" xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'\n" +
	" xmlns:GSpherical='http://ns.google.com/videos/1.0/spherical/'>\n" +
	"  <GSpherical:ProjectionType>equirectangular</GSpherical:ProjectionType>\n" +
	"  <GSpherical:SourceCount>3</GSpherical:SourceCount>\n" +
	"  <GSpherical:Spherical>True</GSpherical:Spherical>\n" +
	"  <GSpherical:Stitched>True</GSpherical:Stitched>\n" +
	"  <GSpherical:StitchingSoftware>Comma Sync</GSpherical:StitchingSoftware>\n" +
	"</rdf:SphericalVideo>"

// sphericalUUID is the fixed box UUID for Spherical Video V1.
var sphericalUUID = []byte{0xff, 0xcc, 0x82, 0x63, 0xf8, 0x55, 0x4a, 0x93, 0x88, 0x14, 0x58, 0x7a, 0x02, 0x52, 0x1f, 0xdd}

// boxHeader reads the size/type of the MP4 box at off within b.
func boxHeader(b []byte, off int) (typ string, size, hdr int, ok bool) {
	if off+8 > len(b) {
		return "", 0, 0, false
	}
	size = int(binary.BigEndian.Uint32(b[off : off+4]))
	typ = string(b[off+4 : off+8])
	hdr = 8
	if size == 1 {
		if off+16 > len(b) {
			return "", 0, 0, false
		}
		size = int(binary.BigEndian.Uint64(b[off+8 : off+16]))
		hdr = 16
	} else if size == 0 {
		size = len(b) - off
	}
	if size < hdr || off+size > len(b) {
		return "", 0, 0, false
	}
	return typ, size, hdr, true
}

// findChild returns the [start,size,hdr] of the first direct child box of the given
// type within b[start+hdr : start+size].
func findChild(b []byte, parentStart, parentSize, parentHdr int, want string) (start, size, hdr int, ok bool) {
	off := parentStart + parentHdr
	end := parentStart + parentSize
	for off < end {
		t, s, h, k := boxHeader(b, off)
		if !k {
			break
		}
		if t == want {
			return off, s, h, true
		}
		off += s
	}
	return 0, 0, 0, false
}

// isVideoTrak reports whether the trak box (at trakStart) has a video media handler.
func isVideoTrak(b []byte, trakStart, trakSize, trakHdr int) bool {
	ms, mz, mh, ok := findChild(b, trakStart, trakSize, trakHdr, "mdia")
	if !ok {
		return false
	}
	hs, _, hh, ok := findChild(b, ms, mz, mh, "hdlr")
	if !ok {
		return false
	}
	// hdlr payload: version+flags(4), pre_defined(4), handler_type(4)
	ht := hs + hh + 8
	return ht+4 <= len(b) && string(b[ht:ht+4]) == "vide"
}

// setBoxSize rewrites a box's size field in place (handles 32- and 64-bit).
func setBoxSize(b []byte, boxStart, hdr, newSize int) {
	if hdr == 16 {
		binary.BigEndian.PutUint32(b[boxStart:boxStart+4], 1)
		binary.BigEndian.PutUint64(b[boxStart+8:boxStart+16], uint64(newSize))
	} else {
		binary.BigEndian.PutUint32(b[boxStart:boxStart+4], uint32(newSize))
	}
}

// inject360 writes the Spherical V1 metadata into an MP4's video track. It requires the
// file to have moov at the END (no faststart) so growing moov never shifts mdat.
func inject360(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := int(fi.Size())

	// Walk top-level boxes to locate moov (and anything after it).
	moovOff := -1
	for off := 0; off+8 <= fileSize; {
		var hdr [16]byte
		if _, err := f.ReadAt(hdr[:8], int64(off)); err != nil {
			return err
		}
		size := int(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		if size == 1 {
			if _, err := f.ReadAt(hdr[8:16], int64(off+8)); err != nil {
				return err
			}
			size = int(binary.BigEndian.Uint64(hdr[8:16]))
		} else if size == 0 {
			size = fileSize - off
		}
		if size < 8 {
			return fmt.Errorf("bad box %q at %d", typ, off)
		}
		if typ == "moov" {
			moovOff = off
			break
		}
		off += size
	}
	if moovOff < 0 {
		return fmt.Errorf("moov not found")
	}

	// Read moov + everything after it into memory (moov is small; mdat is before it).
	tail := make([]byte, fileSize-moovOff)
	if _, err := f.ReadAt(tail, int64(moovOff)); err != nil {
		return err
	}
	moovTyp, moovSize, moovHdr, ok := boxHeader(tail, 0)
	if !ok || moovTyp != "moov" {
		return fmt.Errorf("moov parse failed")
	}

	// Find the video trak within moov.
	trakStart, trakSize, trakHdr := -1, 0, 0
	off := moovHdr
	for off < moovSize {
		t, s, h, k := boxHeader(tail, off)
		if !k {
			break
		}
		if t == "trak" && isVideoTrak(tail, off, s, h) {
			trakStart, trakSize, trakHdr = off, s, h
			break
		}
		off += s
	}
	if trakStart < 0 {
		return fmt.Errorf("video trak not found")
	}

	// Build the spherical uuid box and insert it at the end of the video trak.
	box := make([]byte, 0, 8+16+len(sphericalXML))
	boxLen := 8 + 16 + len(sphericalXML)
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(boxLen))
	box = append(box, sz[:]...)
	box = append(box, []byte("uuid")...)
	box = append(box, sphericalUUID...)
	box = append(box, []byte(sphericalXML)...)

	insertAt := trakStart + trakSize
	newTail := make([]byte, 0, len(tail)+boxLen)
	newTail = append(newTail, tail[:insertAt]...)
	newTail = append(newTail, box...)
	newTail = append(newTail, tail[insertAt:]...)

	// Grow the trak and moov size fields by the inserted box length.
	setBoxSize(newTail, trakStart, trakHdr, trakSize+boxLen)
	setBoxSize(newTail, 0, moovHdr, moovSize+boxLen)

	// Rewrite from moovOff onward.
	if _, err := f.WriteAt(newTail, int64(moovOff)); err != nil {
		return err
	}
	return f.Truncate(int64(moovOff + len(newTail)))
}
