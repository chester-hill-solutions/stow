#!/usr/bin/env bash
# Render the launch graphics from site/launch/*.html into site/images/*.png.
#
# The cards are HTML rather than generated images so the typography is exact.
# A launch asset with a misspelt word is a launch asset that ships a mistake,
# and image models misspell. This shells out to the Playwright CLI rather than
# taking a Node dependency, so it works on a clean checkout.
#
#   site/render.sh            # all cards
#   site/render.sh 03-session # one card, by name

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
launch="$here/launch"
out="$here/images"
playwright=(npx --yes playwright@1.63.0 screenshot)

mkdir -p "$out"

render() {
  local name="$1" width="$2" height="$3"
  printf 'rendering %-16s %sx%s\n' "$name" "$width" "$height"
  "${playwright[@]}" \
    --viewport-size="${width},${height}" \
    --wait-for-timeout=300 \
    "file://$launch/$name.html" \
    "$out/$name.png" >/dev/null
}

if [ "$#" -gt 0 ]; then
  for name in "$@"; do
    # og is the odd one out: social cards are 1200x630, not 2400x1260.
    if [ "$name" = "og" ]; then render og 1200 630; else render "$name" 2400 1260; fi
  done
else
  for card in "$launch"/*.html; do
    name="$(basename "$card" .html)"
    if [ "$name" = "og" ]; then render og 1200 630; else render "$name" 2400 1260; fi
  done
fi

echo
ls -la "$out"
