# Comma Sync

[![CI](https://github.com/sourylime/comma-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/sourylime/comma-sync/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Roadmap](https://img.shields.io/badge/Roadmap-see%20plan-brightgreen.svg)](ROADMAP.md)

Pull dashcam footage off your [comma](https://comma.ai) device over your local
network and stitch each drive into a single playable video — **with audio** — without
uploading anything to the cloud.

comma/openpilot store footage as one-minute **segments** of raw HEVC, split per camera,
in `/data/media/0/realdata/` on the device. Getting a watchable video out normally means
SSH, `scp`, and manual `ffmpeg` work. Comma Sync does it all for you:

![Comma Sync demo](docs/demo.gif)

### Screenshots

| Index Drives | Sync in progress |
|:---:|:---:|
| ![Index Drives — browse and select drives](docs/screenshot-index.png) | ![Sync in progress with live log](docs/screenshot-sync.png) |
| Browse every drive — on your Mac *and* still on the comma — and download some or all. | Live progress bar and log while it pulls and stitches each drive. |

![Comma Sync in dark mode](docs/screenshot-dark.png)

*Follows your system's light or dark appearance. All screenshots use made-up example data — drive names, sizes, and paths are not real.*

- 🔍 **Auto-discovers** the device on your WiFi (its IP usually changes every connect).
- ⬇️ **Pulls only new drives** — a ledger tracks what's done, so nothing re-downloads.
- 🎬 **Stitches** each drive into one MP4 per camera (road / wide / driver), named by the
  recording start time.
- 🔊 **Adds the microphone audio** when it was recorded.
- ♻️ **Re-sync anything**, even old drives you deleted locally — it pulls them back.
- 🖥️ A small **macOS app**: pick folders, see a progress bar, browse/index every drive
  (on your Mac *and* still on the comma), select some/all, and download — with the
  ability to close it and come back while it keeps working.

> Tested on a **comma 3X** running [sunnypilot](https://github.com/sunnypilot/sunnypilot).
> Should also work with stock openpilot and other forks that keep the standard layout.

> 🐧🪟 **On Linux or Windows?** There's now a cross-platform **beta** app (the new shared
> "Go core") that runs on Linux, Windows, **and** macOS. Jump to
> **[Linux and Windows (beta)](#linux-and-windows-beta)** below to try it.

---

# Install on macOS (step by step)

New to this? Follow these in order. You'll copy a few commands into the **Terminal** app
(press ⌘+Space, type "Terminal", hit Return).

### Step 1 — Install the free tools it relies on

Comma Sync uses `rsync` and `ffmpeg` under the hood. The easiest way to get them is
[Homebrew](https://brew.sh). If you don't already have Homebrew, paste this into Terminal
and press Return (it'll ask for your password):

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

Then install the two tools:

```bash
brew install rsync ffmpeg
```

### Step 2 — Get Comma Sync

**Option A — Download the ready-made app (easiest):**
Go to the [**Releases page**](https://github.com/sourylime/comma-sync/releases/latest),
download **`Comma-Sync-macOS.zip`**, and double-click it to unzip. You'll get
**`Comma Sync.app`** — it's self-contained, so just drag it into your Applications folder.

**Option B — Build it yourself from the code:**
On the project's [GitHub page](https://github.com/sourylime/comma-sync), click the green
**`Code`** button → **Download ZIP**, unzip it, then in Terminal:

```bash
cd ~/Downloads/comma-sync-main      # wherever you unzipped it
xcode-select --install              # one-time: installs Apple's build tools (skip if already done)
bash macos-app/build.sh             # builds "Comma Sync.app" next to comma-sync.sh
```

### Step 3 — Open the app the first time

Because this is a free open-source app (not from the App Store), macOS will refuse to
open it on the first try. To allow it, run this once in Terminal (adjust the path to where
your app is):

```bash
xattr -dr com.apple.quarantine "/path/to/Comma Sync.app"
```

Then double-click the app. (Alternatively: try to open it, then go to **System Settings →
Privacy & Security**, scroll down, and click **Open Anyway**.)

### Step 4 — Let your Mac talk to the comma (one-time)

The comma only allows computers whose SSH key is on a GitHub account you tell it about.

1. **Make an SSH key on your Mac** (skip if you already have `~/.ssh/id_ed25519`):
   ```bash
   ssh-keygen -t ed25519        # press Return at every prompt
   ```
2. **Add it to a GitHub account** — copy the key with `pbcopy < ~/.ssh/id_ed25519.pub`,
   then paste it at [github.com/settings/keys](https://github.com/settings/keys) → *New SSH key*.
3. **On the comma:** Settings → Developer → enable **SSH**, and enter **that GitHub
   username**. (On sunnypilot it's under the Developer section.)

### Step 5 — Use it

1. Make sure the comma is powered on and on the **same WiFi** as your Mac.
2. Open **Comma Sync**, pick where to save videos, and click **Index Drives** to see every
   drive and its size.
3. Tick the ones you want (or **Download All**) and watch them transfer. Done!

---

# Linux and Windows (beta)

> 🧪 **Testers wanted!** There's now a **Linux and Windows** build of Comma Sync (plus a
> macOS build), all powered by a new shared **Go core**. It does everything the macOS app
> does — discovering the comma, downloading drives, and stitching videos — but with SSH,
> file transfer, and discovery built **natively into the app** (so there's nothing to set
> up except `ffmpeg`). It's a **beta**, so please try it and
> **[report any bugs](https://github.com/sourylime/comma-sync/issues)**. Eventually the
> whole project will move onto this one core; for now it lives **alongside** the stable
> macOS app so Linux and Windows users have something to run today.

### Step 1 — Download it

Open the **[Releases page](https://github.com/sourylime/comma-sync/releases)** and find the
newest entry titled **"Comma Sync … — Go core beta (all platforms)"** — it has a grey
**`Pre-release`** label. Click it, scroll to **Assets**, and download the file for your
system:

| Your computer | Download this |
|---------------|---------------|
| **Linux** | `Comma.Sync_*_amd64.AppImage` (simplest) — or `.deb` / `.rpm` |
| **Windows** | `Comma.Sync_*_x64-setup.exe` (installer) — or `.msi` |
| **macOS** | `Comma-Sync-Go-macOS.zip` |

*(Each OS also has a `SHA256SUMS-*.txt` if you want to verify your download.)*

### Step 2 — Install `ffmpeg` (one time)

The app uses `ffmpeg`/`ffprobe` to stitch the videos — it's the only thing you need to
install yourself. Open a terminal and run the line for your OS:

- **Linux:** `sudo apt install ffmpeg` (Debian/Ubuntu) · `sudo dnf install ffmpeg` (Fedora)
- **Windows:** `winget install Gyan.FFmpeg`, then **close and reopen** your terminal so it's found
- **macOS:** `brew install ffmpeg`

### Step 3 — Open the app

**Linux** — using the AppImage, make it runnable and launch it:
```bash
chmod +x Comma.Sync_*_amd64.AppImage
./Comma.Sync_*_amd64.AppImage
# — or install the .deb:   sudo apt install ./Comma.Sync_*_amd64.deb
# — or the .rpm (Fedora):  sudo dnf install ./Comma.Sync-*.x86_64.rpm
```

**Windows** — run the `*-setup.exe` (or the `.msi`). It isn't code-signed yet, so Windows
**SmartScreen** may pop up a warning — click **More info → Run anyway**.

**macOS** — unzip to get **`Comma Sync Go.app`**. It's unsigned, so the first time
**right-click the app → Open**, then click **Open** in the dialog (after that, double-click
as normal).

### Step 4 — Let the app talk to the comma (one time)

This is the **same** one-time setup as the macOS app: the comma only allows computers whose
SSH key is registered to it via a GitHub account. Follow **Step 4** in the macOS
instructions above — it's identical on every OS, except how you make the key:

- **Linux / macOS:** `ssh-keygen -t ed25519` (key lands in `~/.ssh/id_ed25519.pub`)
- **Windows:** `ssh-keygen -t ed25519` in **PowerShell** (built into Windows 10/11); your
  key lands in `%USERPROFILE%\.ssh\id_ed25519.pub`

Add the `.pub` key to a GitHub account, then enter **that GitHub username** on the comma
under **Settings → Developer → SSH**.

### Step 5 — Use it

Make sure the comma is on the **same WiFi**, open the app, pick where to save videos, click
**Index Drives**, tick what you want (or **Download All**), and let it transfer + stitch.
Same as the macOS app. **Hit a bug? [Open an issue](https://github.com/sourylime/comma-sync/issues)** — that's exactly what this beta is for.

---

# Command line — macOS & Linux

The core is a single script that **auto-detects macOS or Linux** (no flags needed):

```bash
./comma-sync.sh                 # find the comma, pull new drives, stitch them
./comma-sync.sh --list          # list drives on your computer + still on the comma
./comma-sync.sh --restitch <route>   # re-make one drive (re-downloads if needed)
```

**Linux setup:** install the tools it shells out to, then run the script:

```bash
sudo apt install rsync ffmpeg openssh-client netcat-openbsd   # Debian/Ubuntu
# (Fedora: sudo dnf install rsync ffmpeg openssh-clients nmap-ncat)
./comma-sync.sh
```

**Windows:** this script needs **WSL** or **Git Bash** with those same tools. For a
native Windows experience, use the **[Linux and Windows (beta)](#linux-and-windows-beta)**
app instead — no WSL required.

Settings are environment variables:

| Variable | Default | Purpose |
|----------|---------|---------|
| `ROOT` | `./Comma Footage` | Where stitched videos go |
| `CHUNKS_DIR` | `ROOT/Raw HEVC Chunks` | Where raw chunks are kept |
| `WITH_AUDIO` | `1` | Mux mic audio when present (`0` = video only) |
| `CLEAN_RAW` | `0` | `1` = delete a drive's chunks after stitching |
| `COMMA_IP` | _(empty)_ | Pin a fixed IP instead of auto-discovering |
| `AUTO_DISCOVER` | `1` | `0` = only use `COMMA_IP` |
| `REMOTE_PORT` | `22` | SSH port (older devices used `8022`) |
| `SSH_KEY` | `~/.ssh/id_ed25519` | Private key to authenticate with |
| `USE_USB` | `0` | `1` = transfer over USB/ADB instead of WiFi (see below) |
| `BWLIMIT` | _(none)_ | Cap the transfer rate (e.g. `3m` = 3 MB/s). Lowers the comma's power draw — use if it reboots mid-transfer (see Troubleshooting) |

# Wired (USB) — fallback only, not faster

No WiFi where you park? You can transfer over a USB-C cable using the comma's ADB gadget:

1. On the comma: **Settings → Developer → enable ADB**.
2. Connect a **data-capable** USB-C cable to the comma's spare port.
3. `brew install --cask android-platform-tools`, then `USE_USB=1 ./comma-sync.sh`.

> ⚠️ **This is not faster than WiFi.** On the comma 3X the USB gadget tops out around
> ~5 MB/s — measured *slower* than the device's WiFi (~6 MB/s). Use USB only when WiFi
> isn't available.

# Contributing & status

PRs and forks welcome! Where things stand:

- **macOS:** native **SwiftUI app** (stable) plus the `comma-sync.sh` CLI.
- **Linux & Windows:** native app in **[beta](#linux-and-windows-beta)** via the shared
  **Go core** — this is where testing and bug reports help most right now.
- **CLI:** `comma-sync.sh` runs natively on macOS and Linux.

The project is mid-migration to a single cross-platform **Go core**: once the Linux and
Windows builds are verified on real hardware, it'll become the one engine behind every
front-end, and these instructions will be consolidated. Until then the stable macOS app
and the beta live side by side.

Found a bug or want to help? **[Open an issue](https://github.com/sourylime/comma-sync/issues)**,
or fork the repo, work on a branch, and open a pull request.

# Troubleshooting

The comma reboots randomly during transfers still, but will pick back up where it left off and evetually get the video transferred. This is being troubleshot currenlty, but if you find a fix feel free to post a branch with a fix. As for now:

- **Best:** transfer with the **engine running** (alternator → stable harness power), or
  power the device from a proper **high-current USB-C supply** (not a laptop port or a
  weak charger). Make sure the cable is fully seated.
- **Mitigate:** lower the draw with `BWLIMIT`, e.g. `BWLIMIT=3m ./comma-sync.sh`.

# License

[MIT](LICENSE) © sourylime — do whatever you like; no warranty.

### Donations

Completely optional, but graciously accepted 🙏 — Bitcoin: **[BITCOIN ADDRESS TO BE ADDED]**
