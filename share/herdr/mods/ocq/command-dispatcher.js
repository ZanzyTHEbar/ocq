import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { run, focused, spaces, panes, terminals } from "herdr";
import { exec, compatibility } from "./compatibility.js";

const hex = (s) => /^[0-9a-f]{64}$/.test(s || "");
const visible = (s) => Array.from(JSON.stringify(s), c => /[\p{Cc}\p{Default_Ignorable_Code_Point}\p{Zl}\p{Zp}]/u.test(c) ? Array.from({ length: c.length }, (_, n) => `\\u${c.charCodeAt(n).toString(16).padStart(4, "0")}`).join("") : c).join("");
const warning = "COMMAND EXECUTION: Start can execute queued COMMANDS immediately and while running. Fixed commands-only, 2 slots, 6 GiB floor. Agent at FIFO head blocks later commands; unknown demand retains capacity. No agent/API polling or permission handling.";
const closure = "KEEP THE TERMINAL/PANE/SPACE OPEN UNTIL ACTUAL EXIT. Closing it can send HUP, TERM and force KILL in about 1 second. Use explicit Drain instead. No guaranteed grace, global close interception, restart supervision or escaped-process containment. Missing terminal outcome remains UNKNOWN, never success/retry/capacity release.";

export class CommandDispatcher {
  constructor(view) { this.view = view; }
  current(owner) { return this.view.dispatcher === this && this.screen && owner.machine === null && this.view.current(owner, this.generation) && owner.spaceID === this.space && owner.folder === this.folder; }
  scopeSame(a, b) { return a && b && ["machine", "space_id", "queue_identity"].every(k => a[k] === b[k]); }
  targetSame(a, b) { return a && b && this.scopeSame(a.scope, b.scope) && ["pane", "terminal", "tab", "space_folder"].every(k => a[k] === b[k]); }
  localTarget() { return this.t && this.scopeSame(this.t.scope, this.scope) && this.t.space_folder === this.folder; }
  candidates() {
    const s = spaces().find(s => s.active && s.machine === null && s.id === this.space && s.folder === this.folder);
    if (!s || !this.scope) return [];
    return panes().filter(p => p.terminal && !p.mod && s.tabs.some(t => t.id === p.tab)).filter(p => terminals().some(t => t.address === p.terminal && t.state === "live" && t.labels?.["herdr.space"] === s.id && t.labels?.["herdr.tab"] === p.tab)).map(p => ({ scope: this.scope, pane: p.pane, terminal: p.terminal, tab: p.tab, space_folder: s.folder }));
  }
  anchorValid() { return this.localTarget() && this.candidates().some(t => this.targetSame(t, this.t)); }
  capture() {
    return Object.freeze({ target: Object.freeze({ ...this.t, scope: Object.freeze({ ...this.t.scope }) }), bridgeImage: compatibility.status?.image_sha256, id: this.status.id, digest: this.status.intent_sha256, owner: this.status.owner && Object.freeze({ ...this.status.owner }), native: this.status.owner && this.nativeOwner(this.status) });
  }
  operationCurrent(owner, operation, status = this.status) {
    const same = s => s?.id === operation.id && s?.intent_sha256 === operation.digest && this.targetSame(s.target, operation.target);
    return this.current(owner) && this.targetSame(this.t, operation.target) && same(this.status) && same(status);
  }
  ownedCurrent(operation) { return operation.owner && operation.native && ["pid", "start", "boot", "terminal"].every(k => this.status?.owner?.[k] === operation.owner[k]) && JSON.stringify(this.nativeOwner(this.status)) === JSON.stringify(operation.native); }
  focusedTarget() { const f = focused(), t = this.t; return f && t && f.space === t.scope.space_id && f.tab === t.tab && f.pane === t.pane && f.terminal === t.terminal && f.cwd === t.space_folder; }
  args() { const t = this.t; return [t.scope.space_id, t.scope.queue_identity, t.pane, t.terminal, t.tab, t.space_folder]; }
  validate(s) {
    if (!s || !this.scopeSame(s.target?.scope, this.scope) && s.state !== "other_context" || s.commands_only !== true || s.slots !== 2 || s.memory_floor_bytes !== "6442450944") throw Error("Dispatcher context/policy unavailable");
    if (s.id && (!hex(s.id) || !hex(s.intent_sha256))) throw Error("Dispatcher intent unavailable");
    if (s.owner && (!Number.isInteger(s.owner.pid) || s.owner.pid < 1 || !/^[1-9][0-9]*$/.test(s.owner.start) || typeof s.owner.boot !== "string")) throw Error("Owned dispatcher identity unavailable");
    return s;
  }
  async call(action, extra = []) {
    const t = this.t, status = this.status;
    const args = action === "read" ? [this.scope.space_id, this.scope.queue_identity] : [...this.args(), ...(!["status", "prepare"].includes(action) ? [this.status.id] : []), ...extra];
    const r = await exec("ocq-herdr", [`dispatcher-${action}`, ...args], { on: null, timeout: 30000, current: () => this.boundCurrent?.() && this.t === t && this.status === status });
    if (r.code !== 0) throw Error("Dispatcher action/readback unavailable. No retry of uncertain launch or signal.");
    return this.validate(JSON.parse(r.stdout));
  }
  open(owner, cx) {
    if (owner.machine !== null || !this.view.current(owner, this.view.generation)) return;
    const d = new CommandDispatcher(this.view); this.view.dispatcher = d;
    d.screen = true; d.generation = this.view.generation; d.space = owner.spaceID; d.folder = owner.folder;
    d.boundCurrent = () => d.current(owner);
    owner.handle.focus(); d.act(owner, "load", cx);
  }
  close(owner, cx) { if (!this.current(owner)) return; this.view.dispatcher = new CommandDispatcher(this.view); owner.handle.focus(); cx.notify(); }
  nativeOwner(s) {
    const o = s?.owner; if (!o || !s.executable || !hex(s.id)) return null;
    const argv = [s.executable, "dispatcher-helper", s.id];
    const t = terminals().find(t => t.address === o.terminal && t.state === "live" && t.pid === o.pid && t.foreground?.pid === o.pid && JSON.stringify(t.argv) === JSON.stringify(argv) && JSON.stringify(t.foreground.argv) === JSON.stringify(argv) && t.labels?.["herdr.space"] === this.space && t.labels?.["herdr.tab"] === this.t.tab);
    const p = t && panes().find(p => p.terminal === o.terminal && p.tab === this.t.tab && p.pane !== this.t.pane);
    if (!p || s.helper_pane && p.pane !== s.helper_pane) return null;
    return { terminal: o.terminal, pane: p.pane, tab: p.tab, pid: String(o.pid), start: o.start };
  }
  act(owner, action, cx) {
    if (this.working || !this.current(owner)) return;
    if (!["load", "read", "prepare"].includes(action) && !this.localTarget()) return;
    if (action === "prepare" && (!this.anchorValid() || this.status?.state !== "none" || this.status.busy)) return;
    const operation = this.t && this.status?.id ? this.capture() : null;
    const current = () => operation ? this.operationCurrent(owner, operation) : this.current(owner);
    this.working = true; this.error = null; this.confirm = null;
    cx.spawn(async cx => {
      try {
        if (!current()) return;
        if (action === "prepare" && !this.anchorValid() || action === "drain-prepare" && !this.ownedCurrent(operation)) return;
        let status;
        if (action === "load") {
          const r = await exec("ocq-herdr", ["dispatcher-context", this.space], { on: null });
          if (!this.current(owner)) return;
          if (r.code !== 0) throw Error("Existing queue unavailable; no initialization or launch");
          this.scope = JSON.parse(r.stdout);
          status = await this.call("read");
        } else status = await this.call(action);
        if (!current()) return;
        this.status = status;
        if (this.status.target.pane) this.t = this.status.target;
        if (action === "drain-prepare") this.confirm = "drain";
        if (action === "retire") {
          const next = await this.call("read");
          if (!current()) return;
          this.t = null; this.status = next;
        }
      } catch (e) { if (this.current(owner)) this.error = String(e); }
      finally { this.working = false; if (this.current(owner)) cx.notify(); }
    }); cx.notify();
  }
  confirmStart(owner, cx) {
    if (this.working || !this.current(owner) || this.confirm !== "start" || this.status?.state !== "prepared" || !this.anchorValid()) return;
    const operation = this.capture();
    this.working = true; this.confirm = null; this.error = null;
    cx.spawn(async cx => {
      try {
        if (!this.operationCurrent(owner, operation) || !this.anchorValid() || this.status.state !== "prepared") return;
        let status = await this.call("arm");
        if (!this.operationCurrent(owner, operation, status)) return;
        this.status = status;
        if (!this.anchorValid() || status.state !== "armed") return;
        status = await this.call("attempt");
        if (!this.operationCurrent(owner, operation, status)) return;
        this.status = status;
        if (!this.anchorValid() || status.state !== "unknown") return;
        const id = operation.id, executable = status.executable;
        run("pane.focus", { pane: this.t.pane }); // exact explicitly selected model pane
        const check = await this.call("status"); // read-only acknowledgement barrier
        if (!this.operationCurrent(owner, operation, check) || !this.anchorValid() || !this.focusedTarget() || check.state !== "unknown") throw Error("Exact pane/context changed before launch. Attempt retained; no process retry.");
        const launchCurrent = () => this.operationCurrent(owner, operation, check) && this.anchorValid() && this.focusedTarget() && check.state === "unknown";
        await compatibility.native(this.scope.queue_identity, operation.bridgeImage, launchCurrent);
        if (!launchCurrent()) throw Error("Context changed before native launch");
        run("terminal.open", { argv: [executable, "dispatcher-helper", id], folder: this.t.space_folder, place: "right", name: `OCQ commands-only ${id}` });
        // One native call only. Binding is part of this acknowledged start,
        // NEVER an action performed by status, reload or uncertain recovery.
        let bound = false;
        for (let n = 0; n < 240; n++) {
          if (!this.operationCurrent(owner, operation)) return;
          status = await this.call("status");
          if (!this.operationCurrent(owner, operation, status)) return;
          this.status = status;
          if (this.status.state === "born" && !bound) {
            if (!this.anchorValid()) throw Error("Exact original anchor unavailable before binding. Leave helper unbound; read back only, never relaunch or bind on recovery.");
            const native = this.nativeOwner(this.status);
            if (native) {
              status = await this.call("bind", [native.terminal, native.pane, native.tab, native.pid, native.start]);
              bound = true;
              if (!this.operationCurrent(owner, operation, status)) return;
              this.status = status;
            }
          }
          if (["ready", "stopped", "exit_recorded"].includes(this.status.state)) return;
          await cx.sleep(50);
        }
        throw Error("Launch/readiness uncertain. Read back only; never resend native open.");
      } catch (e) { if (this.operationCurrent(owner, operation)) this.error = String(e); }
      finally { this.working = false; if (this.operationCurrent(owner, operation)) { this.confirm = null; cx.notify(); } }
    }); cx.notify();
  }
  confirmDrain(owner, cx) {
    if (this.working || !this.current(owner) || this.confirm !== "drain" || this.status?.state !== "drain_prepared" || !this.localTarget() || !this.nativeOwner(this.status)) return;
    const operation = this.capture();
    this.working = true; this.confirm = null; this.error = null;
    cx.spawn(async cx => {
      try {
        if (!this.operationCurrent(owner, operation) || !this.ownedCurrent(operation) || this.status.state !== "drain_prepared") return;
        let status = await this.call("drain-arm");
        if (!this.operationCurrent(owner, operation, status)) return;
        this.status = status;
        if (!this.ownedCurrent(operation) || status.state !== "drain_armed") return;
        status = await this.call("drain-attempt"); // one pidfd TERM, no close
        if (!this.operationCurrent(owner, operation, status)) return;
        this.status = status;
      } catch (e) { if (this.operationCurrent(owner, operation)) this.error = String(e); }
      finally { this.working = false; if (this.operationCurrent(owner, operation)) cx.notify(); }
    }); cx.notify();
  }
  key(e, owner, cx) { if (e.key === "escape" && !this.working) { if (this.confirm) this.confirm = null; else this.close(owner, cx); cx.notify(); cx.stop_propagation(); } }
  render(owner, cx, t, button) {
    const s = this.status, local = this.localTarget(), state = s?.state;
    const box = v_flex().id("ocq-command-dispatcher").flex_none().gap(10).p(12).text_color(t.foreground);
    const finish = () => v_flex().id("ocq-command-dispatcher-scroll").size_full().min_h(0).overflow_y_scroll().bg(t.background).track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx)).child(box);
    box.child(div().font_semibold().child("LOCAL COMMANDS-ONLY dispatcher"))
      .child(div().text_color(t.ansi3).child(warning)).child(div().text_color(t.ansi1).child(closure))
      .child(div().text_size(12).child(`Space: ${this.space}\nSpace folder: ${visible(this.folder)}\nQueue: ${visible(s?.queue_dir || "unavailable")}\nQueue identity: ${this.scope?.queue_identity || "unavailable"}`));
    if (s) box.child(div().child(`State: ${state} · singleton ${s.busy ? "held" : "free"}\nOperation: ${s.id || "none"}\nProcess: ${s.process}${s.owner ? ` · PID ${s.owner.pid} start ${s.owner.start} boot ${s.owner.boot}\nTerminal ${s.owner.terminal} · helper pane ${s.helper_pane || "not bound"}` : ""}\n${s.dispatcher_outcome || "Dispatcher/process state is not job success or capacity release."}`));
    if (this.error) box.child(div().text_color(t.ansi1).child(this.error));
    if (this.working) { box.child(div().child("One confirmed action in progress. Do not resend uncertain launch/signal.")); return finish(); }
    if (this.confirm) {
      box.child(div().font_semibold().child(this.confirm === "start" ? "START these queued COMMANDS once?" : this.confirm === "drain" ? "DRAIN this exact owned dispatcher once?" : "Retire this proven stopped/cancelled lifetime? History is retained."));
      if (this.t) box.child(div().text_size(12).child(`Exact anchor pane ${this.t.pane}\nAnchor terminal ${this.t.terminal} · tab ${this.t.tab}`));
      if (this.confirm === "start") box.child(button("ocq-dispatch-confirm-start", "Confirm one native START", cx => this.confirmStart(owner, cx)));
      if (this.confirm === "drain") box.child(button("ocq-dispatch-confirm-drain", "Confirm one TERM DRAIN · leave terminal open", cx => this.confirmDrain(owner, cx)));
      if (this.confirm === "retire") box.child(button("ocq-dispatch-confirm-retire", "Retire proven lifetime; do not start", cx => this.act(owner, "retire", cx)));
      box.child(button("ocq-dispatch-back-confirm", "Back without action", cx => { this.confirm = null; cx.notify(); })); return finish();
    }
    if (state === "none") {
      box.child(div().child("Explicitly select a live model pane in this exact local space. No pane is guessed or selected automatically."));
      for (const candidate of this.candidates()) box.child(button(`ocq-dispatch-pane-${candidate.pane}`, `Select pane ${candidate.pane}\nTerminal ${candidate.terminal} · tab ${candidate.tab}`, cx => { this.t = candidate; owner.handle.focus(); cx.notify(); }));
      if (this.t) box.child(div().text_size(12).child(`Selected: ${this.t.pane}`));
      if (this.anchorValid()) box.child(button("ocq-dispatch-prepare", "Prepare fixed commands-only intent · NO launch", cx => this.act(owner, "prepare", cx)));
    }
    if (state === "prepared" && local && this.anchorValid()) box.child(button("ocq-dispatch-review-start", "Review explicit START…", cx => { this.confirm = "start"; owner.handle.focus(); cx.notify(); }));
    if (["prepared", "armed"].includes(state) && local) box.child(button("ocq-dispatch-cancel", "Cancel unlaunched intent", cx => this.act(owner, "cancel", cx)));
    if (state === "ready" && local && this.nativeOwner(s)) box.child(button("ocq-dispatch-review-drain", "Prepare / review separate DRAIN…", cx => this.act(owner, "drain-prepare", cx)));
    if (state === "drain_prepared" && local && this.nativeOwner(s)) box.child(button("ocq-dispatch-review-retained-drain", "Review retained DRAIN…", cx => { this.confirm = "drain"; cx.notify(); }));
    if (s?.can_retire && local) box.child(button("ocq-dispatch-review-retire", "Review safe retirement (no restart)…", cx => { this.confirm = "retire"; cx.notify(); }));
    if (["busy_unowned", "other_context"].includes(state)) box.child(div().child("External/other-context dispatcher is not manageable here. No adoption, stop, signal or retry."));
    box.child(button("ocq-dispatch-read", "Read exact retained status only", cx => this.act(owner, "read", cx))).child(button("ocq-dispatch-close", "Back to Queue · does NOT stop dispatch", cx => this.close(owner, cx))); return finish();
  }
}
