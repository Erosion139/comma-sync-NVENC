#!/usr/bin/env bash
#
# comma-sync.sh — pull new dashcam footage off a comma/openpilot device and
#                 stitch each drive into playable MP4s.
#
# What it does, end to end:
#   1. rsync ONLY the new raw *.hevc segments from the device into:
#         Comma Footage/Raw HEVC Chunks/      (visible, so you can browse them)
#      Already-processed drives are skipped and never re-downloaded.
#   2. Groups segments by "drive" (route) and concatenates them in order.
#   3. Produces one playable .mp4 per camera per drive, named by recording start
#      time (so they sort chronologically), each in its own folder. Microphone
#      audio (stored in qcamera.ts) is muxed in when it was recorded:
#         Comma Footage/2026-06-19_23-59-31/2026-06-19_23-59-31__road.mp4
#         Comma Footage/2026-06-19_23-59-31/2026-06-19_23-59-31__wide.mp4
#         Comma Footage/2026-06-19_23-59-31/2026-06-19_23-59-31__driver.mp4
#   4. Records the drive as done. The raw chunks are KEPT in "Raw HEVC Chunks"
#      so you can clean them up yourself whenever you like — deleting them will
#      NOT cause a re-download (the ledger tracks what's processed).
#
# To purge processed raw chunks automatically, run:  CLEAN_RAW=1 ./comma-sync.sh
# Or just delete the "Raw HEVC Chunks" folder by hand anytime.
#
# Safe to run as often as you like. Just run:  ./comma-sync.sh
#
set -euo pipefail

# ---- config -----------------------------------------------------------------
COMMA_IP="${COMMA_IP:-}"                        # optional fixed IP. Leave EMPTY to always
                                               # auto-discover — the device's DHCP IP usually
                                               # changes on every connect, so a fixed guess
                                               # just wastes time.
AUTO_DISCOVER="${AUTO_DISCOVER:-1}"            # 1 = scan the network to find the device
                                               # 0 = only use COMMA_IP (lock to one address)
REMOTE_PORT="${REMOTE_PORT:-22}"               # comma SSH port (older devices use 8022)
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"    # private key whose .pub is on your GitHub
ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/Comma Footage}"  # stitched output
CHUNKS_DIR="${CHUNKS_DIR:-}"                     # where to keep raw chunks (empty = a
                                               # "Raw HEVC Chunks" folder inside ROOT)
FPS="${FPS:-20}"                               # openpilot cameras record at 20fps
WITH_AUDIO="${WITH_AUDIO:-1}"                   # 1 = mux microphone audio (from qcamera.ts)
                                               #     into the video when it was recorded
SKIP_LATEST="${SKIP_LATEST:-1}"                # 1 = hold back a drive only if it's still
                                               #     being recorded; 0 = stitch everything now.
MIN_AGE_SECS="${MIN_AGE_SECS:-120}"            # a drive whose newest chunk was written less
                                               #     than this many seconds ago is treated as
                                               #     "still recording" and skipped until next run.
CLEAN_RAW="${CLEAN_RAW:-0}"                     # 0 = keep raw chunks for manual cleanup
                                               # 1 = delete a drive's raw chunks after stitching
USE_USB="${USE_USB:-0}"                          # 1 = transfer over a USB/ADB link instead of WiFi.
                                               #     A fallback for when WiFi isn't available —
                                               #     it is NOT faster than WiFi on the comma 3X.
USB_PORT="${USB_PORT:-2222}"                     # local port used to tunnel SSH over ADB
BWLIMIT="${BWLIMIT:-}"                            # optional rsync transfer cap (e.g. 3m = 3 MB/s).
                                               #     Lowers the comma's power draw — set this if the
                                               #     device browns out / reboots mid-transfer.
SSH_CIPHER="${SSH_CIPHER:-aes128-gcm@openssh.com}" # HW-accelerated on the comma 3X (ARMv8 crypto):
                                               #     ~20% faster + less CPU/heat than the default
                                               #     chacha20. Set empty to use SSH's default.
REMOTE_USER="comma"
REMOTE_PATH="/data/media/0/realdata/"
RSYNC="$(command -v /opt/homebrew/bin/rsync || command -v rsync)"
# -----------------------------------------------------------------------------

STAGING="${CHUNKS_DIR:-$ROOT/Raw HEVC Chunks}"  # visible: browse/clean these yourself
LEDGER="$ROOT/.processed_routes"
IPCACHE="${HOME}/.cache/comma-sync/last_ip"   # global (not per-folder) so discovery
                                              # works the first time on any new location

