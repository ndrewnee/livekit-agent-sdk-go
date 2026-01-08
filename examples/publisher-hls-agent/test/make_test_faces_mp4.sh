#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT_DIR}/test_faces.mp4"
DEFAULT_URL="https://www.youtube.com/watch?v=OtOcELHsEaw"
INPUT="${1:-$DEFAULT_URL}"

command -v ffmpeg >/dev/null 2>&1 || { echo "ffmpeg not found in PATH" >&2; exit 1; }

TMP=""
cleanup() {
  if [[ -n "${TMP}" && -d "${TMP}" && "${KEEP_SOURCE:-0}" != "1" ]]; then
    rm -rf "${TMP}"
  fi
}
trap cleanup EXIT

SRC="${INPUT}"
if [[ ! -f "${SRC}" ]]; then
  command -v yt-dlp >/dev/null 2>&1 || { echo "yt-dlp not found in PATH" >&2; exit 1; }
  TMP="$(mktemp -d)"
  echo "downloading ${INPUT} (up to 1080p)..." >&2
  yt-dlp -f "bv*[height<=1080]+ba/b[height<=1080]/best" --merge-output-format mkv \
    -o "${TMP}/source.%(ext)s" \
    "${INPUT}" >&2
  SRC="$(ls -1 "${TMP}"/source.* | head -n 1)"
fi

if [[ ! -f "${SRC}" ]]; then
  echo "source file not found: ${SRC}" >&2
  exit 1
fi

# Cuts 00:02:18..00:03:18 and re-encodes to AV1/Opus at 1080p.
# Uses SVT-AV1 for speed; tweak CRF/preset as needed for your machine.
ffmpeg -hide_banner -nostdin -y \
  -ss 00:02:18 -i "${SRC}" -t 60 \
  -vf "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2" \
  -c:v libsvtav1 -preset 8 -crf 35 -g 120 -pix_fmt yuv420p \
  -c:a libopus -b:a 128k -ar 48000 -ac 2 \
  -movflags +faststart \
  "${OUT}"

echo "wrote ${OUT}"
