import { div } from "gpui-kit";
import { v_flex, InputState, Input } from "gpui-base";
import { WebSocket } from "websocket";
import { URL } from "url";
import { Buffer } from "buffer";

export const pushProtocol = "ocq-push-v1";
const cell = "__ocq_push_connection_v1";
export const physical = globalThis[cell] || (globalThis[cell] = { owner: null, socket: null, settled: Promise.resolve() });

function endpoint(value) {
  let u;
  try { u = new URL(value); } catch (_) { throw Error("Enter a valid ws/wss endpoint ending /ocq."); }
  if (!["ws:", "wss:"].includes(u.protocol) || u.username || u.password || u.search || u.hash || u.pathname !== "/ocq")
    throw Error("Use a ws/wss endpoint ending /ocq, without URL credentials, query or fragment.");
  if (u.protocol === "ws:" && !/^ws:\/\/(?:127\.0\.0\.1|\[::1\])(?::[0-9]+)?\/ocq$/i.test(value))
    throw Error("Unencrypted WebSocket is allowed only on literal loopback. Use explicit TLS or your separately configured local tunnel.");
  return u.toString();
}

export class PushConnection {
  constructor(updates) {
    this.updates = updates;
    this.url = InputState.new({ placeholder: "wss://server:port/ocq" });
    this.secret = InputState.new({ placeholder: "Endpoint token (memory only)" });
    this.secret.set_masked(true);
    this.expanded = true;
    this.state = "disconnected";
    this.pending = new Map();
    for (const input of [this.url, this.secret]) {
      input.on("change", (_e, cx) => cx.notify());
      input.on("focus", () => { this.inputFocused = true; });
      input.on("blur", () => { this.inputFocused = false; });
    }
  }
  configure(cx) { this.expanded = !this.expanded; cx.notify(); }
  connect(cx) {
    if (this.state === "connecting" || this.state === "connected") return;
    let address;
    try {
      address = endpoint(this.url.value().trim());
      if (!/^[0-9a-f]{64}$/.test(this.secret.value().trim())) throw Error("Enter the private endpoint token.");
      if (!this.updates.scope) throw Error("Select an explicit machine/space/folder first.");
      if (globalThis.__ocq_observation_physical_v1?.flight) throw Error("An older native observer is unresolved. Quiesce it and use a fresh runtime; it cannot be replaced by a socket.");
    } catch (error) { this.updates.publish("unavailable", error.message); return; }
    if (physical.socket || physical.owner) { this.updates.publish("unavailable", "Previous socket is still settling. Disconnect it first."); return; }
    const scope = this.updates.scope;
    const generation = ++this.updates.connectionGeneration;
    this.state = "connecting";
    const token = this.secret.value().trim(); // Never persisted, logged or echoed.
    this.secret.set_value("");
    physical.owner = this;
    physical.settled = new Promise(resolve => { this.settle = resolve; });
    this.updates.invalidate("Connecting explicitly; all prior confirmations remain invalid.");
    cx.spawn(async cx => {
      let socket;
      try {
        socket = await WebSocket.connect(address);
        physical.socket = socket;
        this.socket = socket;
        if (!this.current(scope, generation)) { await socket.close(); return; }
        await socket.write(JSON.stringify({ protocol: pushProtocol, type: "auth", token, space: scope.localSpace, folder: scope.folder }));
        const welcome = await this.read(socket);
        if (!this.current(scope, generation)) return;
        this.updates.welcome(welcome);
        this.state = "connected";
        this.expanded = false;
        this.updates.kick();
        cx.notify();
        while (this.current(scope, generation)) {
          const frame = await this.read(socket);
          if (this.current(scope, generation)) {
            if (frame.type === "command-received") {
              if (!this.pending.has(frame.id) || frame.connection_id !== this.updates.connectionID) throw Error("Unexpected transport receipt");
              // Not action success or permission. Keep the durable UI attempt
              // marker uncertain until the original command result arrives.
            } else if (frame.type === "command-result") {
              if (!this.pending.has(frame.id) || frame.connection_id !== this.updates.connectionID) throw Error("Unexpected command receipt");
              const pending = this.pending.get(frame.id); this.pending.delete(frame.id);
              pending.resolve(frame.result);
            } else this.updates.message(frame, cx);
          }
        }
      } catch (_error) {
        if (this.current(scope, generation)) this.updates.invalidate("Push connection unavailable or closed. Reconnect explicitly; no command was retried.");
      } finally {
        this.rejectPending();
        if (socket) { try { await socket.close(); } catch (_) {} }
        if (physical.owner === this) { physical.socket = null; physical.owner = null; this.settle?.(); }
        this.socket = null; this.state = "disconnected";
        if (!this.updates.disposed) cx.notify();
      }
    });
  }
  current(scope, generation) {
    return this.updates.current() && this.updates.scope === scope && this.updates.connectionGeneration === generation;
  }
  async read(socket) {
    const raw = await socket.read();
    if (typeof raw !== "string" || Buffer.from(raw).length > 1048576 + 16384) throw Error("Bounded text push message required");
    const frame = JSON.parse(raw);
    if (frame.protocol !== pushProtocol) throw Error("Unsupported push protocol");
    return frame;
  }
  async write(value) {
    if (!this.socket || this.state !== "connected") throw Error("No authenticated push connection");
    const raw = JSON.stringify(value);
    if (Buffer.from(raw).length > (value.type === "command" ? 1048576 : 16384)) throw Error("Frame exceeds transport bound");
    await this.socket.write(raw);
  }
  rejectPending() {
    for (const pending of this.pending.values()) {
      pending.reject(Error("Command outcome uncertain; inspect the exact retained receipt. No resend."));
    }
    this.pending.clear();
  }
  async command(args, payload) {
    if (this.pending.size >= 4 || !this.updates.current() || this.state !== "connected") throw Error("Command unavailable or four commands are unsettled");
    const id = String(++this.updates.control);
    let resolve, reject;
    const result = new Promise((yes, no) => { resolve = yes; reject = no; });
    this.pending.set(id, { resolve, reject });
    try {
      await this.write({ type: "command", id, request: { connection_id: this.updates.connectionID, args, ...(payload !== undefined ? { payload } : {}) } });
    } catch (_) { this.rejectPending(); }
    return result;
  }
  async disconnect() {
    ++this.updates.connectionGeneration;
    this.updates.invalidate("Disconnected. Stored data may be stale; confirmations stay invalid.");
    this.rejectPending();
    if (this.socket) { try { await this.socket.close(); } catch (_) {} }
    if (physical.owner === this) await physical.settled;
    this.secret.set_value("");
  }
  render(cx, theme) {
    const control = (id, text, action) => div().id(id).role("button").accessibility_label(text).cursor_pointer()
      .px(8).py(4).hover(e => e.bg(theme.hover)).on_click((_e, cx) => action(cx)).child(text);
    return v_flex().px(10).py(6).gap(4).text_size(12)
      .child(control("ocq-push-setup", `Push connection · ${this.state}`, cx => this.configure(cx)))
      .when(this.expanded, e => e
        .child(div().text_color(theme.muted).child("Bind this endpoint to the selected machine, space and folder. Token stays in memory. No HC alias is treated as server identity."))
        .child(div().child("Endpoint URL")).child(Input.new(this.url))
        .child(div().child("Private token · masked, memory only")).child(Input.new(this.secret))
        .child(control("ocq-push-connect", "Connect selected context", cx => this.connect(cx)))
        .child(control("ocq-push-disconnect", "Disconnect", cx => cx.spawn(async () => this.disconnect())))
        .child(div().text_color(theme.muted).child("Actions require server opt-in and fresh exact review. Native operations also require the reviewed private HC binding.")));
  }
}