# One cleanup hook for temp files and any USB port-forward.
cleanup() {
  [ -n "${EXCLUDES:-}" ] && rm -f "$EXCLUDES"
  [ -n "${DEVCOUNTS:-}" ] && rm -f "$DEVCOUNTS"
  [ "$USE_USB" = "1" ] && adb forward --remove "tcp:${USB_PORT}" >/dev/null 2>&1
  return 0
}
trap cleanup EXIT

# Optional USB/ADB transport. Tunnels SSH over the comma's ADB gadget and points the
# whole pipeline at localhost. Same speed or slower than WiFi on the comma 3X — only
# worth it when no WiFi is available. Requires ADB enabled on the device (Developer menu).
if [ "$USE_USB" = "1" ]; then
  command -v adb >/dev/null 2>&1 || { echo "!! USE_USB=1 needs adb (macOS: brew install --cask android-platform-tools · Linux: apt install android-tools-adb)"; exit 1; }
  adb get-state >/dev/null 2>&1 || { echo "!! No ADB device. Enable ADB on the comma (Settings -> Developer) and connect USB."; exit 1; }
  echo "==> Using USB (ADB) link. NOTE: not faster than WiFi on the comma 3X — use only when WiFi is unavailable."
  adb forward "tcp:${USB_PORT}" "tcp:${REMOTE_PORT}" >/dev/null
  COMMA_IP="127.0.0.1"; REMOTE_PORT="${USB_PORT}"; AUTO_DISCOVER=0
fi

# Ignore host keys for this LAN device — they change on every reflash, which would
# otherwise trigger a "host identification changed" error and block the connection.
SSH_BASE="-i ${SSH_KEY} -p ${REMOTE_PORT} ${SSH_CIPHER:+-c $SSH_CIPHER} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR"
SSH_OPTS="ssh ${SSH_BASE} -o ConnectTimeout=10 -o ServerAliveInterval=10 -o ServerAliveCountMax=6"

mkdir -p "$STAGING"
mkdir -p "$(dirname "$IPCACHE")" 2>/dev/null || true
touch "$LEDGER"

# ---- portability shims (macOS vs Linux) -------------------------------------
case "$(uname -s)" in Darwin) IS_MAC=1 ;; *) IS_MAC=0 ;; esac

# epoch mtime of a file
_mtime() { if [ "$IS_MAC" = 1 ]; then stat -f %m "$1"; else stat -c %Y "$1"; fi; }
# format an epoch with a strftime string (e.g. "+%Y-%m-%d_%H-%M-%S")
_fmtdate() { if [ "$IS_MAC" = 1 ]; then date -r "$1" "$2"; else date -d "@$1" "$2"; fi; }
# TCP connect probe (~1s). BSD nc uses -G for connect timeout; GNU/ncat don't.
_ncz() {
  if [ "$IS_MAC" = 1 ]; then nc -z -G 1 -w 1 "$1" "$2" >/dev/null 2>&1
  else nc -z -w 1 "$1" "$2" >/dev/null 2>&1; fi
}
# this machine's LAN IP (for the subnet scan)
_localip() {
  if [ "$IS_MAC" = 1 ]; then
    local iface; iface="$(route -n get default 2>/dev/null | awk '/interface:/{print $2; exit}')"
    ipconfig getifaddr "${iface:-en0}" 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true
  else
    ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}' \
      || hostname -I 2>/dev/null | awk '{print $1}'
  fi
}
# -----------------------------------------------------------------------------

# Map a camera file to a friendly label.
label_for() {
  case "$1" in
    fcamera.hevc) echo "road" ;;
    ecamera.hevc) echo "wide" ;;
    dcamera.hevc) echo "driver" ;;
    *)            echo "${1%.hevc}" ;;
  esac
}

# Recording start stamp for a route, from the earliest segment's file mtime
# (rsync -a preserves the device's mtimes). Prints YYYY-MM-DD_HH-MM-SS.
route_start_stamp() {
  local route="$1" seg0 epoch f
  seg0="$(find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec basename {} \; \
          | while IFS= read -r d; do printf '%s\t%s\n' "${d##*--}" "$d"; done \
          | sort -n -k1,1 | head -1 | cut -f2)"
  [ -n "$seg0" ] || return 1
  epoch="$(for f in "$STAGING/$seg0"/*.hevc; do [ -e "$f" ] && _mtime "$f"; done | sort -n | head -1)"
  [ -n "$epoch" ] || return 1
  _fmtdate "$epoch" "+%Y-%m-%d_%H-%M-%S"
}

