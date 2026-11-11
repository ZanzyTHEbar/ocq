#!/bin/sh
# Opt-in private real-service/native proof. The caller builds the four helpers
# under checked job-scoped build settings, then launches this beneath the proven
# dispatcher cleanup-owner. No shared server, client, agent or live queue.
set -eu
test "$#" = 5; test "$1" = --owned
root=$2; app=$3; hc=$4; guard=$5
test "${PROBE_ROOT-}" = "$root"
test "${OCQ_FIXTURE_OWNER_PID-}" = "$PPID"
repo=$(pwd)
umask 077
ln -s bridge-real "$root/bin/ocq-herdr"
for d in home config data cache run tmp zdot apphome; do mkdir -m 700 "$root/$d"; done
cat > "$root/via" <<EOF
#!/bin/sh
exec env -i HOME='$root/home' XDG_CONFIG_HOME='$root/config' XDG_DATA_HOME='$root/data' XDG_CACHE_HOME='$root/cache' XDG_RUNTIME_DIR='$root/run' TMPDIR='$root/tmp' ZDOTDIR='$root/zdot' PROBE_ROOT='$root' OCQ_STATE_DIR='$root/fixture/queue' HERDR_APP_HOME='$root/apphome' HC_SESSION=handoff DISPLAY=":\$(cat '$root/display')" PATH='$root/bin:/usr/bin:/bin' SHELL=/bin/sh LC_ALL=C.UTF-8 GOMAXPROCS=2 HERDR_TELEMETRY=0 DO_NOT_TRACK=1 NO_AT_BRIDGE=1 DBUS_SESSION_BUS_ADDRESS='unix:path=$root/no-bus' OCQ_HANDOFF_SERVER_URL="\$(cat '$root/origin')" OCQ_PRIVATE_SERVER_PROFILE='$root/profile.json' OCQ_PRIVATE_SERVER_URL="\$(cat '$root/origin')" /bin/sh '$root/auth-via' "\$@"
EOF
chmod 700 "$root/via"
cat > "$root/auth-via" <<EOF
#!/bin/sh
# Read after env isolation, so even the fixture password is never an env(1)
# assignment argument. Authentication is inherited by exec, not passed as argv.
OPENCODE_SERVER_PASSWORD=\$(jq -r .password '$root/profile.json')
export OPENCODE_SERVER_PASSWORD
exec '$guard' "\$@"
EOF
if test "$app" = --backend-pipes; then
 printf '0\n' > "$root/display"
else
env -i HOME="$root/home" XDG_RUNTIME_DIR="$root/run" "$guard" /usr/bin/Xvfb -displayfd 3 -screen 0 1440x1000x24 -nolisten tcp -ac 3> "$root/display" > "$root/xvfb.log" 2>&1 &
sleep 1
test -s "$root/display"
fi
# Password is fixture-only and remains private; never bridge DTO/argv.
printf '{"password":"%s"}\n' "$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')" > "$root/profile.json"
printf 'http://127.0.0.1:1' > "$root/origin"
"$root/via" "$root/bin/opencode-real" serve --hostname 127.0.0.1 --port 0 > "$root/server.stdout" 2> "$root/server.stderr" &
i=0
while ! grep -q 'server listening on http://127.0.0.1:' "$root/server.stdout"; do i=$((i+1)); test "$i" -lt 100; sleep .1; done
sed -n 's/^server listening on \(http:\/\/127.0.0.1:[0-9]*\)$/\1/p' "$root/server.stdout" > "$root/origin"
test -s "$root/origin"
# Isolated owned repository/worktree. No user checkout copied or modified.
mkdir -m 700 "$root/repository"
git -C "$root/repository" init -q
git -C "$root/repository" -c user.name=fixture -c user.email=fixture@invalid commit -q --allow-empty -m fixture
git -C "$root/repository" worktree add -q "$root/worktree" -b exact-empty
"$root/via" "$root/bin/reconcile-proof" "$root/bin/bridge-real" "$root/fixture" "$root/repository" "$root/worktree" --fixture > "$root/fixture.json"
cat > "$root/bin/opencode" <<EOF
#!/bin/sh
set -eu
p='$root'
if test "\$#" = 1 && test "\$1" = --version; then
 if test -f "\$p/pipe-phase"; then
  printf 'version\n' >> "\$p/pipe-cli.trace"
  if test "\$(cat "\$p/pipe-phase")" = version; then exec "\$p/bin/handoff-proof" --pipe-parent version "\$@"; fi
 fi
 exec "\$p/bin/opencode-real" --version
fi
test "\$#" = 5 && test "\$1" = --server && test "\$2" = "\$(cat "\$p/origin")" && test "\$3" = api && test "\$4" = get || { echo DENIED >> "\$p/api.trace"; exit 92; }
printf 'GET %s\n' "\$5" >> "\$p/api.trace"
if test -f "\$p/pipe-phase"; then
 test "\$(cat "\$p/pipe-phase")" = session || exit 92
 printf 'session_get\n' >> "\$p/pipe-cli.trace"
 exec "\$p/bin/handoff-proof" --pipe-parent session "\$@"
