import { spaces } from "herdr";
import { compatibility } from "./compatibility.js";
import { PushConnection } from "./push-connection.js";

const protocol = "ocq-observe-v1";
const hex = s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s);
export const decimal = s => typeof s === "string" && /^(0|[1-9][0-9]*)$/.test(s) && (s.length < 19 || s.length === 19 && s <= "9223372036854775807");
export function compareRevision(a, b) { return a.length === b.length ? (a === b ? 0 : a < b ? -1 : 1) : a.length < b.length ? -1 : 1; }
export function acceptObservationVersion(previous, next) {
  if (!next || !decimal(next.revision) || !next.binding || !next.selector) return false;
  if (!previous) return true;
  if (previous.binding !== next.binding || previous.selector !== next.selector) return false;
  const order = compareRevision(next.revision, previous.revision);
  return order > 0 || order === 0 && next.serial > previous.serial;
}
function pins(v) { return { queue_identity: v.queue_identity, store_context: v.store_context, bridge_context: v.bridge_context }; }
function currentScope(scope) {
  const s = spaces().find(s => s.active);
  return s && s.id === scope.space && s.machine === scope.machine && s.folder === scope.folder;
}

// Exactly one native socket. Only transport loss has a recovery timer; projection
// and configuration reads never poll or fall back to execution.
export class StoredUpdates {
  constructor(owner, cx) {
    this.owner = owner; this.serial = 0;
    this.connectionGeneration = 0; this.bindingEpoch = 0; this.control = 0;
    this.connection = new PushConnection(this);
    this.health = { state: "starting", reason: null, readAt: null };
    cx.spawn(task => { this.cx = task; }, { owner });
  }
  sync(cx) {
    if (this.retired) return;
    const o = this.owner, next = { space: o.spaceID, machine: o.machine, folder: o.folder, panelEpoch: o.panelEpoch };
    next.localSpace = next.machine !== null && next.space?.startsWith(`${next.machine}/`) ? next.space.slice(next.machine.length + 1) : next.space;
    if (!next.space || !next.folder || !/^sp[0-9a-f]{32}$/.test(next.localSpace)) {
      if (this.scope) {
        this.scope = null;
        cx.spawn(async () => this.connection.dispose());
      }
      return;
    }
    if (!this.scope || JSON.stringify(this.scope) !== JSON.stringify(next)) {
      if (this.scope) {
        this.owner.resetObservationVersions();
      }
      this.scope = Object.freeze(next); this.disposed = false;
      this.binding = null; this.revision = null; this.bindingEpoch++;
      this.subscription = null; this.lastKey = null;
      this.publish("starting");
      this.connection.associate(this.scope, cx);
    }
    this.kick();
  }
  current() { return !this.disposed && this.scope && currentScope(this.scope) && this.owner.panelCurrent(); }
  async dispose() {
    this.disposed = true; this.retired = true;
    await this.connection.dispose();
  }
  readAgain() {
    this.lastKey = null; // Explicit user read/selection change, not a timer.
    this.kick();
  }
  suspendRecovery() { this.lastKey = null; }
  publish(state, reason = null) {
    this.health = { ...this.health, state, reason };
    this.owner.updateHealth = this.health;
    if (this.cx?.phase() !== "render") this.cx?.notify();
  }
  invalidate(reason) {
    this.binding = null; this.subscription = null; this.lastKey = null;
    this.owner.resetObservationVersions();
    compatibility.invalidateObservation(reason);
    this.owner.observationReset = reason;
    this.publish("unavailable", reason);
  }
  welcome(frame) {
    if (frame.type !== "welcome" || !/^[0-9a-f]{32}$/.test(frame.connection_id) || typeof frame.mutations !== "boolean" ||
      JSON.stringify(frame.commands) !== JSON.stringify(["subscribe", "unsubscribe", "ping", "close", "command"])) throw Error("Unsupported endpoint capabilities");
    const d = compatibility.validate(frame.descriptor, { id: this.scope.space, machine: this.scope.machine, folder: this.scope.folder });
    if (![6, 7].includes(d.observation_schema) || !hex(d.queue_identity) || !hex(d.observation_context) || !hex(d.observation_store_context)) throw Error("Existing schema6/7 queue required");
    compatibility.observe();
    compatibility.status = d;
    compatibility.pushConnection = this.connection;
    compatibility.allowActions = frame.mutations;
    compatibility.reason = frame.mutations ? "Reviewed OCQ actions enabled by endpoint owner. Agent reconciliation requires its own human confirmation; native client/dispatcher execution still requires a pinned HC host/core." : "Endpoint owner has not enabled actions; explicit reads and read-only handoff verification remain available.";
    this.binding = Object.freeze({ queue_identity: d.queue_identity, store_context: d.observation_store_context, bridge_context: d.observation_context });
    this.connectionID = frame.connection_id; this.sequence = "0"; this.revision = null;
    this.lastKey = null; this.bindingEpoch++;
    // Read freshness and endpoint opt-in are separate from action authority.
    this.owner.observationReset = null;
    this.publish("reading", "Authenticated; waiting for the server's initial projection.");
  }
  plan() { return this.owner.observationSelectors(); }
  log() { return this.owner.queue.observationLog(this.owner); }
  work() { return this.owner.workOperator.observation(); }
  kick() {
    if (!this.current() || !this.binding || this.connection.state !== "connected") return;
    const planned = this.plan(), selected = planned.slice(0, 8);
    this.selectorLimit = planned.length > 8;
    if (this.selectorLimit) this.publish("limit_exceeded", "Only the first eight selected pages are subscribed. Later pages are not current; reopen the panel to reset retained pages.");
    const log = this.log(), work = this.work();
    const request = { protocol, ...this.binding, selectors: selected.map(d => d.selector) };
    if (log?.needsRead || log?.watch) request.log = log.cursor;
    if (work) request.work = { project_id: work.cursor.project_id, fingerprint: "" };
    if (!selected.length && !request.log && !request.work) return;
    // Log offsets advance on the server, not through UI read renewals.
    const key = JSON.stringify({ ...request, ...(request.log ? { log: { job_id: request.log.job_id, stream: request.log.stream } } : {}) });
    // Only subscription/selection changes write controls; dirty hints and
    // revisions never issue a projection read. The server pushes the state.
    const capture = { selected, log: request.log ? log : null, work: request.work ? work : null };
    const previous = this.subscription?.capture;
    if (key === this.lastKey && previous && previous.selected.length === selected.length &&
      selected.every((d, index) => d.target === previous.selected[index].target) &&
      (capture.log?.target ?? null) === (previous.log?.target ?? null)) {
      this.subscription.capture = capture; return;
    }
    if (this.sending) return;
    this.lastKey = key;
    const scope = this.scope, generation = this.connectionGeneration;
    this.sending = true;
    this.cx.spawn(async cx => {
      try {
        if (!this.current() || this.scope !== scope || this.connectionGeneration !== generation) return;
        const id = String(++this.control);
        this.subscription = { id, capture, scope, generation };
        await this.connection.write({ type: "subscribe", id, request });
      } catch (_) {
        if (this.scope === scope && this.connectionGeneration === generation) {
          // Closing the one owner wakes its read loop; its finally owns recovery.
          await this.connection.disconnect();
        }
      } finally { this.sending = false; cx.notify(); }
    });
  }
  message(frame, cx) {
    if (frame.connection_id !== this.connectionID) throw Error("Connection generation changed");
    if (frame.type === "pong" || frame.type === "unsubscribed") return;
    if (frame.type !== "snapshot" || !decimal(frame.sequence) || compareRevision(frame.sequence, this.sequence) <= 0) throw Error("Push frame ordering invalid");
    this.sequence = frame.sequence;
    const s = this.subscription;
    if (!s || frame.id !== s.id || this.scope !== s.scope || this.connectionGeneration !== s.generation) return;
    this.receive(frame.snapshot, { command: "push", capture: s.capture, serial: ++this.serial }, cx);
  }
  receive(result, flight, cx) {
    if (result.protocol !== protocol || result.status !== "ready" || ![6, 7].includes(result.schema) || result.consistency !== "single_sqlite_snapshot" ||
      !decimal(result.revision) || !this.binding || JSON.stringify(pins(result)) !== JSON.stringify(this.binding) ||
      !Array.isArray(result.slots) || result.slots.length !== flight.capture.selected.length) throw Error("Pinned coherent snapshot required");
    if (this.revision && compareRevision(result.revision, this.revision) < 0) throw Error("Queue revision decreased; investigate restore");
    if (new Set(result.slots.map(s => s.slot)).size !== result.slots.length ||
      flight.capture.selected.some(d => !result.slots.some(s => s.slot === d.selector.slot && s.kind === d.selector.kind && ["ready", "not_found", "limit_exceeded", "reset_required"].includes(s.status)))) throw Error("Incomplete projection batch");
    this.revision = result.revision;
    const visible = this.plan();
    for (const d of flight.capture.selected) {
      const key = JSON.stringify(d.selector), slot = result.slots.find(s => s.slot === d.selector.slot);
      if (!visible.some(v => JSON.stringify(v.selector) === key && v.target === d.target)) continue;
      const meta = { binding: JSON.stringify([this.scope, this.binding, this.bindingEpoch]), queue_identity: result.queue_identity, revision: result.revision, serial: flight.serial, selector: key };
      d.apply(slot, meta, result.read_at, cx);
    }
    if (result.log && flight.capture.log) this.owner.queue.applyLogObservation(flight.capture.log, result.log, cx);
    if (flight.capture.work) {
      if (!result.work) throw Error("Missing separate filesystem work projection");
      this.owner.workOperator.apply(flight.capture.work, result.work, result.revision, cx, result.main, result.clients);
    }
    this.health.readAt = result.read_at;
    this.connection.attempt = 0;
    if (this.plan().length <= 8) this.publish("waiting", compatibility.allowActions
      ? "Server push current · actions still require fresh exact review; this is not execution success."
      : "Server push current · endpoint actions are disabled.");
    // Applying a frame can replace pending-page closures or advance a log
    // cursor. Refresh captures before the next socket frame, not only on the
    // next render; two committed pushes may arrive within one native frame.
    this.kick();
  }
}
