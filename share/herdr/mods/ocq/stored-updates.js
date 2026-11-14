import { spaces } from "herdr";
import { Buffer } from "buffer";
import { createHash, randomBytes } from "crypto";
import { exec, compatibility } from "./compatibility.js";

const protocol = "ocq-observe-v1";
const lifecycleProtocol = "ocq-observation-lifecycle-v1";
const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
export const decimal = value => typeof value === "string" && /^(0|[1-9][0-9]*)$/.test(value) && (value.length < 19 || (value.length === 19 && value <= "9223372036854775807"));
export function compareRevision(a, b) { return a.length === b.length ? (a === b ? 0 : a < b ? -1 : 1) : a.length < b.length ? -1 : 1; }
export function acceptObservationVersion(previous, next) {
  if (!next || !decimal(next.revision) || !next.binding || !next.selector) return false;
  if (!previous) return true;
  if (previous.binding !== next.binding || previous.selector !== next.selector) return false;
  const order = compareRevision(next.revision, previous.revision);
  return order > 0 || (order === 0 && next.serial > previous.serial);
}

// One outstanding native observation PROMISE in this JS runtime, across logical
// view replacement. Rejection settles that promise, NOT the remote helper's
// physical lifetime. Pure read-only retries may overlap remote bounded helpers;
// they never retry mutations or resolve queue execution/resource ownership.
const cellKey = "__ocq_observation_physical_v1";
const physical = globalThis[cellKey] || (globalThis[cellKey] = { flight: null, desired: null });

function currentScope(scope) {
  const active = spaces().find(space => space.active);
  return active && active.id === scope.space && active.machine === scope.machine && active.folder === scope.folder;
}
function pins(value) { return { queue_identity: value.queue_identity, store_context: value.store_context, bridge_context: value.bridge_context }; }
function bindingKey(value) { return JSON.stringify(pins(value)); }

export class StoredUpdates {
  constructor(owner, cx) {
    this.owner = owner; this.entries = new Map(); this.serial = 0;
    // init's cx is ambient, not owner-bound. Capture a supported task context
    // so a retired controller can wake this owner without borrowing its scope.
    cx.spawn(task => { this.cx = task; }, { owner });
    this.health = { state: "starting", reason: null, readAt: null };
  }

  sync(cx) {
    if (this.retired) return;
    const owner = this.owner;
    const next = { space: owner.spaceID, machine: owner.machine, folder: owner.folder };
    // Herdr may qualify a peer's native ID with the exact machine prefix. The
    // bridge consumes that core's local ID, never another discovered/latest ID.
    next.localSpace = next.machine !== null && next.space?.startsWith(`${next.machine}/`) ? next.space.slice(next.machine.length + 1) : next.space;
    if (!next.space || !next.folder) {
      if (this.scope) this.owner.resetObservationVersions();
      this.disposed = true; this.scope = null; this.retryToken = {}; this.retryPending = false;
      if (physical.desired === this) physical.desired = null;
      return;
    }
    if (!/^sp[0-9a-f]{32}$/.test(next.localSpace)) { if (!this.stopped || this.scope?.space !== next.space) this.owner.resetObservationVersions(); this.scope = Object.freeze(next); this.stopped = true; this.publish("upgrade_required", "Selected native space ID is unsupported; no fallback location is used."); return; }
    if (!this.scope || JSON.stringify(this.scope) !== JSON.stringify(next)) {
      if (this.scope) this.owner.resetObservationVersions();
      this.scope = Object.freeze(next); this.binding = null; this.revision = null;
      this.entries.clear(); this.stopped = false; this.disposed = false;
      this.recovery = null;
      this.bindingEpoch = (this.bindingEpoch || 0) + 1;
      this.missing = false; this.retryToken = {}; this.retryPending = false; this.failures = 0;
      this.health = { state: "starting", reason: null, readAt: null };
    }
    if (physical.desired !== this) {
      if (physical.desired) { physical.desired.disposed = true; physical.desired.retired = true; physical.desired.retryToken = {}; }
      physical.desired = this; this.disposed = false;
    }
    this.kick();
  }

