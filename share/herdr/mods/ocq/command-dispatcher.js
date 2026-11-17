import { div } from "gpui-kit";
import { v_flex, h_flex, ScrollHandle } from "gpui-base";
import { run, focused, spaces, panes, terminals } from "herdr";
import { bridgeCommand, compatibility, enabledButton } from "./compatibility.js";

const hex = s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s);
const warning = "Commands only · 2 slots · 6 GiB floor. Start may execute queued commands immediately. Unknown demand holds capacity.";
const closure = "Keep the helper terminal open until actual exit. Closing it can force-kill it. Drain requests one TERM; missing exit proof never permits retry or capacity release.";

export class CommandDispatcher {
  constructor(view) {
    this.view = view;
    this.scroll = null; // Placeholders own no native handle; open allocates it.
    try { this.attempts = JSON.parse(localStorage.getItem("dispatcher-attempts-v1") || "{}"); }
    catch (_) { this.attempts = {}; this.storageError = true; }
    if (!this.attempts || typeof this.attempts !== "object" || Array.isArray(this.attempts)) this.storageError = true;
  }
  dispose() {
    if (this.disposed) return;
    // Retained status/reviews/attempts are evidence, not a reusable controller.
    this.disposed = true; this.generation = (this.generation || 0) + 1; this.screen = false;
    this.targets = []; this.owner = null; this.view = null;
    const scroll = this.scroll; this.scroll = null;
    if (scroll) scroll.release();
  }
  current(owner) {
    return !this.disposed && this.view.dispatcher === this && this.screen && owner.machine === null &&
      this.view.current(owner, this.generation) && owner.spaceID === this.space && owner.folder === this.folder &&
      owner.updates.current() && owner.updates.connection.state === "connected" &&
      owner.updates.connectionID === this.connectionID && owner.updates.connectionGeneration === this.connectionGeneration;
  }
  scopeSame(a, b) { return a && b && ["machine", "space_id", "queue_identity"].every(k => a[k] === b[k]); }
  targetSame(a, b) { return a && b && this.scopeSame(a.scope, b.scope) && ["pane", "terminal", "tab", "space_folder", "hc_binding_sha256"].every(k => a[k] === b[k]); }
  localTarget() { return this.t && this.scopeSame(this.t.scope, this.scope) && this.t.space_folder === this.folder; }
  candidates() {
    const s = spaces().find(s => s.active && s.machine === null && s.id === this.space && s.folder === this.folder);
    const digest = compatibility.hc?.approvedDigest();
    if (!s || !this.scope || !digest) return [];
    return panes().filter(p => p.terminal && !p.mod && s.tabs.some(t => t.id === p.tab))
      .filter(p => terminals().some(t => t.address === p.terminal && t.state === "live" && t.labels?.["herdr.space"] === s.id && t.labels?.["herdr.tab"] === p.tab))
      .map(p => ({ scope: this.scope, pane: p.pane, terminal: p.terminal, tab: p.tab, space_folder: s.folder, hc_binding_sha256: digest }));
  }
  anchorValid() { return this.localTarget() && this.candidates().some(t => this.targetSame(t, this.t)); }
  focusedTarget() { const f = focused(), t = this.t; return f && t && f.space === t.scope.space_id && f.tab === t.tab && f.pane === t.pane && f.terminal === t.terminal && f.cwd === t.space_folder; }
  capture() {
    return { target: this.t, id: this.status.id, digest: this.status.intent_sha256,
      hc: compatibility.hc.approved, context: compatibility.hc.context() };
  }
  operationCurrent(owner, op, status = this.status) {
    return this.current(owner) && compatibility.nativeReady() && op.hc === compatibility.hc.approved &&
      op.target.hc_binding_sha256 === compatibility.hc.approvedDigest() &&
      op.context === compatibility.hc.context() && this.targetSame(this.t, op.target) &&
      [this.status, status].every(s => s?.id === op.id && s.intent_sha256 === op.digest && this.targetSame(s.target, op.target));
  }
  helperTerminal(s) {
    const o = s?.owner, argv = [s?.executable, "dispatcher-helper", s?.id];
    if (!o || s.target.hc_binding_sha256 !== compatibility.hc?.approvedDigest()) return null;
    return terminals().find(t => t.address === o.terminal && t.state === "live" && t.pid === o.pid &&
      (!t.foreground || t.foreground.pid === o.pid && JSON.stringify(t.foreground.argv) === JSON.stringify(argv)) &&
      JSON.stringify(t.argv) === JSON.stringify(argv) && t.cwd === this.folder &&
      t.labels?.["herdr.space"] === this.space && t.labels?.["herdr.tab"] === this.t.tab &&
      t.labels?.["ocq.hc_binding"] === s.target.hc_binding_sha256 && t.labels?.["ocq.dispatcher"] === s.id);
  }
  nativeOwner(s) {
    const t = this.helperTerminal(s);
    const p = t && panes().find(p => p.terminal === t.address && p.tab === this.t.tab && p.pane !== this.t.pane);
    if (!p || s.helper_pane && p.pane !== s.helper_pane) return null;
    return { terminal: t.address, pane: p.pane, tab: p.tab, pid: String(s.owner.pid), start: s.owner.start };
  }
  args() { const t = this.t; return [t.scope.space_id, t.scope.queue_identity, t.pane, t.terminal, t.tab, t.space_folder]; }
  validate(s) {
    if (!s || !this.scopeSame(s.target?.scope, this.scope) && s.state !== "other_context" ||
        s.commands_only !== true || s.slots !== 2 || s.memory_floor_bytes !== "6442450944" ||
        !["none", "prepared", "armed", "cancelled", "unknown", "born", "bound", "ready", "drain_prepared", "drain_armed", "draining", "exit_recorded", "stopped", "retired", "busy_unowned", "other_context"].includes(s.state)) throw Error("Dispatcher context/policy unavailable");
    if (s.id && (!hex(s.id) || !hex(s.intent_sha256))) throw Error("Dispatcher intent unavailable");
    if (s.owner && (!Number.isSafeInteger(s.owner.pid) || s.owner.pid < 1 || !/^[1-9][0-9]*$/.test(s.owner.start) || typeof s.owner.boot !== "string")) throw Error("Dispatcher owner unavailable");
    return s;
  }
  async call(action, current, extra = []) {
    const args = action === "read" ? [this.scope.space_id, this.scope.queue_identity] :
      [...this.args(), ...(!["status", "prepare"].includes(action) ? [this.status.id] : []), ...extra];
    const r = await bridgeCommand([`dispatcher-${action}`, ...args],
      { on: null, current, hcBinding: compatibility.hc?.approvedDigest() });
    if (r.code !== 0) throw Error("Dispatcher reply unavailable/refused. Read the exact record; do not retry launch or signal.");
    return this.validate(JSON.parse(r.stdout));
  }
  open(owner, cx) {
    if (this.disposed || this.view.dispatcher !== this || owner.machine !== null || !this.view.current(owner, this.view.generation)) return;
    const view = this.view, d = view.replaceDispatcher();
    d.scroll = ScrollHandle.new();
    d.owner = owner; d.screen = true; d.generation = view.generation; d.space = owner.spaceID; d.folder = owner.folder;
    d.connectionID = owner.updates.connectionID; d.connectionGeneration = owner.updates.connectionGeneration;
    owner.handle.focus(); d.act(owner, "load", cx);
  }
  close(owner, cx) { if (!this.current(owner)) return; this.view.replaceDispatcher(); owner.handle.focus(); cx.notify(); }
  attempted(kind) { return !!this.attempts?.[JSON.stringify([kind, this.space, this.scope?.queue_identity, this.status?.id, this.status?.intent_sha256, this.t?.hc_binding_sha256])]; }
  async mark(kind) {
    if (this.storageError || this.attempted(kind)) throw Error("Attempt already retained or private storage unavailable");
    this.attempts[JSON.stringify([kind, this.space, this.scope.queue_identity, this.status.id, this.status.intent_sha256, this.t.hc_binding_sha256])] = "uncertain";
    localStorage.setItem("dispatcher-attempts-v1", JSON.stringify(this.attempts));
    await localStorage.flush();
  }
  reviewAction(kind, cx) {
    if (!compatibility.nativeReady() || this.working || !this.localTarget() ||
        this.t.hc_binding_sha256 !== compatibility.hc.approvedDigest()) return;
    this.review = { kind, operation: this.capture(), status: this.status,
      pages: this.owner.workOperator.pagesFor({ dispatcher: this.status, hc_binding: compatibility.hc.approved.value }),
      page: 0, seen: new Set() };
    this.confirm = null; cx.notify();
  }
  reviewCurrent(kind) {
    const r = this.review;
    return r && r.kind === kind && r.status === this.status && this.operationCurrent(this.owner, r.operation) && r.seen.size === r.pages.length;
  }
  act(owner, action, cx) {
    if (this.working || !this.current(owner)) return;
    const op = this.t && this.status?.id && !["read", "load"].includes(action) ? this.capture() : null;
    const current = () => op ? this.operationCurrent(owner, op) : this.current(owner);
    if (action === "prepare" && (!this.anchorValid() || this.status?.state !== "none")) return;
    if (action === "drain-prepare" && !this.nativeOwner(this.status)) return;
    if (action === "bind" && (!this.launch || this.launch.bound || !this.operationCurrent(owner, this.launch.operation) ||
        !this.anchorValid() || !this.nativeOwner(this.status))) return;
    if (action === "retire" && (!this.reviewCurrent("retire") || !this.status.can_retire)) return;
    this.working = true; this.error = null; this.confirm = null; this.review = null;
    cx.spawn(async cx => {
      try {
        if (!current()) return;
        if (action === "load") {
          const r = await bridgeCommand(["dispatcher-context", this.space], { on: null });
          if (!current()) return;
          if (r.code !== 0) throw Error("Existing queue unavailable");
          this.scope = JSON.parse(r.stdout);
        }
        let extra = [];
        if (action === "bind") {
          const n = this.nativeOwner(this.status);
          await this.mark("bind");
          if (!current() || !n || !this.anchorValid() || JSON.stringify(n) !== JSON.stringify(this.nativeOwner(this.status))) return;
          this.launch.bound = true;
          extra = [n.terminal, n.pane, n.tab, n.pid, n.start];
        }
        const s = await this.call(action === "load" ? "read" : action, current, extra);
        if (!current()) return;
        this.status = s;
        if (s.target.pane) this.t = s.target;
        if (action === "retire") { this.t = null; this.launch = null; }
      } catch (e) { if (this.current(owner)) this.error = String(e.message || e); }
      finally {
        this.working = false;
        if (this.current(owner)) {
          if (action === "drain-prepare" && this.status?.state === "drain_prepared") this.reviewAction("drain", cx);
          cx.notify();
        }
      }
    }); cx.notify();
  }
  confirmStart(owner, cx) { this.confirmOperation(owner, "start", cx); }
  confirmDrain(owner, cx) { this.confirmOperation(owner, "drain", cx); }
  confirmOperation(owner, kind, cx) {
    const start = kind === "start";
    if (this.working || this.confirm !== kind || !this.reviewCurrent(kind) || this.storageError || this.attempted(kind) ||
        this.status.state !== (start ? "prepared" : "drain_prepared") || (start ? !this.anchorValid() : !this.nativeOwner(this.status))) return;
    const op = this.review.operation;
    this.working = true; this.confirm = null; this.review = null; this.error = null;
    cx.spawn(async cx => {
      try {
        if (!this.operationCurrent(owner, op)) return;
        await this.mark(kind); // Before either transport; uncertain never resends.
        if (!this.operationCurrent(owner, op) || (start ? !this.anchorValid() : !this.nativeOwner(this.status))) return;
        let s = await this.call(start ? "arm" : "drain-arm", () => this.operationCurrent(owner, op));
        if (!this.operationCurrent(owner, op, s)) return;
        this.status = s;
        if (s.state !== (start ? "armed" : "drain_armed")) throw Error("Exact claim unavailable; no attempt sent.");
        if (start ? !this.anchorValid() : !this.nativeOwner(s)) throw Error("Exact native owner unavailable; no attempt sent.");
        s = await this.call(start ? "attempt" : "drain-attempt", () => this.operationCurrent(owner, op));
        if (!this.operationCurrent(owner, op, s)) return;
        this.status = s;
        // Backend already ran the helper once. No terminal.open/hc run here.
        if (start && s.state === "born" && s.owner) this.launch = { operation: op, presented: false, bound: false };
      } catch (e) { if (this.current(owner)) this.error = String(e.message || e); }
      finally { this.working = false; if (this.current(owner)) cx.notify(); }
    }); cx.notify();
  }
  present(owner, cx) {
    const launch = this.launch;
    if (!launch || launch.presented || !this.operationCurrent(owner, launch.operation) ||
        this.status.state !== "born" || !this.anchorValid() || !this.focusedTarget() || !this.helperTerminal(this.status)) return;
    launch.presented = true; // UI presentation, never another native run.
    try { run("terminal.keep", { terminal: this.status.owner.terminal, place: "right" }); }
    catch (_) { this.error = "Terminal presentation unavailable. Do not launch a replacement."; }
    cx.notify();
  }
  key(e, owner, cx) {
    if (!this.current(owner) || e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    if (e.key === "escape" && !this.working) {
      if (this.review) { this.review = null; this.confirm = null; } else this.close(owner, cx);
    } else if (["down", "up"].includes(e.key) && this.targets?.length) {
      const index = this.targets.findIndex(t => t.id === owner.navigationTarget);
      const next = index < 0 ? (e.key === "down" ? 0 : this.targets.length - 1) :
        (index + (e.key === "down" ? 1 : this.targets.length - 1)) % this.targets.length;
      const target = this.targets[next];
      owner.navigationTarget = target.id; owner.navigationLabel = target.label; target.reveal?.();
    } else if (e.key === "enter" && !e.is_held) this.targets?.find(t => t.id === owner.navigationTarget)?.run(cx);
    else return;
    cx.notify(); cx.stop_propagation();
  }
  render(owner, cx, t, nativeButton) {
    this.targets = [];
    // ScrollHandle indices address direct native children, including HC pages.
    const items = [], box = { child(element) { items.push(element); return this; } };
    const button = (id, text, action) => {
      const guarded = cx => { if (enabledButton(id) && this.current(owner)) action(cx); };
      const previous = owner.actionReveal;
      // Capture this row now; later renders must not reveal a stale list length.
      const index = items.length;
      owner.actionReveal = () => { if (this.current(owner)) this.scroll.scroll_to_item(index); };
      try {
        if (enabledButton(id)) owner.registerAction(id, text, guarded, this.targets);
      } finally { owner.actionReveal = previous; }
      const target = this.targets.find(target => target.id === id);
      if (target) {
        const invoke = target.run;
        target.run = cx => { if (this.current(owner)) invoke(cx); };
      }
      return nativeButton(id, text, cx => target ? target.run(cx) : guarded(cx));
    };
    const s = this.status, local = this.localTarget(), state = s?.state;
    const finish = () => v_flex().id("ocq-command-dispatcher-scroll").size_full().min_h(0).overflow_y_scroll().track_scroll(this.scroll)
      .gap(8).p(12).text_color(t.foreground).bg(t.background).track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx)).children(items);
    box.child(div().font_semibold().child("Commands-only dispatcher"))
      .child(div().text_size(12).child(warning));
    owner.nativeHC.render(cx, t, button, box);
    if (s) box.child(div().child(`State: ${state} · singleton ${s.busy ? "held" : "free"}`))
      .child(div().text_size(12).child(`Operation: ${s.id || "none"}${s.owner ? `\nReturned terminal: ${s.owner.terminal} · PID ${s.owner.pid}` : ""}`));
    if (this.error) box.child(div().text_color(t.blocked).child(this.error));
    if (this.storageError) box.child(div().text_color(t.blocked).child("Private attempt evidence unavailable. Mutations disabled."));
    if (this.working) { box.child(div().child("One explicit action in progress; no uncertain retry.")); return finish(); }
    if (this.review) {
      const r = this.review; r.seen.add(r.page);
      box.child(div().text_color(t.blocked).child(closure))
        .child(div().child(`Exact ${r.kind} review · ${r.page + 1}/${r.pages.length}`)).child(div().child(r.pages[r.page]))
        .child(h_flex().gap(4)
          .when(r.page > 0, e => e.child(button("ocq-dispatch-review-previous", "Previous retained page", cx => { r.page--; cx.notify(); })))
          .when(r.page < r.pages.length - 1, e => e.child(button("ocq-dispatch-review-next", "Next retained page", cx => { r.page++; cx.notify(); }))));
      if (this.reviewCurrent(r.kind)) {
        if (!this.confirm) box.child(button("ocq-dispatch-review-complete", `Review one ${r.kind}`, cx => { this.confirm = r.kind; cx.notify(); }));
        else box.child(button(`ocq-dispatch-confirm-${r.kind}`, r.kind === "start" ? "Confirm one backend START" : r.kind === "drain" ? "Confirm one TERM DRAIN" : "Retire proven lifetime",
          cx => r.kind === "retire" ? this.act(owner, "retire", cx) : this.confirmOperation(owner, r.kind, cx)));
      }
      box.child(button("ocq-dispatch-back-confirm", "Back without action", cx => { this.review = null; this.confirm = null; cx.notify(); }));
      return finish();
    }
    if (state === "none") {
      for (const candidate of this.candidates()) box.child(button(`ocq-dispatch-pane-${candidate.pane}`, `Select anchor ${candidate.pane} · ${candidate.terminal} · ${candidate.tab}`, cx => { this.t = candidate; cx.notify(); }));
      if (this.anchorValid()) box.child(button("ocq-dispatch-prepare", "Prepare commands-only intent", cx => this.act(owner, "prepare", cx)));
    }
    if (state === "prepared" && local && this.anchorValid() && !this.attempted("start"))
      box.child(button("ocq-dispatch-review-start", "Review START", cx => this.reviewAction("start", cx)));
    if (["prepared", "armed"].includes(state) && local) box.child(button("ocq-dispatch-cancel", "Cancel unlaunched intent", cx => this.act(owner, "cancel", cx)));
    if (state === "born" && this.launch && this.operationCurrent(owner, this.launch.operation)) {
      box.child(button("ocq-dispatch-focus-anchor", "Focus original anchor", () => { if (this.anchorValid()) run("pane.focus", { pane: this.t.pane }); }));
      if (!this.launch.presented && this.focusedTarget() && this.helperTerminal(s))
        box.child(button("ocq-dispatch-present", "Present returned helper · no launch", cx => this.present(owner, cx)));
      if (!this.launch.bound && this.nativeOwner(s) && this.anchorValid())
        box.child(button("ocq-dispatch-bind", "Bind exact observed helper pane", cx => this.act(owner, "bind", cx)));
    }
    if (state === "ready" && local && this.nativeOwner(s)) box.child(button("ocq-dispatch-review-drain", "Prepare separate DRAIN", cx => this.act(owner, "drain-prepare", cx)));
    if (state === "drain_prepared" && local && this.nativeOwner(s) && !this.attempted("drain")) box.child(button("ocq-dispatch-review-retained-drain", "Review retained DRAIN", cx => this.reviewAction("drain", cx)));
    if (s?.can_retire && local) box.child(button("ocq-dispatch-review-retire", "Review stopped-only retirement", cx => this.reviewAction("retire", cx)));
    box.child(button("ocq-dispatch-read", "Read exact retained status", cx => this.act(owner, "read", cx)))
      .child(button("ocq-dispatch-close", "Back to Queue · does not stop dispatch", cx => this.close(owner, cx)));
    return finish();
  }
}