# Newest chunk mtime for a route (epoch) — used to tell if a drive is still recording.
route_newest_mtime() {
  local route="$1" s f
  for s in "$STAGING/${route}--"*; do
    [ -d "$s" ] || continue
    for f in "$s"/*.hevc; do [ -e "$f" ] && _mtime "$f"; done
  done | sort -n | tail -1
}

# Quick TCP check so a stale/expected IP fails fast (~1s) instead of a long SSH timeout.
port_open() { _ncz "$1" "$REMOTE_PORT"; }

# Is the host at $1 actually our comma? (SSH in with our key, look for the
# openpilot install — present on every comma even before any drives are recorded)
is_comma() {
  ssh $SSH_BASE -o BatchMode=yes -o ConnectTimeout=4 \
    "${REMOTE_USER}@$1" 'test -d /data/openpilot' >/dev/null 2>&1
}

# Find the comma's current IP: try preferred + cached first, then scan the subnet.
# On success, sets COMMA_IP and caches it. Returns non-zero if nothing is found.
resolve_comma_ip() {
  local ip cached
  cached="$(cat "$IPCACHE" 2>/dev/null || true)"

  # Fast path: a fixed COMMA_IP (if set) or the last-known IP — but only bother
  # with a full SSH probe when the port is actually open, so a stale IP is cheap.
  for ip in "$COMMA_IP" "$cached"; do
    [ -n "$ip" ] || continue
    if port_open "$ip" && is_comma "$ip"; then
      COMMA_IP="$ip"; echo "$ip" > "$IPCACHE"
      echo "==> Comma found at ${ip}" >&2; return 0
    fi
  done

  if [ "$AUTO_DISCOVER" != "1" ]; then
    echo "!! Couldn't reach the comma at ${COMMA_IP} (AUTO_DISCOVER is off)." >&2; return 1
  fi
  if ! command -v nc >/dev/null 2>&1; then
    echo "!! 'nc' not found, can't scan the network. Set COMMA_IP manually." >&2; return 1
  fi

  # Find our active interface's subnet (e.g. 192.168.1).
  local iface localip base i tmp
  localip="$(_localip)"
  if [ -z "$localip" ]; then
    echo "!! Couldn't determine this Mac's network. Set COMMA_IP manually." >&2; return 1
  fi
  base="${localip%.*}"

  echo "==> Scanning ${base}.0/24 for the comma (port ${REMOTE_PORT})..." >&2
  tmp="$(mktemp)"
  # Parallel TCP scan, throttled in batches so we don't spawn 254 procs at once.
  for i in $(seq 1 254); do
    ( _ncz "${base}.${i}" "$REMOTE_PORT" && echo "${base}.${i}" >> "$tmp" ) &
    if [ $((i % 64)) -eq 0 ]; then wait; fi
  done
  wait

  # Probe each host with an open port; first real comma wins.
  while IFS= read -r ip; do
    [ "$ip" = "$localip" ] && continue
    if is_comma "$ip"; then
      COMMA_IP="$ip"; echo "$ip" > "$IPCACHE"; rm -f "$tmp"
      echo "==> Comma found at ${ip}" >&2; return 0
    fi
  done < <(sort -t. -k4 -n "$tmp")
  rm -f "$tmp"

  echo "!! No comma device found on ${base}.0/24." >&2
  echo "   Make sure it's powered on and on the same WiFi, then re-run." >&2
  return 1
}

# One-shot remote scan: per route still on the comma, print
#   route|earliest_mtime_epoch|cam1.hevc,cam2.hevc|segments|sizeKB
device_drives_remote() {
  resolve_comma_ip || return 1
  ssh $SSH_BASE -o BatchMode=yes -o ConnectTimeout=8 "${REMOTE_USER}@${COMMA_IP}" '
    cd /data/media/0/realdata 2>/dev/null || exit 0
    for r in $(ls -1d *--*/ 2>/dev/null | sed -E "s#--[0-9]+/##" | sort -u); do
      [ "$r" = "boot" ] && continue
      cnt=$(ls -1d ${r}--*/ 2>/dev/null | wc -l | tr -d " ")
      [ "$cnt" = "0" ] && continue
      mt=$(for f in ${r}--*/*.hevc; do [ -e "$f" ] && stat -c %Y "$f"; done | sort -n | head -1)
      cams=$(ls ${r}--*/*.hevc 2>/dev/null | xargs -n1 basename 2>/dev/null | sort -u | tr "\n" "," | sed "s/,$//")
      sz=$(du -sk ${r}--* 2>/dev/null | awk "{s+=\$1} END{print s+0}")
      echo "${r}|${mt}|${cams}|${cnt}|${sz}"
    done
  ' 2>/dev/null
}