fi
if test -f "\$p/verify.hold"; then
 : > "\$p/verify.entered"
 i=0; while ! test -f "\$p/verify.release"; do i=\$((i+1)); test "\$i" -lt 150; sleep .05; done
 "\$p/bin/opencode-real" "\$@"
 : > "\$p/verify.returned"
else exec "\$p/bin/opencode-real" "\$@"; fi
EOF
chmod 700 "$root/bin/opencode"
cat > "$root/mutate" <<EOF
#!/bin/sh
set -eu
p='$root'
job=\$(jq -r .target.job_id "\$p/fixture.json")
session=\$(jq -r .target.recorded_session_id "\$p/fixture.json")
tree=\$(jq -r .target.recorded_worktree "\$p/fixture.json")
case "\$1" in
 wrong-location) tree="\$p/repository";;
 absent) session=ses_ocq_absent_human_handoff_fixture;;
 restore) ;; *) exit 2;;
esac
sqlite3 "\$p/fixture/queue/queue.db" "UPDATE agent_ownership SET worktree='\$tree',session_id='\$session' WHERE job_id='\$job';"
jq --arg tree "\$tree" --arg session "\$session" '.target.recorded_worktree=\$tree|.target.recorded_session_id=\$session' "\$p/fixture.json" > "\$p/changed.json"
EOF
chmod 700 "$root/mutate"
if test "$app" = --backend-pipes; then
 if ! "$root/via" "$root/bin/handoff-proof" "$root/bin/bridge-real" --backend-pipes > "$root/results.txt" 2>&1; then
  : > "$root/scenario-failed"
 fi
 cat "$root/results.txt"
 # No further CLI call after the deadline. The owner alone stops/reaps the
 # real pipe children and private server; this is not a success certificate.
 test "$(sqlite3 "$root/fixture/queue/queue.db" 'SELECT count(*) FROM agent_operations;')" = 0 || : > "$root/scenario-failed"
 exit 0
fi
"$root/via" "$hc" serve > "$root/core.log" 2>&1 &
sleep 1
"$root/via" "$app" > "$root/app.log" 2>&1 &
sleep 2
"$root/via" "$app" status > "$root/startup-status.json"
"$root/via" "$root/bin/handoff-proof" --install "$repo/share/herdr/mods/ocq"
cp share/herdr/layouts/ocq-workspace.json "$root/apphome/layouts/ocq-workspace.json"
"$root/via" "$app" check "$root/apphome/mods/ocq" > "$root/native-check.txt" 2>&1
if ! "$root/via" "$root/bin/handoff-proof" "$root/bin/bridge-real" "$app" > "$root/results.txt" 2>&1; then
 # This proof cannot launch agents/dispatchers. An assertion failure still
 # authorizes closing ONLY this known harmless fixture beneath its live owner.
 : > "$root/scenario-failed"
 cat "$root/results.txt"
 if test -f "$root/native-owned-spaces.json"; then
  for id in $(jq -r '.[].space' "$root/native-owned-spaces.json"); do
   "$root/via" "$app" do space.close --id "$id" --confirmed true
  done
 fi
 "$root/via" "$app" do window.close > "$root/window-closed.json"
 echo 'FAIL scenario; authorizing owned harmless runtime cleanup, not acceptance'
 exit 0
fi
cat "$root/results.txt"
# Session history and queue launch intents remain empty. No prompt ever sent.
session=$(jq -r .target.recorded_session_id "$root/fixture.json")
if ! (
 ! grep -q DENIED "$root/api.trace" &&
 "$root/via" "$root/bin/opencode" --server "$(cat "$root/origin")" api get "/api/session/$session/message?limit=100&order=asc" > "$root/messages-after.json" &&
 # Direct setup HTTP and CLI output differ by a trailing newline. Compare
 # canonical JSON, retaining all data/cursor fields rather than raw whitespace.
 jq -S . "$root/fixture/messages-before.json" > "$root/messages-before.canonical.json" &&
 jq -S . "$root/messages-after.json" > "$root/messages-after.canonical.json" &&
 cmp "$root/messages-before.canonical.json" "$root/messages-after.canonical.json" &&
 test "$(sqlite3 "$root/fixture/queue/queue.db" 'SELECT count(*) FROM agent_operations;')" = 0
); then
 : > "$root/scenario-failed"
 "$root/via" "$app" do window.close > "$root/window-closed.json"
 echo 'FAIL no-execution assertions; authorizing owned harmless runtime cleanup only'
 exit 0
fi
"$root/via" "$app" do window.close > "$root/window-closed.json"
echo 'PASS no prompts, agent execution intents, permission answers or client launches; owner will finalize runtime shutdown'
