#!/bin/sh
# One queue/UI-free HC stdio observation. No machine registration or mutation.
set -eu
if [ "${1-}" != --owned ]; then
 test "$#" = 4
 proof=$(realpath "$1"); owner=$(realpath "$2"); hc=$(realpath "$3"); guard=$(realpath "$4")
 umask 077
 root=$(mktemp -d "${XDG_STATE_HOME:-$HOME/.local/state}/ocq-stdio-XXXXXXXX")
 chmod 700 "$root"
 printf '%s\n' "$owner" > "$root/owner-path"
 printf 'Transport evidence: %s\n' "$root"
 PROBE_ROOT="$root" "$owner" cleanup-owner "$PWD/proof/herdr-compatibility/transport.sh" "$proof" "$hc" "$guard"
 exit
fi
test "$#" = 5
root=$2; proof=$3; hc=$4; guard=$5
test "${PROBE_ROOT-}" = "$root"
test "${OCQ_FIXTURE_OWNER_PID-}" = "$PPID"
for d in home run tmp peer-config peer-data peer-state; do mkdir -m 700 "$root/$d"; done
owner=$(cat "$root/owner-path")
exec env -i HOME="$root/home" XDG_RUNTIME_DIR="$root/run" TMPDIR="$root/tmp" PROBE_ROOT="$root" PATH=/usr/bin:/bin GOMAXPROCS=2 "$proof" transport-observe "$hc" "$guard" "$owner"
