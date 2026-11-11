import { div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { exec, compatibility } from "./compatibility.js";
import { connect } from "net";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { composeProof, composeEqual, composeDecode, socketFrames } from "./private-frames.js";

const hash = (s) => createHash("sha256").update(s).digest("hex");
const hex = (s) => /^[0-9a-f]{64}$/.test(s || "");
const quote = (s) => Array.from(JSON.stringify(s), (c) => /[\p{Cc}\p{Default_Ignorable_Code_Point}\p{Zl}\p{Zp}]/u.test(c) ? Array.from({ length: c.length }, (_, n) => `\\u${c.charCodeAt(n).toString(16).padStart(4, "0")}`).join("") : c).join("");

// Concrete general COMMAND admission, independent of projects. Go owns all v1
// canonicalization and filesystem resolution. No shell string or dispatcher.
export class CommandSubmission {
  // Two concrete Queue forms share only private review/journal choreography.
  // Routes/kinds are class constants, never selected by imported request JSON.
  get slot() { return "submission"; }
  get route() { return "command-submit"; }
  get kind() { return "command"; }
  get version() { return 1; }
  get activeFields() { return this.fields; }
  get draftRoute() { return "draft"; }
  get formHint() { return "Every field is explicit. Arguments are a JSON string array, not shell syntax. Memory 0 means zero declared bytes (admission floor still applies). F6 leaves text unchanged; then ←/→ changes fields. G resolves a private draft; no admission."; }
  get warning() { return "TRUSTED EXECUTION: a dispatcher ALREADY RUNNING may execute immediately after admission. This form never starts one. No shell is inserted; /bin/sh or another interpreter runs ONLY if explicitly chosen. Paths are resolved before review, NOT executable/file-content or safety proof."; }
  newInstance() { return new this.constructor(this.view, this.fields); }
  static inputs(view) {
    return [["key", "Immutable idempotency key", false], ["executable", "Explicit executable (bare name or path)", false], ["arguments", "Arguments: JSON array of strings, excluding executable", true], ["cwd", "Explicit working directory", false], ["memory_bytes", "Nonnegative memory bytes: exact decimal integer", false]].map(([key, label, multiline]) => {
      const state = multiline ? TextareaState.new({ placeholder: label, rows: 4 }) : InputState.new({ placeholder: label });
      state.on("change", (_e, cx) => cx.notify());
      return { key, label, multiline, state };
    });
  }
  constructor(view, fields) { this.view = view; this.fields = fields; this.page = 0; }
  current(owner) { return this.view[this.slot] === this && this.screen && owner.machine === null && this.view.current(owner, this.generation) && owner.spaceID === this.space && owner.folder === this.folder; }
  same(a, b) { return a && b && ["machine", "space_id", "queue_identity"].every((k) => a[k] === b[k]); }
  args() { return [this.t.space_id, this.t.queue_identity]; }
  open(owner, cx) {
    if (owner.machine !== null || !this.view.current(owner, this.view.generation)) return;
    const next = this.newInstance(); this.view[this.slot] = next;
    next.screen = true; next.generation = this.view.generation; next.space = owner.spaceID; next.folder = owner.folder;
    next.boundCurrent = () => next.current(owner);
    for (const f of next.fields) f.state.set_value("");
    owner.handle.focus(); next.action(owner, "load", cx);
  }
  close(owner, cx) { if (!this.current(owner)) return; this.view[this.slot] = this.newInstance(); owner.handle.focus(); cx.notify(); }
  validate(out) {
    if (!out || !["idle", "draft", "prepared", "armed", "attempted", "completed", "cancelled", "blocked", "other_context"].includes(out.state) || (out.state !== "other_context" && !this.same(out.scope, this.t))) throw new Error("Command scope changed");
    if (["prepared", "armed", "attempted", "completed", "cancelled"].includes(out.state) && (!hex(out.id) || !hex(out.payload_sha256))) throw new Error("Command intent unavailable");
    if (out.receipt && (out.state !== "completed" || out.observation !== "matched" || out.receipt.queue_identity !== this.t.queue_identity || out.receipt.job?.kind !== this.kind || !/^job-[0-9a-f]{32}$/.test(out.receipt.job.id) || !/^[1-9][0-9]*$/.test(out.receipt.job.sequence))) throw new Error("Submission receipt mismatch");
    return out;
  }
  async call(action, id) {
    const target = this.t, status = this.status;
    const r = await exec("ocq-herdr", [`${this.route}-${action}`, ...this.args(), ...(id ? [id] : [])], { on: null, timeout: 30000, current: () => this.boundCurrent?.() && this.t === target && this.status === status });
    if (r.code !== 0) throw new Error("Command admission response unavailable");
    return this.validate(JSON.parse(r.stdout));
  }
  async exchange(owner, mode, id, raw, cx) {
    if (!this.current(owner)) throw new Error("Context changed");
    const current = compatibility.transfer(() => this.current(owner));
    const launch = randomBytes(32).toString("hex");
    const worker = exec("ocq-herdr", [`${this.route}-${mode}`, ...this.args(), ...(mode === "preview" ? [id] : []), launch], { on: null, timeout: 25000, current: () => this.current(owner) }).then((r) => r, () => null);
    let socket = null;
    try {
      let entry = null;
      for (let n = 0; n < 100; n++) {
        if (!this.current(owner)) throw new Error("Context changed");
        const r = await exec("ocq-herdr", [`${this.route}-rendezvous`, ...this.args(), launch], { on: null });
        if (!this.current(owner)) throw new Error("Context changed");
        if (r.code === 0) { entry = JSON.parse(r.stdout); break; }
        await cx.sleep(50);
      }
      if (!entry || entry.launch !== launch || !hex(entry.capability) || !Number.isInteger(entry.port) || entry.port < 1 || entry.port > 65535 || !this.current(owner)) throw new Error("Rendezvous unavailable");
      socket = await connect("127.0.0.1", entry.port);
      const frames = socketFrames(socket, 1048576), nonce = randomBytes(32).toString("hex");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(nonce + "\n");
      if (!composeEqual(await frames.line(64), composeProof(entry.capability, nonce, "server"))) throw new Error("Server authentication failed");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(composeProof(entry.capability, nonce, "client") + "\n");
      if (await frames.line(64) !== "READY" || !current()) throw new Error("Authentication/context changed");
      if (mode !== "preview") {
        const b = Buffer.from(raw, "utf8");
        if (!b.length || b.length > 131072 || composeDecode(b) !== raw) throw new Error("Invalid command frame");
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(`${b.length}\n`);
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(raw);
      }
      let review = null;
      if (mode === "receive") {
        if (!/^PREPARED [0-9a-f]{64}$/.test(await frames.line(80))) throw new Error("Preparation uncertain");
      } else {
        const h = await frames.bytes(4), size = h[0] * 16777216 + h[1] * 65536 + h[2] * 256 + h[3];
        if (!size || size > 1048576) throw new Error("Private review exceeds bound");
        review = JSON.parse(composeDecode(await frames.bytes(size)));
        const p = JSON.parse(review.raw);
        if (hash(review.raw) !== review.sha256 || !this.same(p.scope, this.t) || typeof p.canonical !== "string" || hash(p.canonical) !== review.canonical_sha256 || !Array.isArray(review.fields) || !review.fields.length || review.fields.some((f) => typeof f.label !== "string" || typeof f.value !== "string")) throw new Error("Exact private request mismatch");
      }
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !current()) throw new Error("Exchange outcome unavailable");
      const status = this.validate(JSON.parse(done.stdout));
      if (mode === "receive" && status.payload_sha256 !== hash(raw) || mode === "preview" && (status.id !== id || status.payload_sha256 !== review.sha256)) throw new Error("Exact retained digest mismatch");
      return { status, review };
    } finally { if (socket) socket.close(); await worker; }
  }
  setReview(review, retained) {
    const pages = [];
    for (const f of review.fields) {
      const tokens = quote(f.value).match(/\\(?:u[0-9a-fA-F]{4}|["\\/bfnrt])|[\s\S]/gu) || [];
      for (let start = 0; start < tokens.length;) {
        let end = start, length = 0;
        while (end < tokens.length && length + tokens[end].length <= 160) length += tokens[end++].length;
        pages.push(`${f.label}${start ? " (continued)" : ""}:\n${tokens.slice(start, end).join("")}`); start = end;
      }
    }
    this.review = { ...review, pages, retained, openedThrough: 0 }; this.page = 0; this.confirm = false;
  }
  last() { const r = this.review; return r && this.page === r.pages.length - 1 && r.openedThrough === this.page; }
  draftInput() {
    const d = Object.fromEntries(this.fields.map((f) => [f.key, f.state.value()]));
    d.arguments = JSON.parse(d.arguments);
    if (!Array.isArray(d.arguments) || d.arguments.some((a) => typeof a !== "string") || !/^(0|[1-9][0-9]*)$/.test(d.memory_bytes) || Object.values(d).some((v) => typeof v === "string" && composeDecode(Buffer.from(v)) !== v) || d.arguments.some((a) => composeDecode(Buffer.from(a)) !== a)) throw new Error("Invalid fields");
    return JSON.stringify(d);
  }
  action(owner, action, cx) {
    if (this.working || !this.current(owner)) return;
    if (action === "status" && !this.t) action = "load";
    const id = this.status?.id, state = this.status?.state;
    if (action === "draft" && (state !== "idle" || this.review)) return;
    if (action === "receive" && (state !== "idle" || !this.review || this.review.retained || !this.last())) return;
    if (action === "preview" && !["prepared", "armed", "attempted", "completed", "cancelled"].includes(state)) return;
    if (action === "cancel" && (!["prepared", "armed"].includes(state) || !this.review?.retained)) return;
    if (action === "reconcile" && !["attempted", "completed"].includes(state)) return;
    if (action === "retire" && !["completed", "cancelled"].includes(state)) return;
    let raw = null;
    if (action === "draft") {
      try {
        raw = this.draftInput();
      } catch (_e) { this.error = this.formHint; cx.notify(); return; }
    }
    if (action === "receive") raw = this.review.raw;
    this.working = true; this.error = null; this.confirm = false;
    cx.spawn(async (cx) => {
      try {
        if (action === "load") {
          const r = await exec("ocq-herdr", [`${this.route}-context`, this.space], { on: null });
          if (!this.current(owner)) return;
          const t = JSON.parse(r.stdout);
          if (r.code !== 0 || t.machine !== "local" || t.space_id !== this.space || !hex(t.queue_identity)) throw new Error("Queue unavailable");
          this.t = Object.freeze(t);
        }
        if (!this.current(owner)) return;
        if (["draft", "receive", "preview"].includes(action)) {
          const out = await this.exchange(owner, action === "draft" ? this.draftRoute : action, id, raw, cx);
          if (!this.current(owner)) return;
          if (action !== "draft") this.status = out.status;
          if (out.review) this.setReview(out.review, action === "preview");
          else { this.review = null; this.page = 0; }
        } else {
          const out = await this.call(action === "load" ? "status" : action, action === "status" || action === "load" ? null : id);
          if (!this.current(owner)) return;
          this.status = out; this.review = null; this.page = 0;
        }
      } catch (_e) {
        if (this.current(owner)) { this.review = null; this.page = 0; if (action !== "draft") this.status = null; this.error = action === "draft" ? "Draft unavailable. Check explicit fields, core path/profile/permission rules and size. Nothing was prepared." : "Outcome unavailable. Refresh the retained journal; never retry admission automatically."; }
      } finally { if (this.current(owner)) { this.working = false; owner.handle.focus(); cx.notify(); } }
    }); cx.notify();
  }
  send(owner, cx) {
    const preview = this.review, id = this.status?.id;
    if (this.working || !this.confirm || !preview?.retained || !this.last() || this.status?.state !== "prepared" || !this.current(owner)) return;
    this.working = true; this.confirm = false; this.error = null;
    cx.spawn(async (cx) => {
      let claimLaunched = false;
      try {
        const exact = await this.exchange(owner, "preview", id, null, cx);
        if (!this.current(owner) || this.review !== preview || exact.status.state !== "prepared" || exact.review.raw !== preview.raw) throw new Error("Exact retained bytes changed");
        claimLaunched = true; this.status = null; this.review = null;
        const arm = await this.call("arm", id);
        if (!this.current(owner)) return;
        this.status = arm;
        if (arm.id !== id || arm.state !== "armed" || arm.payload_sha256 !== preview.sha256) throw new Error("Claim not acknowledged");
        if (!this.current(owner)) return;
        this.status = null;
        const out = await this.call("attempt", id); // One admission call only.
        if (!this.current(owner)) return;
        this.status = out;
      } catch (_e) { if (this.current(owner)) { this.status = null; this.review = null; this.error = claimLaunched ? "Claim/admission outcome uncertain. Refresh and check the exact receipt; never resend." : "Exact retained review unavailable. Nothing was claimed; refresh and review again."; } }
      finally { if (this.current(owner)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  advance(delta, owner, cx) {
    if (this.working || this.confirm || !this.current(owner)) return;
    if (this.review) {
      const r = this.review, next = Math.max(0, Math.min(r.pages.length - 1, this.page + delta));
      if (delta === 1 && this.page === r.openedThrough && next === this.page + 1) r.openedThrough = next;
      this.page = next;
    } else if (this.status?.state === "idle") this.page = Math.max(0, Math.min(this.activeFields.length - 1, this.page + delta));
    owner.handle.focus(); cx.notify();
  }
  key(e, owner, cx) {
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    if (e.key === "f6") { owner.handle.focus(); cx.stop_propagation(); return; }
    // Native entities survive a context reset; their earlier focus/blur events
    // must not turn ordinary typing into command shortcuts in the new view.
    if (!owner.handle.is_focused()) return;
    if (e.key === "escape") { if (this.confirm) this.confirm = false; else this.close(owner, cx); }
    else if (!e.is_held && e.key === "enter" && !this.working && !this.review && this.status?.state === "idle" && this.current(owner)) this.activeFields[this.page]?.state.focus();
    else if (!e.is_held && ["left", "right"].includes(e.key)) this.advance(e.key === "right" ? 1 : -1, owner, cx);
    else if (!e.is_held && e.key === "s" && this.last() && this.review?.retained && this.status?.state === "prepared" && !this.working) this.confirm = true;
    else if (!e.is_held && e.key === "y") this.send(owner, cx);
    else if (!e.is_held && ({ r: "status", g: "draft", p: "receive", v: "preview", c: "cancel", k: "reconcile", t: "retire" })[e.key]) this.action(owner, ({ r: "status", g: "draft", p: "receive", v: "preview", c: "cancel", k: "reconcile", t: "retire" })[e.key], cx);
    cx.stop_propagation(); cx.notify();
  }
  render(owner, cx, t, button) {
    const s = this.status, r = this.review, f = this.activeFields[this.page];
    return v_flex().id("ocq-command-submit").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx))
      .child(h_flex().gap(8).child(div().font_semibold().child(`Submit general ${this.kind} · local only`)).child(button("ocq-command-close", "Close", (cx) => this.close(owner, cx))))
      .child(div().text_color(t.blocked).child(this.warning))
      .child(div().text_size(12).child(`Machine: this computer\nSpace: ${this.space}\nQueue: ${this.t?.queue_identity || "checking…"}\nThis action adds no project association. A matching global key recovers its original job unchanged. Key and resolved v${this.version} request are immutable once prepared.`))
      .child(div().child(this.working ? "Checking exact local context/journal…" : `Retained state: ${s?.state || "unavailable — refresh only"}${s?.observation ? " · " + s.observation : ""}\nIntent: ${s?.id || "none"}`))
      .when(Boolean(this.error), (e) => e.child(div().text_color(t.ansi1).child(this.error)))
      .when(Boolean(s?.state === "other_context"), (e) => e.child(div().text_color(t.blocked).child(`Retained request belongs to another exact space: ${s.scope.space_id}. Return there; no adoption or resend here.`)))
      .when(Boolean(s?.receipt), (e) => e.child(div().child(`Exact receipt matched: ${s.receipt?.job.id}\nSequence: ${s.receipt?.job.sequence}\nCurrent state: ${s.receipt?.job.state} · validation: ${s.receipt?.job.validation}\nReceipt match proves admission, NOT successful execution.`)))
      .when(!this.working && Boolean(this.t), (e) => e.child(button("ocq-command-status", "Refresh journal (no retry)", (cx) => this.action(owner, "status", cx))))
      .when(!this.working && s?.state === "idle" && !r, (e) => e.child(v_flex().gap(8)
        .when(this.kind === "agent", (e) => e.child(this.modeControls(owner, cx, button)))
        .child(div().font_semibold().child(`Field ${this.page + 1}/${this.activeFields.length} · ${f?.label}`))
        .child((f?.multiline ? Textarea.new(f.state).h(120) : Input.new(f.state).h(38)).id(`ocq-command-${f?.key}`).accessibility_label(f?.label).px(8).border_1().border_color(t.border).text_color(t.foreground))
        .child(h_flex().gap(8).child(button("ocq-command-field-prev", "Previous field", (cx) => this.advance(-1, owner, cx))).child(button("ocq-command-field-next", "Next field", (cx) => this.advance(1, owner, cx))))
        .child(div().text_size(12).text_color(t.muted).child(this.formHint))
        .when(this.page === this.activeFields.length - 1, (e) => e.child(button("ocq-command-build", "Build exact private draft (no admission)", (cx) => this.action(owner, "draft", cx))))))
      .when(Boolean(r), (e) => e.child(v_flex().gap(8)
        .child(div().font_semibold().child(`${r?.retained ? "RETAINED" : "DRAFT"} exact review · ${this.page + 1}/${r?.pages.length}`))
        .child(div().text_size(12).text_color(t.muted).child("Quoted controls/invisible characters and continuations preserve every field. Opening pages is not proof of comprehension. Resolved paths may change; neither executable suitability nor contents are snapshotted."))
        .child(div().child(r?.pages[this.page]))
        .when(!this.confirm && !this.working, (e) => e.child(h_flex().gap(8).child(button("ocq-command-review-prev", "Previous", (cx) => this.advance(-1, owner, cx))).child(button("ocq-command-review-next", "Next", (cx) => this.advance(1, owner, cx)))))
        .when(!this.working && this.last() && !r?.retained, (e) => e.child(button("ocq-command-prepare", "Prepare these exact bytes (no admission)", (cx) => this.action(owner, "receive", cx))))
        .when(!this.working && this.last() && r?.retained && s?.state === "prepared" && !this.confirm, (e) => e.child(button("ocq-command-review-admit", "Review admission…", (cx) => { if (this.last() && this.current(owner)) this.confirm = true; cx.notify(); })))
        .when(!this.working && this.confirm, (e) => e.child(v_flex().gap(8).child(div().text_color(t.blocked).child(`Admit this EXACT retained ${this.kind} once? It may execute immediately under an existing dispatcher. A lost response stays uncertain; no automatic retry.`))
          .child(button("ocq-command-admit", "Confirm one admission", (cx) => this.send(owner, cx)))))))
      .when(!this.working && ["prepared", "armed", "attempted", "completed", "cancelled"].includes(s?.state) && !r, (e) => e.child(button("ocq-command-preview", `Review exact retained ${this.kind}`, (cx) => this.action(owner, "preview", cx))))
      .when(!this.working && ["prepared", "armed"].includes(s?.state) && Boolean(r?.retained), (e) => e.child(button("ocq-command-cancel", "Cancel unattempted admission", (cx) => this.action(owner, "cancel", cx))))
      .when(!this.working && ["attempted", "completed"].includes(s?.state), (e) => e.child(button("ocq-command-reconcile", "Check exact receipt (no retry)", (cx) => this.action(owner, "reconcile", cx))))
      .when(!this.working && ["completed", "cancelled"].includes(s?.state), (e) => e.child(button("ocq-command-retire", "Retire resolved admission intent", (cx) => this.action(owner, "retire", cx))))
      .child(div().text_size(12).text_color(t.muted).child("Enter edit field · F6 leave input · ←/→ fields/pages · G draft · P prepare · V retained review · S review admission · Y confirm · R refresh · K receipt · C cancel · T retire · Esc close"));
  }
}
