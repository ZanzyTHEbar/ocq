#!/bin/sh
# Opt-in native proof. Local modes NEVER configure/start/select a peer.
# Usage: native.sh --local-corrections|--local-audits|--peer APP HC GUARD OLD_BRIDGE
# Caller supplies verified job-scoped TMPDIR/GOCACHE. --peer is separate and
# has bounded same-host stdio validation; local success is NOT peer acceptance.
set -eu
if [ "${1-}" != --owned ]; then
 test "$#" = 5
 mode=$1; shift
 case "$mode" in --local-corrections|--local-audits|--peer) ;; *) exit 64;; esac
 app=$(realpath "$1"); hc=$(realpath "$2"); guard=$(realpath "$3"); old=$(realpath "$4")
 test "$("$app" --version)" = 'herdr-app 0.1.14'
 test "$("$hc" --version)" = 'hc 0.1.19'
 : "${TMPDIR:?Supply verified job-scoped build scratch and Go cache}"
 export GOMAXPROCS=2
 umask 077
 root=$(mktemp -d "${XDG_STATE_HOME:-$HOME/.local/state}/ocq-local-XXXXXXXX")
 chmod 700 "$root"; mkdir -m 700 "$root/bin"
 printf '%s\n' "$mode" > "$root/proof-mode"
 if [ "$mode" = --local-corrections ]; then : > "$root/corrections-only"; fi
 printf 'Native evidence: %s\n' "$root"
 go build -p 1 -o "$root/bin/bridge-real" ./cmd/ocq-herdr
 go build -p 1 -ldflags '-buildid=ocq-compatibility-drift-fixture' -o "$root/bin/bridge-drift" ./cmd/ocq-herdr
 go build -p 1 -o "$root/bin/compatibility-proof" ./proof/herdr-compatibility
 go build -p 1 -o "$root/bin/dispatcher-proof" ./proof/herdr-command-dispatcher
 OCQ_COMPAT_OLD_BRIDGE="$old" PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-owner "$PWD/proof/herdr-compatibility/native.sh" "$app" "$hc" "$guard"
 test ! -e "$root/scenario-failed"
 exit
fi
test "$#" = 5; test "$1" = --owned
root=$2; app=$3; hc=$4; guard=$5
test "${PROBE_ROOT-}" = "$root"
test "${OCQ_FIXTURE_OWNER_PID-}" = "$PPID"
: "${OCQ_COMPAT_OLD_BRIDGE:?Exact retained pre-contract bridge required}"
repo=$(pwd)
mode=$(cat "$root/proof-mode")
case "$mode" in --local-corrections|--local-audits|--peer) ;; *) exit 64;; esac
umask 077
printf '%s\n' "$guard" > "$root/guard-path"
if [ "$mode" = --peer ]; then : > "$root/peer-transfer"; fi
socket="$root/run/herdr-core/c.holder.00000000.sock"
test "${#socket}" -le 107 || { echo 'Fixture root too long for Linux Unix sockets' >&2; exit 1; }
for d in home config data cache run tmp zdot apphome machine-config machine-data machine-state; do mkdir -m 700 "$root/$d"; done
if [ "$mode" = --peer ]; then
 # Documented HC shell argv is an explicit program, not an interactive shell.
 # space.new without argv uses this exact direct foreground command.
 mkdir -m 700 "$root/config/herdr-core" "$root/machine-config/herdr-core"
 printf 'notes = false\nshell = ["%s", "model-anchor", "origin"]\n' "$root/bin/dispatcher-proof" > "$root/config/herdr-core/config.toml"
 printf 'notes = false\nshell = ["%s", "model-anchor", "peer"]\n' "$root/bin/dispatcher-proof" > "$root/machine-config/herdr-core/config.toml"
