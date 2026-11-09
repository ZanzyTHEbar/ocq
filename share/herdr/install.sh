#!/bin/sh
# Explicitly install a personal Herdr layout; never modify a shipped layout.
set -eu
umask 077

if [ "$#" -ne 3 ] || [ "$3" != "--activate" ]; then
  echo "usage: sh share/herdr/install.sh HERDR_APP_HOME HERDR_APP --activate" >&2
  echo "Requires a running Herdr app 0.1.14; switches it to a new personal ocq-workspace layout." >&2
  exit 2
fi

home=$1
app=$2
case "$home" in /*) ;; *) echo "HERDR_APP_HOME must be an absolute path" >&2; exit 2 ;; esac
here=$(CDPATH= cd -P "$(dirname "$0")" && pwd -P)

if [ ! -d "$home/mods" ] || [ ! -d "$home/layouts" ] || [ -L "$home/mods" ] || [ -L "$home/layouts" ]; then
  echo "Start Herdr once with this app home first; mods and layouts must be real directories." >&2
  exit 1
fi
if [ "$("$app" --version)" != "herdr-app 0.1.14" ]; then
  echo "This personal layout is validated only with herdr-app 0.1.14." >&2
  exit 1
fi
if ! HERDR_APP_HOME="$home" "$app" status >/dev/null; then
  echo "No running Herdr app found for the specified home; no files changed." >&2
  exit 1
fi
mod="$home/mods/ocq"
layout="$home/layouts/ocq-workspace.json"
if [ -e "$mod" ] || [ -L "$mod" ] || [ -e "$layout" ] || [ -L "$layout" ]; then
  echo "OCQ mod or ocq-workspace layout already exists; refusing to overwrite either." >&2
  exit 1
fi

echo "Installing a trusted mod that runs ocq-herdr on the active Herdr machine."
echo "The bridge must already be available on that machine's Herdr core PATH."
mkdir "$mod"
cp "$here/mods/ocq/main.js" "$mod/main.js"
cp "$here/mods/ocq/mod.json" "$mod/mod.json"
if ! HERDR_APP_HOME="$home" "$app" check "$mod"; then
  echo "Mod check failed; the newly created mod directory was retained for inspection." >&2
  exit 1
fi
if ! (set -C; cat "$here/layouts/ocq-workspace.json" > "$layout"); then
  echo "Could not create the personal layout without overwriting; the new mod was retained." >&2
  exit 1
fi
if ! HERDR_APP_HOME="$home" "$app" do layout.switch --name ocq-workspace; then
  echo "Layout switch failed or its response was lost; inspect Herdr before retrying. Files were retained." >&2
  exit 1
fi
echo "Requested the personal ocq-workspace layout. Verify it in Herdr; your previous layout is retained."
