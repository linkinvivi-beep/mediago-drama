#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
SOURCE_SVG="${WORKSPACE_DIR}/design/icons/medialink/icon.svg"
OUTPUT_DIR="${WORKSPACE_DIR}/build/icons"
ICONSET_DIR="${OUTPUT_DIR}/icon.iconset"
OUTPUT_ICNS="${OUTPUT_DIR}/icon.icns"
MASTER_PNG="${ICONSET_DIR}/icon.svg.png"

if [[ ! -f "${SOURCE_SVG}" ]]; then
	printf 'MediaLink icon source not found: %s\n' "${SOURCE_SVG}" >&2
	exit 1
fi

if [[ ! -x /usr/bin/qlmanage || ! -x /usr/bin/sips || ! -x /usr/bin/iconutil ]]; then
	printf 'MediaLink icon generation requires macOS qlmanage, sips, and iconutil.\n' >&2
	exit 1
fi

cleanup() {
	rm -rf -- "${ICONSET_DIR}"
}
trap cleanup EXIT

mkdir -p -- "${OUTPUT_DIR}"
cleanup
mkdir -p -- "${ICONSET_DIR}"

/usr/bin/qlmanage \
	-t \
	-s 1024 \
	-o "${ICONSET_DIR}" \
	"${SOURCE_SVG}" >/dev/null

if [[ ! -f "${MASTER_PNG}" ]]; then
	printf 'macOS Quick Look did not render the MediaLink SVG.\n' >&2
	exit 1
fi

render_icon() {
	local pixels="$1"
	local filename="$2"
	/usr/bin/sips \
		-s format png \
		--resampleHeightWidth "${pixels}" "${pixels}" \
		"${MASTER_PNG}" \
		--out "${ICONSET_DIR}/${filename}" >/dev/null
}

render_icon 16 icon_16x16.png
render_icon 32 icon_16x16@2x.png
render_icon 32 icon_32x32.png
render_icon 64 icon_32x32@2x.png
render_icon 128 icon_128x128.png
render_icon 256 icon_128x128@2x.png
render_icon 256 icon_256x256.png
render_icon 512 icon_256x256@2x.png
render_icon 512 icon_512x512.png
render_icon 1024 icon_512x512@2x.png

rm -f -- "${MASTER_PNG}"
/usr/bin/iconutil -c icns "${ICONSET_DIR}" -o "${OUTPUT_ICNS}"
printf 'Built MediaLink icon: %s\n' "${OUTPUT_ICNS}"
