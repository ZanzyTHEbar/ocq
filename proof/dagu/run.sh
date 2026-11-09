#!/usr/bin/env bash
# A bounded Linux acceptance experiment, NOT an ocq scheduler.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/../.." && pwd)
cache="$repo/runtime/dagu-v2.22.4"
mkdir -p "$cache"
chmod 00700 "$repo/runtime" "$cache"
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) archive=dagu_2.22.4_linux_amd64.tar.gz
    digest=f1b2133d8031580649d7cf31140b59f365fa03fd103da047894a471224657ee2 ;;
  *) echo 'This bounded proof supports Linux x86_64 only.' >&2; exit 1 ;;
esac
release=https://github.com/dagucloud/dagu/releases/download/v2.22.4
for file in checksums.txt "$archive"; do
  if [[ ! -f "$cache/$file" ]]; then
    curl --fail --location --silent --show-error "$release/$file" -o "$cache/$file.download"
    mv "$cache/$file.download" "$cache/$file"
  fi
done
# Both the official manifest and the observed release digest must agree.
(cd "$cache"; grep " $archive\$" checksums.txt | sha256sum --check -)
printf '%s  %s\n' "$digest" "$cache/$archive" | sha256sum --check -
tar -xzf "$cache/$archive" -C "$cache" dagu
chmod 700 "$cache/dagu"
bin="$cache/dagu"
run=$(mktemp -d "$repo/runtime/proof.XXXXXXXX")
printf 'Artifacts: %s\n' "$run"
mkdir "$run/home" "$run/dags"
chmod 00700 "$run" "$run/home" "$run/dags"
cp "$repo/proof/dagu/config.yaml" "$run/config.yaml"
cp "$repo/proof/dagu/job.yaml" "$run/dags/job.yaml"
touch "$run/events" "$run/identities" "$run/cleanup"

