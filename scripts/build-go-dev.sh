#!/bin/sh
# Build a manifest-checked development payload. This is not an SDK package or
# a signed release: install it only in the isolated development VM.
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir=${1:-"$project_dir/.codex/go-loop/build/go-dev"}
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/smart-srun-dev.XXXXXX")
trap 'rm -rf -- "$stage_dir"' EXIT HUP INT TERM

revision=$(git -C "$project_dir" rev-parse --short=12 HEAD)
if [ -n "$(git -C "$project_dir" status --porcelain -- core)" ]; then revision="$revision.dirty"; fi
version="0.0.0-dev.$revision"
arch=${GOARCH:-$(go env GOARCH)}
# targets.json owns the payload budget. Read it rather than repeating it here,
# and refuse to build on anything that is not a plain decimal count of bytes --
# a silently empty value would turn the check below into a comparison against
# nothing, which passes.
payload_limit=$(sed -n 's/.*"development_payload_limit_bytes"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' \
    "$project_dir/targets.json")
case "$payload_limit" in
    ''|*[!0-9]*) echo "Cannot read development_payload_limit_bytes from targets.json" >&2; exit 1 ;;
esac
mkdir -p "$stage_dir/usr/bin" "$stage_dir/etc/init.d" \
    "$stage_dir/usr/share/smart-srun" \
    "$stage_dir/usr/lib/lua/luci/controller" "$stage_dir/usr/lib/lua/luci/model/cbi" \
    "$stage_dir/usr/lib/lua/luci/smart_srun" \
    "$stage_dir/www/luci-static/resources"
# PREBUILT_SRUNNET exists for a Windows checkout whose POSIX shell has no Go
# toolchain: the cross build runs outside, this assembles the same payload. It
# must be the ELF built from this tree at this revision; nothing here can check
# that, so the build-info version is the only claim made about it.
if [ -n "${PREBUILT_SRUNNET:-}" ]; then
    cp "$PREBUILT_SRUNNET" "$stage_dir/usr/bin/srunnet"
else
(
    cd "$project_dir/core"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X github.com/matthewlu070111/smart-srun/core/internal/cli.Version=$version" \
        -o "$stage_dir/usr/bin/srunnet" ./cmd/srunnet
)
fi
# Normalize text resource line endings when building a Windows checkout.
sed 's/\r$//' "$project_dir/root/etc/init.d/smart_srun" > "$stage_dir/etc/init.d/smart_srun"
sed 's/\r$//' "$project_dir/root/etc/init.d/smart_srun_update" > "$stage_dir/etc/init.d/smart_srun_update"
cp "$project_dir/doc/school-presets.json" "$stage_dir/usr/share/smart-srun/school-presets.json"
# The interface is part of what has to be installed to try anything through it.
# Same files the package will install; the packaging itself is batch C.
luci_files="usr/share/smart-srun/third-party-licenses.txt
usr/lib/lua/luci/controller/smart_srun.lua
usr/lib/lua/luci/model/cbi/smart_srun.lua
usr/lib/lua/luci/smart_srun/schema.lua
usr/lib/lua/luci/smart_srun/rpc.lua
usr/lib/lua/luci/smart_srun/bridge.lua
www/luci-static/resources/smart_srun.js"
for relative in $luci_files; do
    sed 's/\r$//' "$project_dir/root/$relative" > "$stage_dir/$relative"
    chmod 644 "$stage_dir/$relative"
done
chmod 755 "$stage_dir/usr/bin/srunnet" "$stage_dir/etc/init.d/smart_srun"
chmod 755 "$stage_dir/etc/init.d/smart_srun_update"
chmod 644 "$stage_dir/usr/share/smart-srun/school-presets.json"

(
    cd "$stage_dir"
    sha256sum usr/bin/srunnet etc/init.d/smart_srun etc/init.d/smart_srun_update \
        usr/share/smart-srun/school-presets.json $luci_files > manifest.sha256
    payload_bytes=$(wc -c < usr/bin/srunnet)
    payload_bytes=$((payload_bytes + $(wc -c < etc/init.d/smart_srun) + $(wc -c < usr/share/smart-srun/school-presets.json)))
    payload_bytes=$((payload_bytes + $(wc -c < etc/init.d/smart_srun_update)))
    for relative in $luci_files; do
        payload_bytes=$((payload_bytes + $(wc -c < "$relative")))
    done
    if [ "$payload_bytes" -gt "$payload_limit" ]; then
        echo "Development payload exceeds the $payload_limit byte budget: $payload_bytes bytes" >&2
        exit 1
    fi
    printf 'version=%s\narch=%s\ninstalled_payload_bytes=%s\nformat=development-tar-not-sdk-package\n' \
        "$version" "$arch" "$payload_bytes" > build-info.txt
    tar -czf "$output_dir/smart-srun-dev-$arch.tar.gz" \
        manifest.sha256 build-info.txt usr/bin/srunnet etc/init.d/smart_srun etc/init.d/smart_srun_update \
        usr/share/smart-srun/school-presets.json $luci_files
    cat build-info.txt
)
sha256sum "$output_dir/smart-srun-dev-$arch.tar.gz"
