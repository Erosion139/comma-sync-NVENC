# Roadmap

Where Comma Sync is headed, and where help is welcome. The goal is a clean,
cross-platform tool: one shared engine, with native front-ends per OS.

## Architecture

```
              ┌──────────────────────────────┐
              │   comma-sync core (engine)    │
              │  discover · list · sync ·     │
              │  re-stitch · audio · USB      │
              └──────────────────────────────┘
                 ▲            ▲            ▲
          macOS app      Linux GUI     Windows GUI
         (SwiftUI)     └─── one cross-platform GUI ───┘
                          (Tauri / Flutter / Qt)
```

The engine is now a single self-contained **core binary** (Go) that every front-end
calls, so we never re-implement discovery, the ledger, stitching, or audio muxing per
platform. The original bash script (`comma-sync.sh`) is **retired** but kept in the repo
for anyone who still wants it.

## Status

| Piece | State |
|-------|-------|
| **Go core** (single binary, JSON output, native SSH/SFTP) | ✅ Done |
| **macOS app** (SwiftUI on the core) | ✅ Done |
| **Linux & Windows app** (Tauri on the core) | ✅ Done |
| **CI** (GitHub Actions: core + macOS + Tauri, all platforms) | ✅ Done |
| **SHA-256 checksums + built-in update check** | ✅ Done |
| Legacy `comma-sync.sh` CLI (macOS/Linux) | ✅ Done (retired) |
| Signed + notarized macOS app | 🔜 Pending an Apple Developer account decision |
| **GitHub Pages** site | 🔜 Planned |

## Phases

### Phase 1 — Go core ✅ Shipped
Re-implement the script's logic as a single cross-compiled binary:
- Subcommands with JSON output (`discover`, `list --json`, `sync`), emitting
  **structured progress events** so GUIs don't parse text.
- Implement the file pull over **native SSH/SFTP** (Go `crypto/ssh`) to **drop the
  `rsync` dependency** — the main Windows blocker. `ffmpeg` stays (bundled per OS).
- Feature parity: ledger/dedup, resume, per-camera HEVC→MP4, audio mux from
  `qcamera.ts`, collision-safe naming, USB/ADB fallback.
- CI builds it for Ubuntu / macOS / Windows on every push.

### Phase 2 — Cross-platform GUI ✅ Shipped
One front-end (Tauri preferred — tiny native binaries) that calls the core and
mirrors the macOS app's screens (main, indexing results with per-drive progress,
stop). Ships Linux `.AppImage`/`.deb` first.

### Phase 3 — Windows ✅ Shipped
Package the GUI for Windows (`.msi`/`.exe`) and bundle a known-good `ffmpeg.exe`.
With rsync/ssh handled natively by the Go core, this becomes mostly packaging.

### Phase 4 — Docs & site
Per-OS install docs, a GitHub Pages landing page, and CI that auto-attaches every
platform's artifact to each GitHub Release.

### Phase 5 — Releases, trust & auto-update
- **SHA-256 checksums** published with every release so downloads can be verified.
- **Signed + notarized macOS app** so it opens with no Gatekeeper warning — *pending
  a decision on an Apple Developer account* (signing embeds the developer's name or
  org into the app; weigh this against the project's anonymity before enabling).
- **Built-in update check** — on launch the app checks GitHub Releases for a newer
  version and offers to update. **On by default**, with a setting to turn it off, so
  casual users don't get stuck on an old build. Only fetches the public Releases feed;
  no telemetry, no personal data sent. (macOS: a lightweight Releases-API check, or
  Sparkle once the app is signed; Tauri GUI: the built-in `tauri-plugin-updater`.)

## How CI helps contributors

GitHub's hosted runners (`ubuntu-latest`, `macos-latest`, `windows-latest`) act as the
test bench, so changes for a platform you don't own still get built and checked
automatically. The workflow in `.github/workflows/ci.yml` already has commented matrix
stubs for the Go core and GUI jobs — adding a phase means filling those in.

## Contributing

Fork, branch, PR. Especially wanted: the **Go core**, a **Linux/Windows GUI**, and
anyone who can test on real Linux/Windows hardware. See the Contributing section of the
[README](README.md).