fi
printf 'matched\n' > "$root/mode"
ln -s compatibility-proof "$root/bin/ocq-herdr"
ln -s compatibility-proof "$root/bin/ocq-compat-gate"
ln -s "$hc" "$root/bin/hc"
# Installed HC's supported via bootstrap looks up this executable name.
ln -s "$hc" "$root/bin/herdr-core"
cat > "$root/via" <<EOF
#!/bin/sh
exec env -i HOME='$root/home' XDG_CONFIG_HOME='$root/config' XDG_DATA_HOME='$root/data' XDG_CACHE_HOME='$root/cache' XDG_RUNTIME_DIR='$root/run' TMPDIR='$root/tmp' ZDOTDIR='$root/zdot' PROBE_ROOT='$root' OCQ_STATE_DIR='$root/queue' HERDR_APP_HOME='$root/apphome' HC_SESSION=c DISPLAY=":\$(cat '$root/display')" PATH='$root/bin:/usr/bin:/bin' SHELL=/bin/sh LC_ALL=C.UTF-8 GOMAXPROCS=2 HERDR_TELEMETRY=0 DO_NOT_TRACK=1 NO_AT_BRIDGE=1 DBUS_SESSION_BUS_ADDRESS='unix:path=$root/no-bus' OCQ_COMPAT_OLD_BRIDGE='$OCQ_COMPAT_OLD_BRIDGE' '$guard' "\$@"
EOF
chmod 700 "$root/via"
cat > "$root/peer-env" <<EOF
#!/bin/sh
exec env -i HOME='$root/home' XDG_CONFIG_HOME='$root/machine-config' XDG_DATA_HOME='$root/machine-data' XDG_STATE_HOME='$root/machine-state' XDG_RUNTIME_DIR='$root/run' TMPDIR='$root/tmp' HC_SESSION=p PROBE_ROOT='$root' OCQ_STATE_DIR='$root/machine-queue' PATH='$root/bin:/usr/bin:/bin' SHELL=/bin/sh LC_ALL=C.UTF-8 GOMAXPROCS=2 HERDR_TELEMETRY=0 DO_NOT_TRACK=1 "\$@"
EOF
chmod 700 "$root/peer-env"
cat > "$root/local-machine" <<EOF
#!/bin/sh
# Private same-host stdio peer only. Honor hc's one trusted generated shell line
# (echo/version/connect preflight); never SSH, install or contact a network peer.
# The supported via interface supplies the shell line as its LAST argument;
# leading transport arguments are not the command. Retain count only, no input.
printf 'argc=%s\n' "\$#" >> '$root/local-machine.trace'
test "\$#" -gt 0
for line do :; done
# The originating HC core already carries the approved inherited filter.
# A second NEW_LISTENER fails EBUSY and strands its guard in recvmsg. Require
# the original guard ancestry; never remove/replace that inherited restriction.
'$root/bin/ocq-compat-gate' inherited-transport
exec '$root/peer-env' /bin/sh -c "\$line"
EOF
chmod 700 "$root/local-machine"
# Parser-only check under the same launch-time owner and approved guard, before
# any core/window starts. This never queries or starts a shared core.
if ! "$root/peer-env" "$guard" "$root/bin/compatibility-proof" diagnostic-check "$root/bin/hc"; then
 : > "$root/scenario-failed"
 exit 0 # no native core/window started; owner still certifies final ECHILD
fi
env -i HOME="$root/home" XDG_RUNTIME_DIR="$root/run" "$guard" /usr/bin/Xvfb -displayfd 3 -screen 0 1440x1000x24 -nolisten tcp -ac 3> "$root/display" > "$root/xvfb.log" 2>&1 &
xvfb_pid=$!
sleep 1
test -s "$root/display"
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$xvfb_pid" "$root/xvfb-owner.json"
"$root/via" "$hc" serve > "$root/core.log" 2>&1 &
core_pid=$!
sleep 1
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$core_pid" "$root/core-owner.json"
# No service discovery/start fallback in the via path: establish the exact
# second private local profile before HC's stdio bootstrap connects to it.
if [ "$mode" = --peer ]; then
 "$root/peer-env" "$guard" "$hc" serve > "$root/peer-core.log" 2>&1 &
 peer_pid=$!
 sleep 1
 PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$peer_pid" "$root/peer-owner.json"
fi
"$root/via" "$app" > "$root/app.log" 2>&1 &
app_pid=$!
sleep 2
PROBE_ROOT="$root" "$root/bin/dispatcher-proof" cleanup-record "$app_pid" "$root/app-owner.json"
"$root/via" "$app" status > "$root/startup-status.json"
"$root/via" "$root/bin/compatibility-proof" install "$repo/share/herdr/mods/ocq"
cp share/herdr/layouts/ocq-workspace.json "$root/apphome/layouts/ocq-workspace.json"
"$root/via" "$app" check "$root/apphome/mods/ocq" > "$root/native-check.txt" 2>&1
if ! "$root/via" "$root/bin/compatibility-proof" run "$app" > "$root/results.txt" 2>&1; then
 : > "$root/scenario-failed"
 cat "$root/results.txt"
 # No dispatcher may be launched by this scenario. Close only exact retained
 # fixture spaces even on assertion failure, then let its live owner reap.
 if test -f "$root/owned-spaces.json"; then
  for id in $(jq -r '.[].space' "$root/owned-spaces.json"); do "$root/via" "$app" do space.close --id "$id" --confirmed true; done
 fi
fi
cat "$root/results.txt"
"$root/via" "$app" do window.close > "$root/window-closed.json"
sleep 1
echo 'Scenario ended; launch-time owner must verify shutdown (scenario-failed is not acceptance)'
