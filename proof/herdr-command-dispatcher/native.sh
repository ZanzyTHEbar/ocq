#!/bin/sh
# Opt-in real native races. Requires an explicit Herdr 0.1.14 app, HC 0.1.19,
# and the approved local network guard. Never points at a running/shared app.
# Usage: sh proof/herdr-command-dispatcher/native.sh APP HC NETWORK_GUARD
set -eu
if [ "${1-}" = --owned ]; then
test "$#" = 5
root=$2; app=$3; hc=$4; guard=$5
test "${PROBE_ROOT-}" = "$root"
test "${OCQ_FIXTURE_OWNER_PID-}" = "$PPID"
repo=$(pwd)
umask 077
else
default_xdg=false
ancestor=false
if [ "${1-}" = --default-xdg-ancestor ]; then default_xdg=true; ancestor=true; shift; fi
if [ "${1-}" = --default-xdg ]; then default_xdg=true; shift; fi
test "$#" = 3
app=$(realpath "$1"); hc=$(realpath "$2"); guard=$(realpath "$3")
test "$("$app" --version)" = 'herdr-app 0.1.14'
test "$("$hc" --version)" = 'hc 0.1.19'
repo=$(pwd)
root=$(mktemp -d "${XDG_STATE_HOME:-$HOME/.local/state}/ocq-dispatch-races-XXXXXXXX")
chmod 700 "$root"
echo "Native evidence: $root"
umask 077
for d in bin home config data cache run tmp zdot apphome; do mkdir -m 700 "$root/$d"; done
if "$default_xdg"; then
 : > "$root/default-state-xdg"
 if "$ancestor"; then
  : > "$root/default-state-ancestor"
  mkdir -m 700 "$root/default-state-real"
  ln -s default-state-real "$root/default-state"
 else mkdir -m 700 "$root/default-state"; fi
fi
export GOMAXPROCS=2
: "${TMPDIR:?Supply job-scoped build scratch (and Go caches) before running this proof}"
go build -p 1 -o "$root/bin/bridge-real" ./cmd/ocq-herdr
go build -p 1 -o "$root/bin/dispatcher-proof" ./proof/herdr-command-dispatcher
ln -s "$hc" "$root/bin/hc"
ln -s dispatcher-proof "$root/bin/ocq-herdr"
ln -s dispatcher-proof "$root/bin/ocq-dispatch-race-gate"
# Establish kernel ownership BEFORE launching any native runtime. The internal
# script may request shutdown, but cannot itself claim all descendants stopped.
exec env PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-owner "$repo/proof/herdr-command-dispatcher/native.sh" "$app" "$hc" "$guard"
fi
state_env="OCQ_STATE_DIR='$root/queue'"
if test -f "$root/default-state-xdg"; then state_env="XDG_STATE_HOME='$root/default-state'"; fi
cat > "$root/via" <<EOF
#!/bin/sh
exec env -i HOME='$root/home' XDG_CONFIG_HOME='$root/config' XDG_DATA_HOME='$root/data' XDG_CACHE_HOME='$root/cache' XDG_RUNTIME_DIR='$root/run' TMPDIR='$root/tmp' ZDOTDIR='$root/zdot' PROBE_ROOT='$root' $state_env OCQ_RACE_BRIDGE='$root/bin/bridge-real' HERDR_APP_HOME='$root/apphome' HC_SESSION=race DISPLAY=":\$(cat '$root/display')" PATH='$root/bin:/usr/bin:/bin' SHELL=/bin/sh LC_ALL=C.UTF-8 GOMAXPROCS=2 HERDR_TELEMETRY=0 DO_NOT_TRACK=1 NO_AT_BRIDGE=1 DBUS_SESSION_BUS_ADDRESS='unix:path=$root/no-bus' '$guard' "\$@"
EOF
chmod 700 "$root/via"
# Bounded startup only, not retrying any mutation.
env -i HOME="$root/home" XDG_RUNTIME_DIR="$root/run" "$guard" /usr/bin/Xvfb -displayfd 3 -screen 0 1440x1000x24 -nolisten tcp -ac 3> "$root/display" > "$root/xvfb.log" 2>&1 &
xvfb_pid=$!
sleep 1
test -s "$root/display"
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$xvfb_pid" "$root/xvfb-owner.json"
"$root/via" "$root/bin/hc" serve > "$root/core.log" 2>&1 &
core_pid=$!
sleep 1
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$core_pid" "$root/core-owner.json"
"$root/via" "$app" > "$root/app.log" 2>&1 &
app_pid=$!
sleep 2
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$app_pid" "$root/app-owner.json"
"$root/via" "$app" status > "$root/startup-status.json"
# First native startup provisions stock bundled mods/layouts into this new home.
"$root/via" "$root/bin/dispatcher-proof" native-install "$repo/share/herdr/mods/ocq"
cp share/herdr/layouts/ocq-workspace.json "$root/apphome/layouts/ocq-workspace.json"
"$root/via" "$app" check "$root/apphome/mods/ocq" > "$root/native-check.txt" 2>&1
"$root/via" "$root/bin/dispatcher-proof" native-run "$app" > "$root/native-results.txt" 2>&1 || {
  echo "Native proof failed. Retained exact private resources/evidence at $root; do not force-close a live dispatcher."
  cat "$root/native-results.txt"
  exit 1
}
cat "$root/native-results.txt"
# Retain a pre-shutdown observation, NOT a spawn-quiescence/success certificate.
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-inventory "$root/cleanup-identities.json" "$root/xvfb-owner.json" "$root/core-owner.json" "$root/app-owner.json"
"$root/via" "$app" do window.close > "$root/window-closed.json"
sleep 1
# Success authorizes the owner to begin bounded shutdown. Do not wait for direct
# children here: ownership must survive this shell and parents that exit/fork.
# Only cleanup-owner's final empty kernel inventory can report cleanup success.
echo "Native scenario complete; launch-time owner will verify shutdown: $root"