  dispose() {
    this.disposed = true; this.retired = true;
    this.retryToken = {}; this.retryPending = false;
    if (physical.desired === this) physical.desired = null;
    // Never discard an unresolved native promise or claim physical cancellation.
    return physical.flight?.controller === this ? physical.flight.settled : null;
  }

  current() { return !this.disposed && physical.desired === this && this.scope && currentScope(this.scope) && this.owner.panelCurrent(); }
  readAgain(cx) {
    if (this.recovery) return; // Explicit recovery owns its drain/check/snapshot.
    this.stopped = false;
    for (const entry of this.entries.values()) entry.dirty = true;
    this.kick();
  }
  recoveryCurrent(recovery) {
    return this.recovery === recovery && this.current() && this.scope === recovery.scope &&
      this.owner.generation === recovery.generation && compatibility.recovery === recovery.token;
  }
  suspendRecovery() {
    if (this.recovery) {
      this.recovery.running = false; this.stopped = true;
    }
  }
  recoveryAuthorized(recovery) {
    return recovery.running && this.recoveryCurrent(recovery) && compatibility.epoch === recovery.epoch &&
      compatibility.serial === recovery.serial && compatibility.status === recovery.descriptor;
  }
  failRecovery(reason) {
    if (!this.recovery) return;
    this.recovery.running = false; this.stopped = true;
    this.owner.observationReset = `Recovery stopped: ${reason}`;
    this.publish("unavailable", this.owner.observationReset);
  }
  recover(cx) {
    if (this.recovery?.running) return;
    if (!this.current()) {
      this.owner.observationReset = "Select an available workspace with an explicit folder, then recover that selected context.";
      cx.notify(); return;
    }
    const previous = physical.flight;
    this.stopped = true; this.retryToken = {}; this.retryPending = false;
    this.owner.resetObservationVersions();
    // Retire the old read's projection ownership without dropping its promise.
    this.scope = Object.freeze({ ...this.scope });
    this.binding = null; this.revision = null; this.entries.clear(); this.visibleKeys = null;
    this.bindingEpoch = (this.bindingEpoch || 0) + 1;
    this.needsSnapshot = true; this.missing = false; this.health.readAt = null;
    const recovery = this.recovery = { running: true, scope: this.scope, generation: this.owner.generation,
      token: compatibility.beginRecovery() };
    this.owner.observationReset = "Recovering selected context. Old confirmations are invalid; attempted operations remain recovery-only.";
    this.publish(previous ? "draining" : "reading", this.owner.observationReset);
    cx.spawn(async cx => {
      try {
        if (previous) {
          if (!previous.settled) throw Error("The previous observer has no settlement handle. Its outcome remains unknown; do not start an overlapping observer.");
          // One finite drain deadline, not a projection polling loop.
          const settled = await Promise.race([previous.settled.then(() => true), cx.sleep(26000).then(() => false)]);
          if (!this.recoveryCurrent(recovery)) return;
          if (!settled) {
            if (!previous.proof) throw Error("The previous native read has no exact lifecycle receipt identity; its outcome remains unknown.");
            // ONE explicit status query, never a polling observer. Retain the
            // ORIGINAL route/pins, not the newly selected context's process list.
            const old = previous.scope;
            const response = await exec("ocq-herdr", ["--mod-space", old.localSpace, "--mod-folder", old.folder,
              "observation-read-status", JSON.stringify(previous.proof)], { on: old.machine, timeout: 10000 });
            if (!this.recoveryCurrent(recovery)) return;
            if (response.code !== 0 || Buffer.from(response.stdout || "").length > 16384)
              throw Error("Original observation process exit was not proved; no observer was replaced.");
            const proof = JSON.parse(response.stdout);
            if (proof.protocol !== lifecycleProtocol || proof.status !== "original_process_exited" ||
              JSON.stringify(proof.key) !== JSON.stringify(previous.proof))
              throw Error("Original observation lifecycle evidence mismatched; no observer was replaced.");
            if (physical.flight && physical.flight !== previous) throw Error("Observation flight changed during recovery.");
            // Physical exit, not callback cancellation, is now proved. Late
            // reactions may settle but can never project or touch a new flight.
            previous.orphanRetired = true;
            if (physical.flight === previous) physical.flight = null;
            previous.resolve();
          }
        }
        if (!this.recoveryCurrent(recovery)) return;
        const checking = compatibility.check(() => this.recoveryCurrent(recovery), true, false, recovery.token);
        recovery.epoch = compatibility.epoch; recovery.serial = compatibility.serial;
        recovery.descriptor = await checking;
        if (!this.recoveryAuthorized(recovery)) throw Error("Compatibility changed during recovery; recover the selected context again.");
        if (recovery.descriptor.observation_schema !== 6 || !hex(recovery.descriptor.queue_identity))
          throw Error("No supported existing schema-6 queue in this selected context; nothing was initialized or migrated.");
        // First read MUST use this fresh descriptor, never the old cursor.
        this.binding = Object.freeze({ queue_identity: recovery.descriptor.queue_identity,
          store_context: recovery.descriptor.observation_store_context, bridge_context: recovery.descriptor.observation_context });
        this.stopped = false; this.kick();
      } catch (error) {
        if (this.recovery === recovery) this.failRecovery(String(error));
      }
    });
  }
  publish(state, reason = null) {
    if (this.health.state === state && this.health.reason === reason && this.owner.updateHealth === this.health) return;
    this.health = { ...this.health, state, reason };
    this.owner.updateHealth = this.health;
    if (this.cx?.phase() !== "render") this.cx?.notify();
  }
  invalidate(reason, clearProjections = false) {
    if (this.recovery) this.failRecovery(reason);
    this.owner.workOperator.suspend(reason);
    if (currentScope(this.scope)) {
      compatibility.invalidateObservation(reason);
      if (this.owner.queue.detail.data?.job || this.owner.queue.detail.data?.event) this.owner.queue.blockDetailActions(undefined, null, "Observation connection/context changed; action scope suspended.");
    }
    this.owner.observationReset = reason;
    if (clearProjections) {
      this.owner.resetObservationVersions();
      this.health.readAt = null;
      this.revision = null;
      this.entries.clear();
      this.bindingEpoch = (this.bindingEpoch || 0) + 1;
    }
    for (const entry of this.entries.values()) entry.dirty = true;
  }

