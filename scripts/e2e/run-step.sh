#!/usr/bin/env bash
# Usage: run-step.sh <NN-name>   — records scripts/e2e/<NN-name>.sh into a v2 cast, then agg -> gif.
set -euo pipefail
E2E_ROOT="${E2E_ROOT:-/tmp/relayflow-e2e}"
GIF_DIR="$E2E_ROOT/gifs"
HERE="$(cd "$(dirname "$0")" && pwd)"
step="$1"
mkdir -p "$GIF_DIR"
asciinema rec -f asciicast-v2 --overwrite --return --cols 160 --rows 40 -c "bash $HERE/$step.sh" "$GIF_DIR/$step.cast" </dev/null
agg --speed 1.5 --cols 160 --rows 40 "$GIF_DIR/$step.cast" "$GIF_DIR/$step.gif"
ls -la "$GIF_DIR/$step.gif"