# Pull just one route (hevc + qcamera.ts) off the comma into the chunks folder.
pull_route() {
  local route="$1"
  resolve_comma_ip || return 1
  echo "   downloading ${route} from ${COMMA_IP}..."
  local attempt=0 max="${MAX_ATTEMPTS:-40}"
  until "$RSYNC" -a --info=progress2 --no-inc-recursive --partial --append-verify --timeout=120 --prune-empty-dirs \
    --rsync-path="nice -n 19 rsync" ${BWLIMIT:+--bwlimit=$BWLIMIT} \
    --include="${route}--*/" \
    --include="${route}--*/*.hevc" \
    --include="${route}--*/qcamera.ts" \
    --exclude='*' \
    -e "$SSH_OPTS" \
    "${REMOTE_USER}@${COMMA_IP}:${REMOTE_PATH}" "$STAGING/"; do
    attempt=$((attempt + 1))
    [ "$attempt" -ge "$max" ] && { echo "   !! gave up after ${max} reconnect attempts (re-run to resume)"; return 1; }
    echo "   ...dropped (the comma may be rebooting); reconnecting (attempt ${attempt}/${max})..."
    sleep 8
    if [ $((attempt % 6)) -eq 0 ] && ! port_open "$COMMA_IP" "$REMOTE_PORT"; then
      resolve_comma_ip >/dev/null 2>&1 || true
    fi
  done
}

# Comma-separated camera filenames -> friendly labels (ecamera.hevc,fcamera.hevc -> wide,road)
labels_csv() {
  local IFS=',' cam out=""
  for cam in $1; do
    [ -n "$cam" ] || continue
    out="${out:+$out,}$(label_for "$cam")"
  done
  echo "$out"
}

# Is a route fully downloaded? 0 = complete (segments are contiguous 0..max, and
# match the device's count when known), 1 = still partial. Empty dirs are pruned
# before this runs, so only segments with footage are counted.
route_complete() {
  local route="$1" nums cnt max expected
  nums="$(find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec basename {} \; 2>/dev/null \
          | sed -E 's/.*--//' | sort -n)"
  [ -z "$nums" ] && return 1
  cnt="$(printf '%s\n' "$nums" | grep -c .)"
  max="$(printf '%s\n' "$nums" | tail -1)"
  [ "$cnt" -ne "$((max + 1))" ] && return 1                          # gap = missing segments
  expected="$(awk -v r="$route" '$1==r{print $2}' "${DEVCOUNTS:-/dev/null}" 2>/dev/null)"
  [ -n "$expected" ] && [ "$cnt" -lt "$expected" ] && return 1       # missing trailing segments
  return 0
}

