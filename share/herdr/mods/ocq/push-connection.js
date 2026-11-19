import { run } from "process";
import { appHome, checking } from "herdr";
import { WebSocket } from "websocket";
import { URL } from "url";
import { Buffer } from "buffer";

export const pushProtocol = "ocq-push-v1";
const cell = "__ocq_push_connection_v1";
export const physical = globalThis[cell] || (globalThis[cell] = { owner: null, socket: null, settled: Promise.resolve() });
const sourceSlots = globalThis.__ocq_runtime_sockets_v1 || (globalThis.__ocq_runtime_sockets_v1 = {});

function endpoint(value) {
  let u;
  try { u = new URL(value); } catch (_) { throw Error("Enter a valid ws/wss endpoint ending /ocq."); }
  if (!["ws:", "wss:"].includes(u.protocol) || u.username || u.password || u.search || u.hash || u.pathname !== "/ocq")
    throw Error("Use a ws/wss endpoint ending /ocq, without URL credentials, query or fragment.");
  if (u.protocol === "ws:" && !/^ws:\/\/(?:127\.0\.0\.1|\[::1\])(?::[0-9]+)?\/ocq$/i.test(value))
    throw Error("Unencrypted WebSocket is allowed only on literal loopback. Use explicit TLS or your separately configured local tunnel.");
  return u.toString();
}

function contractError(message) { return Object.assign(Error(message), { permanent: true }); }
// Pinned native WebSocket terminal result, not a generic transport failure.
// See docs/push-transport.md. Only close's result is accepted; callers still wait for
// the original read loop and every outstanding write before releasing ownership.
const nativeClosed = "WebSocket connection is closed";