  retry(reason, failureKind = "read_unavailable") {
    if (this.recovery) { this.failRecovery(reason); return; }
    this.health.lastFailureKind = failureKind;
    this.health.lastFailureReason = reason;
    this.invalidate("Observation transport/read failed; old local action authorization stays suspended.");
    for (const entry of this.entries.values()) entry.dirty = true;
    this.needsSnapshot = true;
    const delay = [2000, 5000, 10000, 30000][Math.min(this.failures++, 3)] * (0.8 + Math.random() * 0.4);
    const token = this.retryToken = {}; this.retryPending = true;
    this.publish("reconnecting", `Stored observations may be stale. Read-only recovery in about ${Math.round(delay / 1000)} seconds. ${reason}`);
    this.cx.spawn(async cx => {
      await cx.sleep(Math.round(delay));
      if (this.retryToken !== token) return;
      this.retryPending = false;
      if (this.current()) this.kick();
    });
  }

  plan() { return this.owner.observationSelectors(); }
  entry(descriptor) {
    const key = JSON.stringify(descriptor.selector);
    let entry = this.entries.get(key);
    if (!entry) { entry = { key, dirty: true, target: null, revision: null }; this.entries.set(key, entry); }
    return entry;
  }
  log() { return this.owner.queue.observationLog(this.owner); }
  work() { return this.owner.workOperator.observation(); }