# Stitch one drive's local chunks into MP4s. $2=1 enables collision-safe naming
# (append " (N)" so a re-sync into an existing folder never overwrites old files).
# Returns 0 only if every camera stitched. Does NOT touch the ledger or chunks.
stitch_route() {
  local route="$1" collision="${2:-0}" stamp outdir line s f
  stamp="$(route_start_stamp "$route" 2>/dev/null || true)"
  [ -n "$stamp" ] || stamp="$route"
  outdir="$ROOT/$stamp"
  mkdir -p "$outdir"

  # Segment dirs for this route, sorted by trailing segment number (numeric).
  local segs=()
  while IFS= read -r line; do segs+=("$line"); done < <(
    find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec basename {} \; \
      | while IFS= read -r d; do printf '%s\t%s\n' "${d##*--}" "$d"; done \
      | sort -n -k1,1 | cut -f2
  )
  if [ "${#segs[@]}" -eq 0 ]; then echo "   !! no raw chunks for ${route}"; return 1; fi

  # Cameras present (iterate the glob directly so paths with spaces don't split).
  local cams=()
  while IFS= read -r line; do cams+=("$line"); done < <(
    for s in "${segs[@]}"; do
      for f in "$STAGING/$s"/*.hevc; do [ -e "$f" ] && basename "$f"; done
    done | sort -u
  )
  if [ "${#cams[@]}" -eq 0 ]; then echo "   !! no camera footage downloaded for ${route} yet"; return 1; fi

  # Collision-safe suffix: lowest N (1 = none) where no camera output exists yet.
  local suffix="" cam lbl
  if [ "$collision" = "1" ]; then
    local n=1 sfx hit
    while :; do
      [ "$n" -eq 1 ] && sfx="" || sfx=" ($n)"
      hit=0
      for cam in "${cams[@]}"; do
        lbl="$(label_for "$cam")"
        [ -e "$outdir/${stamp}__${lbl}${sfx}.mp4" ] && { hit=1; break; }
      done
      [ "$hit" -eq 0 ] && { suffix="$sfx"; break; }
      n=$((n + 1))
    done
  fi

  echo "==> Stitching drive ${route}  ->  ${stamp}${suffix}"

  # Microphone audio (from qcamera.ts), if it was recorded.
  local audio_ts="" cand
  if [ "$WITH_AUDIO" != "0" ]; then
    cand="$(mktemp "${TMPDIR:-/tmp}/comma_aud_XXXXXX.ts")"
    for s in "${segs[@]}"; do
      [ -f "$STAGING/$s/qcamera.ts" ] && cat "$STAGING/$s/qcamera.ts" >> "$cand"
    done
    if [ -s "$cand" ] && ffprobe -v error -select_streams a -show_entries stream=codec_type \
         -of csv=p=0 "$cand" 2>/dev/null | grep -q audio; then
      audio_ts="$cand"
    else
      rm -f "$cand"
    fi
  fi

  local ok=1 out combined rc atag
  for cam in "${cams[@]}"; do
    lbl="$(label_for "$cam")"
    out="$outdir/${stamp}__${lbl}${suffix}.mp4"
    combined="$(mktemp "${TMPDIR:-/tmp}/comma_XXXXXX.hevc")"
    for s in "${segs[@]}"; do
      [ -f "$STAGING/$s/$cam" ] && cat "$STAGING/$s/$cam" >> "$combined"
    done
    rc=0
    if [ -n "$audio_ts" ]; then
      ffmpeg -y -loglevel error -framerate "$FPS" -i "$combined" -i "$audio_ts" \
        -map 0:v:0 -map 1:a:0 -c:v copy -c:a copy -tag:v hvc1 "$out" || rc=$?
    else
      ffmpeg -y -loglevel error -framerate "$FPS" -i "$combined" \
        -c copy -tag:v hvc1 "$out" || rc=$?
    fi
    if [ "$rc" -eq 0 ]; then
      [ -n "$audio_ts" ] && atag=" +audio" || atag=""
      echo "      ${lbl}${atag}: $(basename "$out")"
    else
      echo "      !! ffmpeg failed for ${lbl} on ${route}"; ok=0
    fi
    rm -f "$combined"
  done
  [ -n "$audio_ts" ] && rm -f "$audio_ts"
  [ "$ok" = "1" ]
}

# Does a drive have audio? (check its last segment's qcamera.ts) -> echoes 1/0
drive_has_audio() {
  local route="$1" last
  last="$(find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec basename {} \; \
          | while IFS= read -r d; do printf '%s\t%s\n' "${d##*--}" "$d"; done \
          | sort -n -k1,1 | tail -1 | cut -f2)"
  if [ -n "$last" ] && [ -f "$STAGING/$last/qcamera.ts" ] \
     && ffprobe -v error -select_streams a -show_entries stream=codec_type -of csv=p=0 \
        "$STAGING/$last/qcamera.ts" 2>/dev/null | grep -q audio; then
    echo 1
  else
    echo 0
  fi
}

# --list: one TSV row per drive, merging chunks on this Mac with drives still on
# the comma (re-downloadable). Columns:
#   route <tab> stamp <tab> cameras(csv) <tab> hasAudio(0/1/-) <tab> sizeKB <tab> segments <tab> location
# location is "local" (chunks on disk) or "device" (only on the comma).
cmd_list() {
  local route stamp cams_csv audio sizek segcount s f
  local seen; seen="$(mktemp)"

  # 1) Drives whose chunks are on this Mac.
  while IFS= read -r route; do
    [ -n "$route" ] || continue
    echo "$route" >> "$seen"
    stamp="$(route_start_stamp "$route" 2>/dev/null || echo "$route")"
    cams_csv="$(
      for s in $(find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec basename {} \;); do
        for f in "$STAGING/$s"/*.hevc; do [ -e "$f" ] && basename "$f"; done
      done | sort -u | while IFS= read -r c; do label_for "$c"; done | paste -sd ',' -
    )"
    audio="$(drive_has_audio "$route")"
    segcount="$(find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" | wc -l | tr -d ' ')"
    sizek="$(du -sk "$STAGING"/${route}--* 2>/dev/null | awk '{s+=$1} END{print s+0}' || true)"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$route" "$stamp" "$cams_csv" "$audio" "$sizek" "$segcount" "local"
  done < <(
    find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name '*--*' -exec basename {} \; \
      | sed -E 's/--[0-9]+$//' | sort -u
  )

  # 2) Drives still on the comma that aren't already on this Mac (re-downloadable).
  local r mt cams cnt sz dstamp
  while IFS='|' read -r r mt cams cnt sz; do
    [ -n "$r" ] || continue
    grep -qxF "$r" "$seen" && continue
    if [ -n "$mt" ]; then dstamp="$(_fmtdate "$mt" "+%Y-%m-%d_%H-%M-%S")"; else dstamp="$r"; fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$r" "$dstamp" "$(labels_csv "$cams")" "-" "${sz:-0}" "${cnt:-0}" "device"
  done < <(device_drives_remote || true)

  rm -f "$seen"
}

# --restitch <route>: re-stitch a drive with the current settings, never
# overwriting existing output. Re-downloads the chunks from the comma if needed.
cmd_restitch() {
  local route="$1"
  [ -n "$route" ] || { echo "!! usage: --restitch <route>"; return 2; }
  if ! find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" 2>/dev/null | grep -q .; then
    echo "==> No local chunks for ${route} — fetching from the comma..."
    pull_route "$route" || {
      echo "!! Couldn't download ${route} (comma offline, or the drive is no longer on it)."
      return 1
    }
    if ! find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" 2>/dev/null | grep -q .; then
      echo "!! ${route} is not on the comma anymore."
      return 1
    fi
  fi
  stitch_route "$route" 1 || return 1
  echo
  echo "==> Re-sync complete. Output in: ${ROOT}/"
}

# Subcommands used by the app (list synced drives / re-sync one). Default = sync.
case "${1:-}" in
  --list)     cmd_list; exit 0 ;;
  --restitch) cmd_restitch "${2:-}" && exit 0 || exit 1 ;;
esac

# --- 1. Pull only new *.hevc, skipping drives we've already processed ---------
EXCLUDES="$(mktemp)"
# (EXCLUDES is removed by the global cleanup() trap set above)
# Turn each processed route into an rsync exclude pattern so it's never re-pulled.
while IFS= read -r route; do
  [ -n "$route" ] && printf '%s--*\n' "$route" >> "$EXCLUDES"
done < "$LEDGER"

if resolve_comma_ip; then
  echo "==> Syncing new footage from ${REMOTE_USER}@${COMMA_IP} ..."
  # comma devices drop WiFi often, so keep partial files and retry on disconnects.
  # --partial/--append-verify resumes interrupted files; the ledger (above) is what
  # prevents re-downloading already-processed drives, so --ignore-existing isn't needed.
  # The comma may reboot repeatedly mid-transfer (e.g. on weak power). rsync's
  # --partial/--append-verify resumes each interrupted file (finished files are
  # skipped, so nothing already downloaded is re-transferred), and the ledger keeps
  # done drives out entirely. We just need to outlast the reboots and re-find the
  # device if it comes back on a new IP — so retry generously and re-discover.
  attempt=0; max_attempts="${MAX_ATTEMPTS:-40}"
  until "$RSYNC" -a --info=progress2 --no-inc-recursive --partial --append-verify --timeout=120 --prune-empty-dirs \
    --rsync-path="nice -n 19 rsync" ${BWLIMIT:+--bwlimit=$BWLIMIT} \
    --exclude-from="$EXCLUDES" \
    --include='*/' --include='*.hevc' --include='qcamera.ts' --exclude='*' \
    -e "$SSH_OPTS" \
    "${REMOTE_USER}@${COMMA_IP}:${REMOTE_PATH}" \
    "$STAGING/"; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge "$max_attempts" ]; then
      echo "!! Paused after ${max_attempts} reconnect attempts. Just run it again —"
      echo "   finished files are kept and resume, so nothing already downloaded re-transfers."
      break
    fi
    echo "   ...dropped (the comma may be rebooting); waiting for it to come back (attempt ${attempt}/${max_attempts})..."
    sleep 8
    # Usually it returns on the same IP (cheap retry). Every few tries, re-discover
    # in case a reboot moved it to a new address — rsync resumes either way.
    if [ $((attempt % 6)) -eq 0 ] && ! port_open "$COMMA_IP" "$REMOTE_PORT"; then
      resolve_comma_ip >/dev/null 2>&1 || true
    fi
  done
else
  echo "==> Skipping download; stitching whatever is already in ${STAGING}/."
fi

# --- 1b. Clean up partial-transfer debris + learn which drives finished -------
# Interrupted transfers (the comma rebooting) leave empty segment dirs — the dir
# is created before its hevc is pulled. Remove them so only complete segments count.
find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name '*--*' 2>/dev/null | while IFS= read -r d; do
  [ -z "$(find "$d" -maxdepth 1 -name '*.hevc' -print -quit 2>/dev/null)" ] && rm -rf "$d"
done

# Authoritative segment count per route from the device (only if it's reachable
# right now — no extra scan), so we skip drives that are still downloading.
DEVCOUNTS="$(mktemp)"
if [ -n "${COMMA_IP:-}" ] && port_open "$COMMA_IP" "$REMOTE_PORT"; then
  ssh $SSH_BASE -o BatchMode=yes -o ConnectTimeout=8 "${REMOTE_USER}@${COMMA_IP}" '
    cd /data/media/0/realdata 2>/dev/null || exit 0
    for r in $(ls -1d *--*/ 2>/dev/null | sed -E "s#--[0-9]+/##" | sort -u); do
      [ "$r" = "boot" ] && continue
      echo "${r} $(ls -1d ${r}--*/ 2>/dev/null | wc -l | tr -d " ")"
    done' 2>/dev/null > "$DEVCOUNTS" || true
fi

# --- 2. Figure out which drives (routes) are staged --------------------------
ROUTES=()
while IFS= read -r line; do ROUTES+=("$line"); done < <(
  find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name '*--*' -exec basename {} \; \
    | sed -E 's/--[0-9]+$//' | sort -u
)

if [ "${#ROUTES[@]}" -eq 0 ]; then
  echo "==> No new footage to process."
  exit 0
fi

# --- 3. Stitch each drive ----------------------------------------------------
for route in "${ROUTES[@]}"; do
  # Already stitched on a previous run? Skip (its raw chunks are just being kept).
  if grep -qxF "$route" "$LEDGER"; then
    continue
  fi
  # Not fully downloaded yet (the comma may still be rebooting)? Leave it for the
  # next run rather than stitching a partial drive.
  if ! route_complete "$route"; then
    echo "==> Skipping ${route}: still downloading (incomplete) — re-run to finish it."
    continue
  fi
  # Hold back a drive only if it looks like it's still being recorded right now
  # (its newest chunk was written within the last MIN_AGE_SECS seconds).
  if [ "$SKIP_LATEST" != "0" ] && [ "$MIN_AGE_SECS" -gt 0 ]; then
    newest="$(route_newest_mtime "$route")"
    if [ -n "$newest" ]; then
      age=$(( $(date +%s) - newest ))
      if [ "$age" -lt "$MIN_AGE_SECS" ]; then
        echo "==> Skipping ${route}: last chunk written ${age}s ago (still recording?). Re-run later."
        continue
      fi
    fi
  fi

  # --- 4. Stitch; on full success record the drive (+ optionally purge chunks) -
  if stitch_route "$route" 0; then
    grep -qxF "$route" "$LEDGER" || echo "$route" >> "$LEDGER"
    if [ "$CLEAN_RAW" = "1" ]; then
      find "$STAGING" -mindepth 1 -maxdepth 1 -type d -name "${route}--*" -exec rm -rf {} +
      echo "      purged raw chunks for ${route}"
    else
      echo "      raw chunks kept in: ${STAGING}"
    fi
  fi
done

echo
echo "==> Done. Stitched drives are in: ${ROOT}/"
echo "    Raw chunks are in:         ${STAGING}/"
echo "    Delete that folder anytime to reclaim space (won't trigger re-downloads)."
