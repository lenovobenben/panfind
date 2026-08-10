#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/../../../.." && pwd)

if [ "${PANFIND_PATH:-}" != "" ]; then
    if [ ! -f "$PANFIND_PATH" ]; then
        echo "PanFind executable does not exist: $PANFIND_PATH" >&2
        exit 2
    fi
    exec "$PANFIND_PATH" "$@"
fi

case "$(uname -m)" in
    arm64|aarch64)
        release_name=panfind-macos-arm64
        ;;
    x86_64|amd64)
        release_name=panfind-macos-amd64
        ;;
    *)
        echo "Unsupported macOS architecture: $(uname -m)" >&2
        exit 2
        ;;
esac

for candidate in \
    "$repository_root/$release_name" \
    "$repository_root/dist/$release_name" \
    "$repository_root/panfind" \
    "$repository_root/bin/panfind"
do
    if [ -x "$candidate" ]; then
        exec "$candidate" "$@"
    fi
done

if command -v panfind >/dev/null 2>&1; then
    exec panfind "$@"
fi

echo "PanFind was not found. Extract $release_name to the repository root or build PanFind first." >&2
exit 2