# Identity = PID plus Linux /proc start ticks, not a reusable PID alone.
identity() {
  local stat
  [[ -r /proc/$1/stat ]] || return 1
  stat=$(cat "/proc/$1/stat") || return 1
  stat=${stat##*) }
  read -ra fields <<< "$stat"
  printf '%s %s\n' "$1" "${fields[19]}"
}
owned() {
  [[ -r /proc/$1/environ ]] &&
    grep -zFxq "OCQ_PROOF_RUN=$run" "/proc/$1/environ" 2>/dev/null
}
snapshot() {
  local entry pid id
  for entry in /proc/[0-9]*/environ; do
    pid=${entry#/proc/}; pid=${pid%/environ}
    if owned "$pid"; then
      id=$(identity "$pid") || continue
      printf '%s\n' "$id" >> "$run/identities"
      printf '%s\n' "$id"
    fi
  done
}
signal_owned() {
  local signal=$1 pid=$2 start=$3
  if [[ $(identity "$pid" 2>/dev/null || :) == "$pid $start" ]] && owned "$pid"; then
    printf '%s %s %s\n' "$signal" "$pid" "$start" >> "$run/cleanup"
    kill -s "$signal" "$pid"
  fi
}
cleanup() {
  local result=$? pid start
  trap - EXIT INT TERM
  set +e
  # Discover only processes carrying this unique private-run marker. No pkill.
  snapshot > "$run/remaining"
  while read -r pid start; do
    signal_owned CONT "$pid" "$start"
    signal_owned TERM "$pid" "$start"
  done < "$run/remaining"
  for ((i=0; i<50; i++)); do
    snapshot > "$run/remaining"
    [[ ! -s "$run/remaining" ]] && break
    sleep .1
  done
  while read -r pid start; do signal_owned KILL "$pid" "$start"; done < "$run/remaining"
  [[ -n ${scheduler:-} ]] && wait "$scheduler" 2>/dev/null
  for ((i=0; i<50; i++)); do
    snapshot > "$run/remaining"
    [[ ! -s "$run/remaining" ]] && break
    sleep .1
  done
  if [[ -s "$run/remaining" ]]; then
    echo 'FAIL: owned processes remain; inspect private remaining file.' >&2
    result=1
  else
    echo 'verified: no marked scheduler or children remain' >> "$run/cleanup"
  fi
  sort -u "$run/identities" -o "$run/identities"
  # Dagu can explicitly chmod some files; keep this experiment private.
  find "$run" -type d -exec chmod 00700 {} +
  find "$run" -type f -exec chmod 600 {} +
  printf 'Cleanup evidence: %s/cleanup\n' "$run"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
dagu() {
  env -i PATH="$PATH" HOME="$run/home" LANG=C OCQ_PROOF_RUN="$run" \
    DAGU_HOME="$run/home" "$bin" --context local "$@"
}
dagu version > "$run/version"
[[ $(cat "$run/version") == 2.22.4 ]]
for command in scheduler enqueue status history; do
  dagu "$command" --help > "$run/help-$command"
done
start_scheduler() {
  local phase=$1
  # Record the wrapper's identity BEFORE exec starts the scheduler (same PID).
  (
    identity "$BASHPID" > "$run/scheduler-$phase.identity"
    exec env -i PATH="$PATH" HOME="$run/home" LANG=C OCQ_PROOF_RUN="$run" \
      DAGU_HOME="$run/home" "$bin" --context local scheduler \
      --config "$run/config.yaml" --dags "$run/dags"
  ) > "$run/scheduler-$phase.log" 2>&1 &
  scheduler=$!
  for ((i=0; i<600; i++)); do
    kill -0 "$scheduler" || { echo 'Scheduler exited early' >&2; return 1; }
    if grep -q 'Scheduler started' "$run/scheduler-$phase.log"; then
      grep -q 'Health check server disabled' "$run/scheduler-$phase.log"
      cat "$run/scheduler-$phase.identity" >> "$run/identities"
      return
    fi
    sleep .1
  done
  echo 'Scheduler readiness timeout' >&2; return 1
}
stop_scheduler() {
  local phase=$1 pid start
  read -r pid start < "$run/scheduler-$phase.identity"
  signal_owned TERM "$pid" "$start"
  for ((i=0; i<100; i++)); do
    [[ ! -e /proc/$pid ]] && break
    sleep .1
  done
  [[ ! -e /proc/$pid ]]
  wait "$scheduler"
  printf 'verified graceful exit %s %s\n' "$pid" "$start" >> "$run/cleanup"
}
start_scheduler before
read -r pid start < "$run/scheduler-before.identity"
signal_owned STOP "$pid" "$start"
# Ensure the scheduler is actually stopped before accepting queued work.
grep -q '^State:.*T' "/proc/$pid/status"
for number in 1 2 3; do
  # Each real CLI submitter exits independently while the scheduler is stopped.
  dagu enqueue --config "$run/config.yaml" --run-id "proof-$number" \
    "$run/dags/job.yaml" -- "NUMBER=$number" > "$run/submit-$number.log" 2>&1
  dagu status --config "$run/config.yaml" --run-id "proof-$number" proof-job \
    > "$run/queued-$number"
  grep -qi queued "$run/queued-$number"
done
[[ ! -s "$run/events" ]]
signal_owned KILL "$pid" "$start"
wait "$scheduler" 2>/dev/null || :
[[ ! -e /proc/$pid ]]
printf 'verified crash exit %s %s\n' "$pid" "$start" >> "$run/cleanup"
start_scheduler after
for ((i=0; i<150; i++)); do
  snapshot > "$run/observed"
  [[ $(wc -l < "$run/events") -eq 6 ]] && break
  sleep .2
done
printf 'start 1\nend 1\nstart 2\nend 2\nstart 3\nend 3\n' > "$run/expected-events"
diff -u "$run/expected-events" "$run/events"
for number in 1 2 3; do
  for ((i=0; i<50; i++)); do
    dagu status --config "$run/config.yaml" --run-id "proof-$number" proof-job \
      > "$run/outcome-$number"
    grep -qi succeeded "$run/outcome-$number" && break
    sleep .1
  done
  grep -qi succeeded "$run/outcome-$number"
  # Verify real retained step output, not just our side-channel event file.
  grep -rlFx "hello from job $number" "$run/home/logs" > "$run/output-$number.paths"
done
stop_scheduler after
start_scheduler retained
# After another service restart, the same run ID rejects BOTH identical and
# changed requests. This is duplicate protection, not idempotent acknowledgment.
for value in 1 99; do
  if dagu enqueue --config "$run/config.yaml" --run-id proof-1 \
    "$run/dags/job.yaml" -- "NUMBER=$value" > "$run/replay-$value.log" 2>&1; then
    echo 'Unexpected duplicate acceptance' >&2; exit 1
  fi
  grep -q 'already exists' "$run/replay-$value.log"
done
for number in 1 2 3; do
  dagu status --config "$run/config.yaml" --run-id "proof-$number" proof-job \
    > "$run/retained-$number"
  grep -qi succeeded "$run/retained-$number"
  grep -rlFx "hello from job $number" "$run/home/logs" > "$run/output-$number.paths"
done
diff -u "$run/expected-events" "$run/events"
dagu history --config "$run/config.yaml" --format json proof-job > "$run/history.json"
stop_scheduler retained
printf 'PASS: three queued submitters, scheduler crash/restart, serial order, outcomes, output\n' | tee "$run/result"