  kick() {
    if (this.recovery?.running && !this.recoveryCurrent(this.recovery))
      this.failRecovery("Selection or panel ownership changed. Recover the currently selected context again.");
    if (!this.current() || this.stopped || this.retryPending) return;
    if (this.recovery && !this.recoveryAuthorized(this.recovery)) return;
    const plan = this.plan();
    const visibleKeys = new Set(plan.map(descriptor => JSON.stringify(descriptor.selector)));
    // Owner-held inactive projections retain their own read timestamps. Drop
    // controller references to their old target objects; revisiting is dirty and
    // always gets a fresh selector read, without retaining abandoned page data.
    for (const key of this.entries.keys()) if (!visibleKeys.has(key)) this.entries.delete(key);
    for (const descriptor of plan) if (!this.visibleKeys?.has(JSON.stringify(descriptor.selector))) this.entry(descriptor).dirty = true;
    this.visibleKeys = visibleKeys;
    const needed = plan.filter(descriptor => {
      const entry = this.entry(descriptor);
      return entry.dirty || entry.target !== descriptor.target || entry.revision !== this.revision || descriptor.dirty;
    }).sort((a, b) => (this.entry(a).attempt || 0) - (this.entry(b).attempt || 0));
    const log = this.log();
    const work = this.binding ? this.work() : null;
    if (physical.flight) {
      if (physical.flight.controller !== this || physical.flight.scope !== this.scope) this.publish("draining", "Waiting for the previous selected-location native promise to settle; physical cancellation is not claimed.");
      else if (!this.missing && physical.flight.command === "watch-wait" && (needed.length || log?.needsRead)) this.publish("reading", "Selected view has pending selectors; showing its last accepted timestamp until the bounded wait settles.");
      return;
    }
    if (!this.missing && (!this.binding || needed.length || log?.needsRead || work?.needsRead || this.needsSnapshot)) {
      const selected = needed.slice(0, 8);
      if (this.needsSnapshot && !selected.length && plan[0]) selected.push(plan[0]);
      // Discovery always has a bounded database selector. Retained file cursors
      // are never sent without all binding pins.
      if (!selected.length && !log) selected.push(plan[0]);
      if ((!selected.length || !selected[0]) && !log) return;
      const request = { protocol, ...(this.binding || {}), selectors: selected.map(item => item.selector) };
      if (this.binding && log?.needsRead) request.log = log.cursor;
      if (work?.needsRead) request.work = work.cursor;
      this.launch("view-snapshot", request, { selected, log: request.log ? log : null, work: request.work ? work : null });
    } else {
      const request = { protocol, ...this.binding, revision: this.revision || "0", wait_seconds: 20 };
      if (log?.watch) request.log = log.cursor;
      if (work && hex(work.cursor.fingerprint)) request.work = work.cursor;
      this.launch("watch-wait", request, { selected: [], log: log?.watch ? log : null, work: request.work ? work : null });
    }
  }

