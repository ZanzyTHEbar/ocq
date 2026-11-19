#!/bin/sh
# Canonical personal installation; never modify a shipped layout or queue.
set -eu
umask 077
if [ "$#" -ne 3 ] || { [ "$3" != "--activate" ] && [ "$3" != "--plan" ]; }; then
  echo "usage: sh share/herdr/install.sh HERDR_APP_HOME HERDR_APP --plan|--activate" >&2
  exit 2
fi
case "$1" in /*) ;; *) echo "HERDR_APP_HOME must be an absolute path" >&2; exit 2 ;; esac
here=$(CDPATH= cd -P "$(dirname "$0")" && pwd -P)
command -v node >/dev/null 2>&1 || { echo "Node.js 18+ is required for structured, ownership-checked installation." >&2; exit 1; }
exec node "$here/install.mjs" "$1" "$2" "$3"
