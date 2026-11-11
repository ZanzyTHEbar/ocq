#!/bin/sh
# Focused native acceptance, not a mock bridge. Preconditions: the private
# result-publication fixture has prepared A and B in the SAME selected space;
# A's confirmation is open; B sorts first in the retained list. The fixture
# wrapper retains real bridge responses at A-final-status and B-arm barriers.
# Install bin/ocq-herdr in the private PATH as a launcher for:
#   sh ABSOLUTE_PATH_TO_THIS_SCRIPT --bridge PRIVATE_ROOT "$@"
# bin/bridge-real is the actual freshly built bridge, never a mock.
# Native 0.1.14 geometry: 1280x820 window at +62+90, 360px OCQ panel, zoom 1.
# race.json contains {a,b,job}; driver helpers and app are explicit arguments.
set -eu
umask 077
if [ "${1-}" = --bridge ]; then
 shift
 p=$1; shift
 printf '%s %s\n' "$(date +%s%N)" "$*" >> "$p/bridge.trace"
 a=$(jq -r .a "$p/race.json") b=$(jq -r .b "$p/race.json")
 barrier=
 if [ "$1" = result-publish-status ] && [ "${7-}" = "$a" ] && [ -e "$p/hold-A-final" ]; then
  rm "$p/hold-A-final"; barrier=A-final
 elif [ "$1" = result-publish-arm ] && [ "${7-}" = "$b" ] && [ -e "$p/hold-B-arm" ]; then
  rm "$p/hold-B-arm"; barrier=B-arm
 fi
 if [ -n "$barrier" ]; then
  code=0
  "$p/bin/bridge-real" "$@" > "$p/$barrier-response.json" || code=$?
  touch "$p/$barrier.entered"
  while [ ! -e "$p/$barrier.release" ]; do sleep 0.025; done
  cat "$p/$barrier-response.json"; touch "$p/$barrier.returned"; exit "$code"
 fi
 if [ "$1" = result-publish-attempt ] && [ "${7-}" = "$b" ]; then
  code=0
  "$p/bin/bridge-real" "$@" > "$p/B-attempt-response.json" || code=$?
  cat "$p/B-attempt-response.json"; touch "$p/B-attempt.returned"; exit "$code"
 fi
 exec "$p/bin/bridge-real" "$@"
fi
test "$#" = 4 || { echo 'usage: native-delayed-readback.sh PRIVATE_ROOT APP KEY_HELPER CLICK_HELPER' >&2; exit 2; }
p=$1 app=$2 key=$3 click=$4
test "$(jq -r .result "$p/publication-proof.json")" = PASS
test "$(jq -r .state_dir "$p/publication-proof.json")" = "$p/fixture/queue"
v() { "$p/via" "$@"; }
press() { v "$key" "$1"; }
wait_file() {
 n=0
 while test ! -e "$p/$1"; do
  n=$((n+1)); test "$n" -lt 200 || { echo "native barrier timeout: $1" >&2; exit 1; }
  sleep 0.025
 done
}
count() { awk -v a="$1" -v d="$2" '$2==a && $8==d {n++} END {print n+0}' "$p/bridge.trace"; }
associations() { sqlite3 -readonly "$p/fixture/queue/queue.db" "SELECT count(*) FROM project_result_associations WHERE job_id='$job' AND manifest_digest='$1';"; }
a=$(jq -r .a "$p/race.json") b=$(jq -r .b "$p/race.json") job=$(jq -r .job "$p/race.json")
test "$a" != "$b"
test "$(v "$app" --version)" = 'herdr-app 0.1.14'
v "$app" status > "$p/race-start-status.json"
jq -e '.zoom==1 and (.errors|length)==0' "$p/race-start-status.json" >/dev/null
test "$(count result-publish-attempt "$a")" = 0
test "$(count result-publish-arm "$b")" = 0
# Release owned barriers on failure too; never resend any command.
trap 'touch "$p/A-final.release" "$p/B-arm.release"' EXIT
touch "$p/hold-A-final" "$p/hold-B-arm"
v "$click" 1110 798
wait_file A-final.entered
jq -e --arg a "$a" '.state=="recorded_observed" and .manifest_sha256==$a' "$p/A-final-response.json" >/dev/null
# Leave A while its final read-back is pending. Open a fresh generation for B.
press Escape; press Escape; press Escape
for n in 1 2 3 4 5 6 7; do press Down; done
press Return
sleep 0.35
v "$click" 1110 660
sleep 0.2
v "$click" 1110 462
sleep 0.45
for n in $(seq 1 24); do press Right; done
sleep 0.12
v "$click" 1110 844
sleep 0.12
v "$click" 1110 798
wait_file B-arm.entered
jq -e --arg b "$b" '.state=="armed" and .manifest_sha256==$b' "$p/B-arm-response.json" >/dev/null
test "$(count result-publish-arm "$a")" = 1
test "$(count result-publish-attempt "$a")" = 1
test "$(count result-publish-arm "$b")" = 1
test "$(count result-publish-attempt "$b")" = 0
test "$(associations "$a")" = 1
test "$(associations "$b")" = 0
sleep 0.15
v "$app" shot "$p/B-working-before-A-return.png" >/dev/null
magick "$p/B-working-before-A-return.png" -crop 360x740+912+50 +repage "$p/B-panel-before.png"
# The former bug clears B's working/preview/confirm here and strands its arm.
touch "$p/A-final.release"
wait_file A-final.returned
sleep 0.3
v "$app" status > "$p/after-A-return-status.json"
v "$app" shot "$p/B-working-after-A-return.png" >/dev/null
magick "$p/B-working-after-A-return.png" -crop 360x740+912+50 +repage "$p/B-panel-after.png"
cmp "$p/B-panel-before.png" "$p/B-panel-after.png"
test "$(count result-publish-attempt "$b")" = 0
test "$(associations "$b")" = 0
# B must keep its own captured intent and proceed once after its arm returns.
touch "$p/B-arm.release"
wait_file B-attempt.returned
sleep 0.25
jq -e --arg b "$b" '.state=="recorded_observed" and .manifest_sha256==$b and .association.verification=="structure_hash_linkage_only"' "$p/B-attempt-response.json" >/dev/null
test "$(count result-publish-arm "$a")" = 1
test "$(count result-publish-attempt "$a")" = 1
test "$(count result-publish-arm "$b")" = 1
test "$(count result-publish-attempt "$b")" = 1
test "$(associations "$b")" = 1
v "$app" shot "$p/B-final-observed.png" >/dev/null
v "$app" status > "$p/race-final-status.json"
jq -e '(.errors|length)==0' "$p/race-final-status.json" >/dev/null
echo 'PASS native delayed A final read-back preserves B confirmation/preview/working; B arm proceeds to one attempt; no extra arm/attempt'