export class PushConnection {
  constructor(updates) {
    this.updates = updates;
    this.physical = updates.runtime
      ? (sourceSlots[updates.sourceKey] ||= { owner: null, socket: null, settled: Promise.resolve() }) : physical;
    this.state = "disconnected";
    this.pending = new Map();
    this.writes = new Set();
    this.lifecycle = 0; this.attempt = 0;
  }
  associate(scope, cx) {
    const physical = this.physical;
    const lifecycle = ++this.lifecycle;
    this.cancelRetry(); this.association = null; this.fault = null; this.faultPhase = null; this.attempt = 0;
    this.executionPeer = null; this.executionCore = null;
    const draining = this.disconnect();
    if (checking()) return;
    cx.spawn(async cx => {
      this.transportCX = cx;
      await draining;
      // No grace-period reset: an orphaned owner holds this slot indefinitely.
      if (physical.owner && physical.owner !== this)
        this.updates.publish("unavailable", "A previous panel connection is still finishing. Actions remain unavailable.");
      await physical.settled;
      if (!this.active(scope, lifecycle)) return;
      let phase = "bootstrap";
      try {
        if (this.updates.runtime) {
          this.association = this.updates.runtime;
          this.connect(scope, lifecycle, cx);
          return;
        }
        if (globalThis.__ocq_observation_physical_v1?.flight) throw Error("Older observer is unresolved.");
        // Fixed local bootstrap, not selected-machine exec or an observation
        // fallback. Never log stdout/stderr: stdout carries the private token.
        const directory = `${appHome()}/ocq-push`;
        const result = await run(`${directory}/ocq-herdr`, ["push-association", directory, scope.machine ?? "", scope.space, scope.folder]);
        if (!this.active(scope, lifecycle)) return;
        phase = "configuration";
        if (result.code !== 0 || result.stdout.length > 32768) throw Error("Private association unavailable.");
        phase = "configuration-contract";
        const a = JSON.parse(result.stdout);
        if (a.version !== 1 || a.machine !== scope.machine || a.space !== scope.space || a.folder !== scope.folder ||
          !/^[0-9a-f]{64}$/.test(a.queue_identity) || !/^[0-9a-fA-F]{64}$/.test(a.token)) throw Error("Private association mismatch.");
        a.endpoint = endpoint(a.endpoint);
        this.association = a;
      } catch (_) {
        if (this.active(scope, lifecycle)) {
          this.faultPhase = phase; // Constant diagnostic only; never stderr/token.
          this.stopWithFault(phase === "bootstrap" ? "Local OCQ bootstrap unavailable." : "Check private OCQ association.", cx);
        }
        return;
      }
      this.connect(scope, lifecycle, cx);
    }, { owner: null });
  }
  active(scope, lifecycle) {
    return lifecycle === this.lifecycle && this.updates.scope === scope && this.updates.current();
  }
  cancelRetry() { this.retry?.cancel(); this.retry = null; }
  notify() {
    if (!this.updates.disposed && !this.updates.owner.closed)
      (this.updates.cx ?? this.updates.owner.cx)?.notify();
  }
  stopWithFault(reason, cx) {
    this.cancelRetry(); this.fault = reason; this.association = null;
    this.updates.publish("unavailable", reason); this.notify();
  }
  connect(scope, lifecycle, cx) {
    const physical = this.physical;
    if (!this.active(scope, lifecycle) || !this.association) return;
    if (physical.owner || physical.socket) {
      this.stopWithFault("Another OCQ panel owns this connection.", cx);
      return;
    }
    const generation = ++this.updates.connectionGeneration;
    const association = this.association;
    this.state = "connecting";
    this.closing = null;
    physical.owner = this;
    physical.settled = new Promise(resolve => { this.settle = resolve; });
    this.updates.invalidate("Connection changed; prior confirmations remain invalid.");
    this.updates.publish("reconnecting");
    cx.spawn(async cx => {
      let socket, permanent = false;
      try {
        socket = await WebSocket.connect(association.endpoint);
        physical.socket = socket;
        this.socket = socket;
        if (!this.current(scope, generation)) return;
        await socket.write(JSON.stringify(this.updates.runtime
          ? { protocol: pushProtocol, type: "auth", token: association.token, core_id: association.core_id, queue_identity: association.queue_identity, peer: scope.machine ?? "" }
          : { protocol: pushProtocol, type: "auth", token: association.token, space: scope.localSpace, folder: scope.folder }));
        const welcome = await this.read(socket);
        if (!this.current(scope, generation)) return;
        try {
          if (welcome.descriptor?.queue_identity !== association.queue_identity) throw Error("Queue identity mismatch");
          if (this.updates.runtime && (welcome.runtime?.protocol !== "ocq-runtime-v1" ||
              welcome.runtime.core_id !== association.core_id || welcome.runtime.queue_identity !== association.queue_identity ||
              welcome.execution_peer !== (scope.machine ?? "local"))) throw Error("Runtime core/queue/route identity mismatch");
          if (!this.updates.runtime && welcome.execution_peer !== undefined &&
              (welcome.execution_peer !== scope.machine || typeof welcome.execution_core_id !== "string" || !welcome.execution_core_id))
            throw Error("Selected execution machine mismatch");
          this.updates.welcome(welcome);
          this.executionPeer = welcome.execution_peer ?? null;
          this.executionCore = welcome.execution_core_id ?? null;
        } catch (_) { throw contractError("Check OCQ endpoint identity or version."); }
        this.state = "connected";
        this.updates.kick();
        this.notify();
        while (this.current(scope, generation)) {
          const frame = await this.read(socket);
          if (this.current(scope, generation)) {
            try {
              if (frame.type === "command-received") {
                if (!this.pending.has(frame.id) || frame.connection_id !== this.updates.connectionID) throw Error("Unexpected transport receipt");
                // Not action success or permission. Keep the durable UI attempt
                // marker uncertain until the original command result arrives.
              } else if (frame.type === "command-result") {
                if (!this.pending.has(frame.id) || frame.connection_id !== this.updates.connectionID) throw Error("Unexpected command receipt");
                const pending = this.pending.get(frame.id); this.pending.delete(frame.id);
                pending.resolve(frame.result);
              } else this.updates.message(frame, this.updates.cx ?? this.updates.owner.cx);
            } catch (_) { throw contractError("Check OCQ endpoint contract."); }
          }
        }
      } catch (error) {
        permanent = error.permanent === true;
        if (this.current(scope, generation)) {
          this.updates.invalidate("Connection lost; prior confirmations invalid. Pending outcomes remain uncertain; no resend.");
          if (permanent) this.stopWithFault("Check OCQ endpoint identity or version.", cx);
          else this.updates.publish("reconnecting");
        }
      } finally {
        this.state = "closing";
        this.rejectPending();
        let closeError;
        if (socket) {
          try { await this.closeSocket(socket); }
          catch (error) { closeError = error; }
          // No timeout/cancel as proof. Closing stops new writes; native writes
          // already issued must themselves finish before a successor is possible.
          await Promise.allSettled([...this.writes]);
          if (closeError) {
            // An unsuccessful close is not proof of physical settlement. Hold
            // the global slot; neither retries nor successors may replace it.
            this.state = "unsettled";
            if (this.active(scope, lifecycle)) this.stopWithFault("OCQ connection did not settle; restart the app after inspection.", cx);
            return;
          }
        }
        if (physical.owner === this) { physical.socket = null; physical.owner = null; this.settle?.(); }
        this.socket = null; this.state = "disconnected";
        // Only after close and the physical owner have actually settled.
        if (!permanent && this.active(scope, lifecycle) && this.association) {
          const delay = Math.min(30000, 500 * 2 ** Math.min(this.attempt++, 6)) * (0.75 + Math.random() * 0.25);
          this.retry = cx.timer.after(delay, cx => {
            this.retry = null;
            if (this.active(scope, lifecycle)) this.connect(scope, lifecycle, cx);
          });
        }
        this.notify();
      }
    }, { owner: null });
  }
  current(scope, generation) {
    return this.updates.current() && this.updates.scope === scope && this.updates.connectionGeneration === generation;
  }
  async read(socket) {
    const raw = await socket.read();
    if (typeof raw !== "string" || Buffer.from(raw).length > 1048576 + 16384) throw contractError("Bounded text push message required");
    let frame;
    try { frame = JSON.parse(raw); } catch (_) { throw contractError("Invalid push message"); }
    if (!frame || frame.protocol !== pushProtocol) throw contractError("Unsupported push protocol");
    return frame;
  }
  write(value) {
    if (!this.socket || this.state !== "connected") throw Error("No authenticated push connection");
    const raw = JSON.stringify(value);
    if (Buffer.from(raw).length > (value.type === "command" ? 1048576 : 16384)) throw Error("Frame exceeds transport bound");
    const socket = this.socket;
    return new Promise((resolve, reject) => {
      // Both the native write AND its accounting belong to this task. A view
      // caller may disappear while awaiting the result; it must not own the
      // finally that removes the original native promise from the drain set.
      this.transportCX.spawn(async () => {
        try {
          if (this.state !== "connected" || this.socket !== socket) throw Error("Connection changed before write");
          const operation = socket.write(raw);
          this.writes.add(operation);
          try { await operation; }
          finally { this.writes.delete(operation); }
          resolve();
        } catch (error) { reject(error); }
      }, { owner: null });
    });
  }
  rejectPending() {
    for (const pending of this.pending.values()) {
      pending.reject(Error("Command outcome uncertain; inspect the exact retained receipt. No resend."));
    }
    this.pending.clear();
  }
  async command(args, payload, context) {
    if (this.pending.size >= 4 || !this.updates.current() || this.state !== "connected") throw Error("Command unavailable or four commands are unsettled");
    const id = String(++this.updates.control);
    let resolve, reject;
    const result = new Promise((yes, no) => { resolve = yes; reject = no; });
    this.pending.set(id, { resolve, reject });
    try {
      await this.write({ type: "command", id, request: { connection_id: this.updates.connectionID, args, ...(payload !== undefined ? { payload } : {}), ...(context ? { context } : {}) } });
    } catch (_) {
      this.rejectPending();
      // A failed command is never retried, even if observation reconnects.
      void this.disconnect();
    }
    return result;
  }
  closeSocket(socket) {
    if (!this.closing) this.closing = new Promise((resolve, reject) => {
      this.transportCX.spawn(async () => {
        try { await socket.close(); resolve(); }
        catch (error) {
          // Close alone is not settlement: the read owner also drains writes.
          if (error?.message === nativeClosed) resolve();
          else reject(error);
        }
      }, { owner: null });
    });
    return this.closing;
  }
  async disconnect() {
    const physical = this.physical;
    this.cancelRetry();
    this.state = "closing";
    ++this.updates.connectionGeneration;
    this.updates.invalidate("Disconnected. Stored data may be stale; confirmations stay invalid.");
    this.rejectPending();
    if (this.socket) { try { await this.closeSocket(this.socket); } catch (_) { /* Physical slot remains held. */ } }
    if (physical.owner === this) await physical.settled;
    if (physical.owner !== this) this.state = "disconnected";
  }
  async dispose(cx) {
    // close(cx) supplies the live cleanup context; the connect task's context
    // belongs to the build being retired and cannot spawn after its reload.
    if (cx) this.transportCX = cx;
    ++this.lifecycle; this.association = null;
    await this.disconnect();
  }
}
