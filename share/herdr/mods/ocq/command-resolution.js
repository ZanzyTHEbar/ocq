import { div } from "gpui-kit";
import { v_flex, Textarea } from "gpui-base";
import { exec, compatibility } from "./compatibility.js";
import { connect } from "net";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { composeProof, composeEqual, composeDecode, socketFrames } from "./private-frames.js";

const hash = (s) => createHash("sha256").update(s).digest("hex");
const hex = (s) => /^[0-9a-f]{64}$/.test(s || "");
const privateString = (s) => Array.from(JSON.stringify(s), (c) => /[\p{Cc}\p{Default_Ignorable_Code_Point}\p{Zl}\p{Zp}]/u.test(c) ? Array.from({ length: c.length }, (_, n) => `\\u${c.charCodeAt(n).toString(16).padStart(4, "0")}`).join("") : c).join("");

export class CommandResolution {
  constructor(view) { this.view = view; }
  target(owner) {
    const d = this.view.detail.data, j = d?.job;
    if (owner.machine !== null || j?.kind !== "command" || !hex(d.queue_identity) || typeof j.sequence !== "string" || !/^[1-9][0-9]*$/.test(j.sequence)) return null;
    return { machine: "local", space_id: owner.spaceID, queue_identity: d.queue_identity, job_id: j.id, sequence: j.sequence };
  }
  same(a, b) { return a && b && ["machine", "space_id", "queue_identity", "job_id", "sequence"].every((k) => a[k] === b[k]); }
  current(owner, t = this.t) { return this.t === t && this.view.resolution === this && this.detail === this.view.detail && this.view.current(owner, this.generation) && this.same(t, this.target(owner)); }
  load(owner, cx) { const t = this.target(owner); this.t = t && Object.freeze(t); this.detail = this.view.detail; this.generation = this.view.generation; this.boundCurrent = () => this.current(owner); if (this.t) this.action(owner, "status", cx); }
  args(t) { return [t.space_id, t.queue_identity, t.job_id, t.sequence]; }
  validate(out, t) {
    if (!this.same(out.target, t) || !["none", "prepared", "armed", "uncertain", "refused", "blocked", "changed", "ineligible", "resolved_observed"].includes(out.state)) throw new Error("Resolution target changed");
    if (out.intent_id && (!hex(out.intent_id) || !hex(out.payload_sha256) || !hex(out.evidence_sha256) || !this.same(out.scope, t) || out.scope.displayed_kind !== "command" || out.scope.displayed_state !== "unknown" || !hex(out.scope.reason_sha256))) throw new Error("Retained resolution mismatch");
    const o = out.observation;
    if (o && (o.queue_identity !== t.queue_identity || o.job?.id !== t.job_id || o.job?.sequence !== t.sequence || !hex(o.evidence_sha256))) throw new Error("Resolution observation mismatch");
    if (out.state === "resolved_observed" && (o?.job?.kind !== "command" || o.job.state !== "resolved" || o.job.outcome !== "operator_reconciled" || o.completion?.id !== o.job.completion_event_id || o.completion?.job_id !== t.job_id || o.completion.state !== "resolved" || o.completion.outcome !== "operator_reconciled" || o.completion.validation !== o.job.validation || o.evidence_sha256 !== out.evidence_sha256)) throw new Error("Resolution not established");
    return out;
  }
  async call(action, t, digest) {
    const r = await exec("ocq-herdr", [`command-resolve-${action}`, ...this.args(t), ...(digest ? [digest] : [])], { on: null, current: () => this.boundCurrent?.() && this.t === t });
    if (r.code !== 0) throw new Error("Resolution journal unavailable");
    return this.validate(JSON.parse(r.stdout), t);
  }
  apply(out, owner, cx) {
    this.status = out;
    if (out.observation) this.view.noteHelperObservation(owner, this.generation, this.detail, out.observation, cx);
  }

