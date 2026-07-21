package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// jsonProgress is set by the --json flag; when true, sync/restitch emit one JSON
// object per line (for GUIs). Otherwise human-friendly text.
var jsonProgress bool

// curRoute is the drive being processed right now. The extra-output renderers only get
// (outdir, stamp), but the UIs key their per-drive rows by route, so they tag their
// progress events with this instead of guessing.
var curRoute string

func emit(e ProgressEvent) {
	if jsonProgress {
		b, _ := json.Marshal(e)
		fmt.Println(string(b))
		return
	}
	switch e.Type {
	case "log", "done":
		fmt.Println(e.Message)
	case "drive":
		fmt.Println("==> " + e.Message)
	case "progress":
		fmt.Printf("\r      %s %3.0f%%        ", e.Phase, e.Percent)
		if e.Percent >= 100 {
			fmt.Println()
		}
	case "error":
		fmt.Fprintln(os.Stderr, "!! "+e.Message)
	}
}

func logf(format string, a ...any) {
	emit(ProgressEvent{Type: "log", Message: fmt.Sprintf(format, a...)})
}
