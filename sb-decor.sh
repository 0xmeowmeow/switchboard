#!/usr/bin/env bash
# Set up the decor folder and seed it from your ANSI collection.
set -eu
D="$HOME/.config/switchboard/decor"
mkdir -p "$D"
n=0
for f in "$HOME"/data/art/digital-art/ansi/*.ans "$HOME"/data/art/digital-art/ansi/**/*.ans; do
  [ -f "$f" ] || continue
  # decoration must be small: under 14 rows, under 60 columns
  rows=$(wc -l < "$f")
  [ "$rows" -gt 14 ] && continue
  cp -n "$f" "$D/" 2>/dev/null && n=$((n+1))
done
echo "decor dir: $D"
echo "seeded:    $n file(s) from your .ans collection"
echo
echo "Anything under 14 rows and 60 columns is drawn in the left margin."
echo ".ans keeps its own colours; .txt and .asc are drawn in the theme's tone."
echo "Make one with:  tdf -f Impossible -m sb > $D/banner.txt"