  launch(command, request, capture) {
    const raw = JSON.stringify(request);
    if (Buffer.from(raw).length > 16384) { this.stopped = true; this.publish("limit_exceeded", "Selected observation request exceeds 16 KiB; reduce the selected scope."); return; }
    const scope = this.scope, flight = { controller: this, scope, command, capture, serial: ++this.serial };
    const descriptor = compatibility.status;
    if (this.binding && hex(this.binding.queue_identity) && descriptor &&
      descriptor.queue_identity === this.binding.queue_identity && descriptor.observation_context === this.binding.bridge_context &&
      descriptor.observation_store_context === this.binding.store_context && hex(descriptor.image_sha256)) {
      flight.proof = { protocol: lifecycleProtocol, read_id: randomBytes(32).toString("hex"), command,
        request_sha256: createHash("sha256").update(`${command}\n${raw}`).digest("hex"),
        queue_identity: this.binding.queue_identity, store_context: this.binding.store_context,
        bridge_context: this.binding.bridge_context, image_sha256: descriptor.image_sha256,
        space: scope.localSpace, folder: scope.folder };
    }
    flight.recovery = this.recovery;
    flight.settled = new Promise(resolve => { flight.resolve = resolve; });
    physical.flight = flight;
    let entered = false;
    const run = async cx => {
      // A synchronous scheduling failure may have retired this unstarted slot.
      if (physical.flight !== flight) return;
      entered = true;
      let nativeResolved = false;
      try {
        const args = ["--mod-space", scope.localSpace, "--mod-folder", scope.folder, command, raw];
        if (flight.proof) args.push("--observation-read-id", flight.proof.read_id);
        const response = await exec("ocq-herdr", args, { on: scope.machine, timeout: command === "watch-wait" ? 25000 : 10000 });
        nativeResolved = true;
        // Never race/discard this promise. Its settlement serializes the client,
        // not physical remote processes or delivery of delayed network frames.
        if (!Number.isInteger(response?.code)) throw new Error("Native foreground exit was not established");
        if (flight.orphanRetired || !this.current() || this.scope !== scope) return;
        if (flight.recovery && !this.recoveryAuthorized(flight.recovery)) {
          this.failRecovery("Selected context or authorization changed before the fresh snapshot returned."); return;
        }
        if (response.code !== 0) {
          const diagnostic = response.stderr || "Observation bridge read failed";
          if (response.code === 127 || /unknown command|unknown field|permission denied|unauthorized|authentication|not executable|invalid compatibility|invalid observation|unsupported|protocol|requires.*selector|canonical.*decimal/i.test(diagnostic)) {
            this.stopped = true; this.invalidate("Observation endpoint/configuration unsupported.", true); this.publish("upgrade_required", diagnostic);
          } else this.retry(diagnostic, "read_exit");
          return;
        }
        if (Buffer.from(response.stdout || "").length > 1048576) throw new Error("Observation response exceeds 1 MiB");
        const result = JSON.parse(response.stdout);
        if (result.protocol !== protocol) { this.stopped = true; this.invalidate("Unknown observation protocol.", true); this.publish("upgrade_required", "Install a matched ocq-observe-v1 bridge; nothing is migrated automatically."); return; }
        this.receive(result, flight, cx);
      } catch (error) {
        if (!flight.orphanRetired && this.current() && this.scope === scope) {
          if (nativeResolved) { this.stopped = true; this.invalidate("Malformed/unsupported observation response; old action authorization stays suspended.", true); this.publish("upgrade_required", String(error)); }
          else this.retry(String(error), "native_promise_rejected");
        }
      } finally {
        if (physical.flight === flight) physical.flight = null;
        flight.resolve();
        if (flight.orphanRetired) return;
        const next = physical.desired;
        if (next === this) this.kick();
        // Starting the successor here would register its native call under the
        // OLD view's closing task scope. Wake its own render entry instead.
        else if (next) next.cx.notify();
      }
    };
    try {
      for (const descriptor of capture.selected) this.entry(descriptor).attempt = flight.serial;
      this.publish(command === "watch-wait" ? this.missing ? "missing" : "waiting" : "reading", command === "watch-wait" ? this.missing ? "No existing queue. Automatically waiting for creation on the selected machine; nothing is initialized." : "Automatic change waiting · bounded foreground read, not push or agent liveness." : null);
      this.cx.spawn(run);
    } catch (error) {
      // Never release a started callback: its native promise may be unresolved.
      if (!entered && physical.flight === flight) { physical.flight = null; flight.resolve(); }
      throw error;
    }
  }