  async exchange(owner, mode, t, digest, raw, cx) {
    if (!this.current(owner, t)) throw new Error("Context changed");
    const current = compatibility.transfer(() => this.current(owner, t));
    const launch = randomBytes(32).toString("hex");
    const args = mode === "receive" ? ["command-resolve-receive", ...this.args(t), launch] : ["command-resolve-preview", ...this.args(t), digest, launch];
    // Only IDs enter argv. Evidence bytes are never sent to exec or stdout.
    if (!this.current(owner, t)) throw new Error("Context changed");
    const worker = exec("ocq-herdr", args, { on: null, timeout: 25000, current: () => this.current(owner, t) }).then((r) => r, () => null);
    let socket = null;
    try {
      let entry = null;
      for (let n = 0; n < 100; n++) {
        if (!this.current(owner, t)) throw new Error("Context changed");
        const r = await exec("ocq-herdr", ["command-resolve-rendezvous", ...this.args(t), launch], { on: null });
        if (r.code === 0) { entry = JSON.parse(r.stdout); break; }
        await cx.sleep(50);
      }
      if (!entry || entry.launch !== launch || !hex(entry.capability) || !Number.isInteger(entry.port) || entry.port < 1 || entry.port > 65535 || !this.current(owner, t)) throw new Error("Rendezvous unavailable");
      socket = await connect("127.0.0.1", entry.port);
      const frames = socketFrames(socket), nonce = randomBytes(32).toString("hex");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(nonce + "\n");
      if (!composeEqual(await frames.line(64), composeProof(entry.capability, nonce, "server"))) throw new Error("Server authentication failed");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(composeProof(entry.capability, nonce, "client") + "\n");
      if (await frames.line(64) !== "READY" || !current()) throw new Error("Authentication/context changed");
      let payload = null;
      if (mode === "receive") {
        const b = Buffer.from(raw, "utf8");
        if (!b.length || b.length > 131072 || composeDecode(b) !== raw) throw new Error("Invalid evidence encoding");
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(`${b.length}\n`);
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(raw);
        if (!/^PREPARED [0-9a-f]{64}$/.test(await frames.line(80))) throw new Error("Preparation uncertain");
      } else {
        const h = await frames.bytes(4), size = h[0] * 16777216 + h[1] * 65536 + h[2] * 256 + h[3];
        if (!size || size > 131072) throw new Error("Invalid private preview");
        const exact = composeDecode(await frames.bytes(size));
        if (hash(exact) !== digest) throw new Error("Retained bytes changed");
        payload = JSON.parse(exact);
        if (!this.same(payload.scope, t) || payload.scope.displayed_kind !== "command" || payload.scope.displayed_state !== "unknown" || hash(payload.displayed_reason) !== payload.scope.reason_sha256 || !payload.evidence?.trim()) throw new Error("Private review mismatch");
      }
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !current()) throw new Error("Exchange uncertain");
      const status = this.validate(JSON.parse(done.stdout), t);
      if (mode === "receive" && status.payload_sha256 !== hash(raw) || mode === "preview" && (status.payload_sha256 !== digest || hash(payload.evidence) !== status.evidence_sha256)) throw new Error("Exact receipt mismatch");
      return { status, payload };
    } finally { if (socket) socket.close(); await worker; }
  }

  action(owner, action, cx) {
    const t = this.t, digest = this.status?.payload_sha256;
    if (this.working || !this.current(owner, t)) return;
    if (action === "prepare" && (this.status?.state !== "none" || this.detail.data.job.state !== "unknown")) return;
    if (action === "preview" && (!this.status?.intent_id || !hex(digest))) return;
    let raw = null;
    if (action === "prepare") {
      const j = this.detail.data.job, evidence = this.view.resolveInput.value();
      if (!evidence.trim() || Buffer.from(evidence).length > 16384 || Buffer.from(j.reason).length > 16384 || evidence.includes("\0") || composeDecode(Buffer.from(evidence)) !== evidence) { this.error = "Enter nonempty exact evidence (at most 16 KiB); the complete stored reason must also fit. Nothing was prepared."; cx.notify(); return; }
      raw = JSON.stringify({ scope: { ...t, displayed_kind: "command", displayed_state: "unknown", reason_sha256: hash(j.reason) }, displayed_reason: j.reason, evidence });
    }
    this.working = true; this.error = null; this.confirm = false; this.preview = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t)) return;
        if (action === "status") { const out = await this.call("status", t); if (this.current(owner, t)) this.apply(out, owner, cx); }
        else {
          if (!this.current(owner, t)) return;
          const out = await this.exchange(owner, action === "prepare" ? "receive" : "preview", t, digest, raw, cx);
          if (!this.current(owner, t)) return;
          this.apply(out.status, owner, cx);
          if (action === "preview") { this.preview = { payload: out.payload, digest }; this.confirm = true; owner.handle.focus(); }
          if (action === "prepare") this.view.resolveInput.set_value("");
        }
      } catch (_e) { if (this.current(owner, t)) { this.status = null; this.error = "Preparation/review outcome unavailable. Check the retained journal only; do not resend evidence or resolution."; } }
      finally { this.working = false; if (this.current(owner, t)) cx.notify(); }
    }); cx.notify();
  }
  send(owner, cx) {
    const t = this.t, digest = this.status?.payload_sha256, preview = this.preview;
    if (this.working || !this.confirm || !preview || preview.digest !== digest || !hex(digest) || this.status?.state !== "prepared" || !this.current(owner, t)) return;
    this.working = true; this.status = { ...this.status, state: "uncertain" };
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t) || this.preview !== preview) return;
        const arm = await this.call("arm", t, digest);
        if (!this.current(owner, t) || this.preview !== preview) return;
        if (arm.state === "armed" && arm.payload_sha256 === digest) {
          if (!this.current(owner, t)) return;
          await this.call("attempt", t, digest);
        } else if (!["resolved_observed", "changed"].includes(arm.state)) throw new Error("Claim unavailable");
      } catch (_e) { if (this.current(owner, t)) this.error = "Claim or resolution response unavailable. Checking only; never retry."; }
      finally {
        if (this.current(owner, t)) {
          try { const out = await this.call("status", t); if (this.current(owner, t)) { this.apply(out, owner, cx); this.error = null; } }
          catch (_e) { if (this.current(owner, t)) { this.status = null; this.error = "Exact job/completion/evidence-digest read-back unavailable. Outcome uncertain; Check only."; } }
        }
        this.working = false; this.confirm = false; this.preview = null;
        if (this.current(owner, t)) cx.notify();
      }
    }); cx.notify();
  }
  confirmation(owner, cx, t, button) {
    const p = this.preview.payload, s = p.scope;
    return v_flex().id("ocq-command-resolution-confirm").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.view.key(e, owner, cx))
      .child(div().font_semibold().child("Private operator assertion · resolve UNKNOWN command?"))
      .child(v_flex().flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child("Records your operator assertion, resolves eligible ownership and may release capacity. NOT successful execution or a validated deliverable. Does NOT kill processes. The dispatcher must be stopped; exact process/group exit must be established by the existing core checks. No agent reconciliation."))
        .child(div().child(`Machine: local only\nSpace: ${s.space_id}\nQueue: ${s.queue_identity}\nJob: ${s.job_id}\nReceipt: ${s.sequence}\nDisplayed kind/state: command / unknown\nFull stored reason (exact JSON string): ${privateString(p.displayed_reason)}\nEvidence (exact private JSON string): ${privateString(p.evidence)}\nEvidence SHA-256: ${hash(p.evidence)}\nPayload SHA-256: ${this.preview.digest}`)))
      .child(v_flex().flex_none().gap(4)
        .child(div().text_size(12).text_color(t.muted).child("One durable claim and one attempt. Already-launched calls may finish after leaving. Unknown outcomes require read-only investigation, never automatic retry."))
        .when(!this.working && this.status?.state === "prepared", (e) => e.child(button("ocq-resolve-once", "Record assertion and resolve once · R", (cx) => this.send(owner, cx))))
        .when(!this.working, (e) => e.child(button("ocq-resolve-close-private", "Close private review without resolving", (cx) => { this.confirm = false; this.preview = null; cx.notify(); })))
        .when(Boolean(this.working), (e) => e.child(div().child("Claiming once and reading exact outcome…"))));
  }
  render(content, owner, cx, t, action) {
    const j = this.view.detail.data?.job;
    // A terminal job alone cannot establish the retained private assertion.
    // Keep read-only recovery reachable while its journal read is unavailable.
    if (j?.kind !== "command" || j.state !== "unknown" && !this.status?.intent_id && !this.working && !this.error) return;
    content.child(div().font_semibold().child("UNKNOWN command · operator resolution"));
    if (owner.machine !== null) { content.child(div().text_color(t.muted).child("Resolution disabled on selected remote machines. Use that machine's local UI. No agent reconciliation or process stopping.")); return; }
    if (!this.t) { content.child(div().child("Exact queue identity unavailable; resolution disabled.")); return; }
    const labels = { none: "No resolution intent. Enter private evidence, then prepare and review exact retained bytes.", prepared: "Private evidence retained, not confirmed. Explicit private review is required.", armed: "Claim retained. A call may be delayed. Check only; never resend.", uncertain: "Outcome uncertain. Check only; never retry.", refused: this.status?.refusal === "dispatcher_lock_unavailable" ? "Last attempt refused: dispatcher lock held or unavailable. No dispatcher was stopped or adopted. This attempt is consumed; check only." : "Last attempt refused: exact process/group exit was not established. No process was killed. This attempt is consumed; check only.", changed: "Stored job/observation changed. No matching operator resolution claimed. Investigate the actual outcome below.", blocked: "Journal blocked or owned by another space. Return to the original context; do not retry.", resolved_observed: "Resolved operator assertion, matching evidence digest and immutable completion observed. NOT execution success or validated delivery; caller causality is not established.", ineligible: "Not an UNKNOWN command. Resolution disabled." };
    content.child(div().text_color(t.muted).child(this.working ? "Reading retained resolution…" : labels[this.status?.state] || "Observation unavailable; mutation disabled. Check only."));
    if (this.status) content.child(div().text_size(12).text_color(t.muted).child("Resolution status/job observation time: not recorded. Journal evidence does not update the selected detail or its read timestamp."));
    if (this.error) content.child(div().text_color(t.ansi1).child(this.error));
    if (this.status?.evidence_sha256) content.child(div().text_size(12).text_color(t.muted).child(`Private evidence SHA-256: ${this.status.evidence_sha256}`));
    if (!this.working && this.status?.state === "none" && j.state === "unknown") content
      .child(Textarea.new(this.view.resolveInput).id("ocq-resolve-evidence").accessibility_label("Private operator resolution evidence").h(100).px(8).border_1().border_color(t.border).text_color(t.foreground))
      .child(action("ocq-resolve-prepare", "Prepare private evidence for review…", (cx) => this.action(owner, "prepare", cx)));
    if (!this.working && this.status?.intent_id && this.status?.state !== "blocked") content.child(action("ocq-resolve-preview", "Review exact private retained evidence…", (cx) => this.action(owner, "preview", cx)));
    if (!this.working) content.child(action("ocq-resolve-check", "Check exact job, completion and evidence digest (read-only)", (cx) => this.action(owner, "status", cx)));
  }
}
