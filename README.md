# Comma Sync

Pull dashcam footage off your [comma](https://comma.ai) device over your local
network and stitch each drive into a single playable video — **with audio** — without
uploading anything to the cloud.

comma/openpilot store footage as one-minute **segments** of raw HEVC, split per camera,
in `/data/media/0/realdata/` on the device. Getting a watchable video out normally means
SSH, `scp`, and manual `ffmpeg` work. Comma Sync does it all for you:

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

# Command line (works on any OS with bash)

Prefer the terminal, or on Linux? The core is a single script:

```bash
./comma-sync.sh                 # find the comma, pull new drives, stitch them
./comma-sync.sh --list          # list drives on your computer + still on the comma
./comma-sync.sh --restitch <route>   # re-make one drive (re-downloads if needed)
```

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

# Wired (USB) — fallback only, not faster

No WiFi where you park? You can transfer over a USB-C cable using the comma's ADB gadget:

1. On the comma: **Settings → Developer → enable ADB**.
2. Connect a **data-capable** USB-C cable to the comma's spare port.
3. `brew install --cask android-platform-tools`, then `USE_USB=1 ./comma-sync.sh`.

> ⚠️ **This is not faster than WiFi.** On the comma 3X the USB gadget tops out around
> ~5 MB/s — measured *slower* than the device's WiFi (~6 MB/s). Use USB only when WiFi
> isn't available.

# Contributing & other operating systems

PRs and forks welcome! The command-line script is the portable core; the GUI app is
macOS-only. To help it run on **Linux** or **Windows** (WSL/Git Bash), these are the
macOS-specific spots to adapt:

| Feature | macOS (current) | Linux/other |
|---------|-----------------|-------------|
| File mtime | `stat -f %m` | `stat -c %Y` |
| Format epoch as date | `date -r EPOCH` | `date -d @EPOCH` |
| Find LAN subnet | `route -n get default` + `ipconfig getifaddr` | `ip route` / `hostname -I` |
| Port scan | `nc -G 1 -w 1` (BSD flags) | adjust `nc` flags |

Fork the repo, make your changes on a branch, and open a pull request — a Linux/Windows
GUI would be a great addition.

# License

[MIT](LICENSE) © sourylime — do whatever you like; no warranty.