  receive(result, flight, cx) {
    if (result.status === "limit_exceeded") { this.stopped = true; this.failRecovery("Snapshot exceeds the bounded response limit; narrow the selected view."); this.publish("limit_exceeded", "Snapshot exceeds the bounded response limit; no partial result was applied."); return; }
    if (result.status === "reset" && result.reset_scope !== "log") {
      this.invalidate("Queue, store, schema or bridge observation context changed. Observations will rediscover read-only; local action context stays suspended.", true);
      this.binding = null; this.revision = null;
      this.missing = false;
      this.retry("Rediscovering the selected machine read-only after a context reset."); return;
    }
    if (!hex(result.bridge_context) || !hex(result.store_context) || (result.queue_identity !== "" && !hex(result.queue_identity))) throw new Error("Missing observation binding pins");
    if (this.binding && bindingKey(result) !== bindingKey(this.binding)) {
      this.invalidate("Observation binding changed without an accepted reset.", true); this.binding = null; this.revision = null; this.missing = false; this.retry("Rediscovering exact observation pins after a binding mismatch."); return;
    }
    const descriptor = compatibility.status;
    if (descriptor && (descriptor.observation_context !== result.bridge_context || descriptor.observation_store_context !== result.store_context || descriptor.queue_identity !== result.queue_identity || descriptor.observation_schema !== result.schema)) this.invalidate("Observation pins differ from the matched mutation descriptor; local actions remain suspended.");
    this.binding = Object.freeze(pins(result));
    if (result.status === "upgrade_required" || (result.schema !== 6 && result.schema !== 0)) {
      this.stopped = true; this.invalidate("Observation schema upgrade is required; no automatic migration.", true); this.publish("upgrade_required", "Automatic observation requires schema 6 and a matched bridge. Stop dispatch and perform the documented explicit upgrade; this view never migrates."); return;
    }
    const missing = result.queue_identity === "" && result.schema === 0;
    this.missing = missing;
    if (result.status === "unavailable") {
      this.invalidate("Selected queue unavailable; local actions remain suspended.", missing);
      if (!missing) { this.retry("Stored observation temporarily unavailable."); return; }
      this.revision = missing ? "0" : this.revision;
      this.publish(missing ? "missing" : "unavailable", missing ? "No existing queue. Waiting for its creation on the selected machine; nothing is initialized." : "Stored observation unavailable. Last accepted data may be stale; use diagnostics."); return;
    }
    if (!decimal(result.revision) || result.schema !== 6) throw new Error("Invalid observation revision/schema");
    if (this.revision && compareRevision(result.revision, this.revision) < 0) {
      this.invalidate("Observation revision decreased; old projection cannot be adopted.", true); this.stopped = true; this.publish("unavailable", "Stored revision decreased; investigate possible restoration before reopening."); return;
    }
    const changed = this.revision !== result.revision;
    this.revision = result.revision;
    if (changed) for (const entry of this.entries.values()) entry.dirty = true;
    if (flight.command === "watch-wait") {
      if (!["changed", "unchanged", "reset"].includes(result.status)) throw new Error("Unsupported wait status");
      if (result.log_changed || result.reset_scope === "log") this.owner.queue.observationLogHint(flight.capture.log, result.log, result.reset_scope === "log", cx);
      if (result.work_changed) this.owner.workOperator.hint(flight.capture.work);
      // No projection fetch for unchanged renewals; kick only observes revision,
      // selector ownership and selected-stream metadata.
      this.publish("waiting", result.watch === "degraded" ? "Automatic change waiting · degraded filesystem hints; bounded expiry checks remain active." : "Automatic change waiting · bounded foreground read, not push."); return;
    }
    if (result.status !== "ready" || result.consistency !== "single_sqlite_snapshot" || !Array.isArray(result.slots) || result.slots.length !== flight.capture.selected.length) throw new Error("Incomplete coherent snapshot response");
    if (flight.recovery && result.slots.some(slot => slot.status === "limit_exceeded")) throw Error("Selected snapshot exceeds its limit; narrow the selected view before recovering.");
    if (new Set(result.slots.map(slot => slot.slot)).size !== result.slots.length || flight.capture.selected.some(descriptor => !result.slots.some(slot => slot.slot === descriptor.selector.slot && slot.kind === descriptor.selector.kind && ["ready", "not_found", "limit_exceeded"].includes(slot.status)))) throw new Error("Invalid/duplicate observation slots; no batch applied");
    const visible = this.plan();
    for (const descriptor of flight.capture.selected) {
      const key = JSON.stringify(descriptor.selector);
      const slot = result.slots.find(item => item.slot === descriptor.selector.slot);
      if (!slot || slot.kind !== descriptor.selector.kind || !["ready", "not_found", "limit_exceeded"].includes(slot.status)) throw new Error("Invalid observation slot");
      if (!visible.some(item => JSON.stringify(item.selector) === key && item.target === descriptor.target)) continue;
      const meta = { binding: JSON.stringify([this.scope, this.binding, this.bindingEpoch]), queue_identity: result.queue_identity, revision: result.revision, serial: flight.serial, selector: key };
      if (descriptor.apply(slot, meta, result.read_at, cx) !== false) {
        const entry = this.entry(descriptor); entry.target = descriptor.target; entry.revision = result.revision; entry.dirty = false;
      }
    }
    if (result.log && flight.capture.log) this.owner.queue.applyLogObservation(flight.capture.log, result.log, cx);
    if (flight.capture.work) {
      if (!result.work) throw Error("Missing separate filesystem work observation");
      this.owner.workOperator.apply(flight.capture.work, result.work, result.revision, cx, result.main, result.clients);
    }
    this.health.readAt = result.read_at;
    this.needsSnapshot = false; this.failures = 0;
    if (flight.recovery) {
      if (!this.recoveryAuthorized(flight.recovery)) throw Error("Fresh snapshot authorization changed.");
      compatibility.finishRecovery(flight.recovery.token);
      this.recovery = null; this.owner.observationReset = null;
    }
    this.publish("waiting");
  }
}
