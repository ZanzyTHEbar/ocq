import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { exec } from "./compatibility.js";

const hex = (s) => /^[0-9a-f]{64}$/.test(s || "");
const admitted = (s) => ["launching", "running", "unknown", "finished"].includes(s);

// Stored status never calls the live API. Only an explicit claimed attempt can
// enter the core observer. A terminal job without our operation receipt is not
// evidence that this call ran; an operation receipt is not agent success.
export class AgentReconciliation {
  constructor(view) { this.view = view; }
  target(owner) {
    const d = this.view.detail.data, j = d?.job;
    if (owner.machine !== null || j?.kind !== "agent" || !hex(d.queue_identity) || typeof j.sequence !== "string" || !/^[1-9][0-9]*$/.test(j.sequence) || !j.recorded_session_id || !j.recorded_worktree) return null;
    return { machine: "local", space_id: owner.spaceID, queue_identity: d.queue_identity, job_id: j.id, sequence: j.sequence, recorded_session_id: j.recorded_session_id, recorded_worktree: j.recorded_worktree };
  }
  same(a, b) { return a && b && ["machine", "space_id", "queue_identity", "job_id", "sequence", "recorded_session_id", "recorded_worktree"].every((k) => a[k] === b[k]); }
  current(owner, t = this.t) { return this.t === t && this.view.reconciliation === this && this.detail === this.view.detail && this.view.current(owner, this.generation) && this.same(t, this.target(owner)); }
  load(owner, cx) { const t = this.target(owner); this.t = t && Object.freeze(t); this.detail = this.view.detail; this.generation = this.view.generation; this.boundCurrent = () => this.current(owner); if (this.t) this.action(owner, "status", cx); }
  args(t) { return [t.space_id, t.queue_identity, t.job_id, t.sequence, t.recorded_session_id, t.recorded_worktree]; }
  snapshot(s, t) { return s && s.queue_identity === t.queue_identity && hex(s.snapshot_sha256) && s.job?.kind === "agent" && s.job.id === t.job_id && s.job.sequence === t.sequence && s.job.recorded_session_id === t.recorded_session_id && s.job.recorded_worktree === t.recorded_worktree; }
  validate(out, t) {
    if (!this.same(out.target, t) || !["none", "prepared", "armed", "uncertain", "changed", "blocked", "ineligible", "recorded"].includes(out.state)) throw new Error("Reconciliation target mismatch");
    if (out.operation_id && (!hex(out.operation_id) || !hex(out.intent_sha256) || !this.snapshot(out.before, t))) throw new Error("Operation intent mismatch");
    if (out.current && !this.snapshot(out.current, t)) throw new Error("Current agent snapshot mismatch");
    if (out.state === "recorded") {
      const r = out.operation_receipt;
      if (!r || r.version !== 1 || r.operation_id !== out.operation_id || r.intent_sha256 !== out.intent_sha256 || r.requested_snapshot_sha256 !== out.before.snapshot_sha256 || !hex(out.operation_receipt_sha256) || !this.snapshot(r.snapshot, t) || !["observer_returned", "observer_error", "dispatcher_lock_unavailable"].includes(r.operation_outcome)) throw new Error("Exact operation receipt missing");
    }
    return out;
  }
  async call(action, t, extra = []) {
    const r = await exec("ocq-herdr", [`agent-reconcile-${action}`, ...this.args(t), ...extra], { on: null, timeout: 120000, current: () => this.boundCurrent?.() && this.t === t });
    if (r.code !== 0) throw new Error("Reconciliation operation or journal unavailable");
    return this.validate(JSON.parse(r.stdout), t);
  }
  apply(out, owner, cx) {
    this.status = out;
    if (out.current) this.view.noteHelperObservation(owner, this.generation, this.detail, out.current, cx);
  }
  action(owner, action, cx) {
    const t = this.t, before = this.status?.current?.snapshot_sha256;
    if (this.working || !this.current(owner, t)) return;
    if (action === "prepare" && (this.status?.state !== "none" || !hex(before) || !admitted(this.detail.data.job.state))) return;
    if (action === "review" && this.status?.state !== "prepared") return;
    this.working = true; this.error = null; this.confirm = false;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t)) return;
        const out = await this.call(action === "review" ? "status" : action, t, action === "prepare" ? [before] : []);
        if (!this.current(owner, t)) return;
        this.apply(out, owner, cx); this.confirm = action !== "status" && out.state === "prepared";
        if (this.confirm) owner.handle.focus();
      } catch (_e) { if (this.current(owner, t)) { this.status = null; this.error = "Operation outcome unavailable. Check stored operation receipt only; never implicitly retry reconciliation."; } }
      finally { this.working = false; if (this.current(owner, t)) cx.notify(); }
    }); cx.notify();
  }
  send(owner, cx) {
    const t = this.t, id = this.status?.operation_id, digest = this.status?.intent_sha256;
    if (this.working || !this.confirm || this.status?.state !== "prepared" || !hex(id) || !hex(digest) || !this.current(owner, t)) return;
    this.working = true; this.status = { ...this.status, state: "uncertain" };
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t)) return;
        const claim = await this.call("arm", t, [id, digest]);
        if (!this.current(owner, t)) return;
        if (claim.state === "armed" && claim.operation_id === id && claim.intent_sha256 === digest) {
          if (!this.current(owner, t)) return;
          await this.call("attempt", t, [id, digest]);
        } else if (!["recorded", "changed"].includes(claim.state)) throw new Error("Claim not acknowledged");
      } catch (_e) { if (this.current(owner, t)) this.error = "Claim or observation response unavailable. Checking stored receipt only; never resend."; }
      finally {
        if (this.current(owner, t)) {
          try { const out = await this.call("status", t); if (this.current(owner, t)) { this.apply(out, owner, cx); this.error = null; } }
          catch (_e) { if (this.current(owner, t)) { this.status = null; this.error = "Exact operation read-back unavailable. Current job state alone cannot prove this call ran. Use Check only."; } }
        }
        this.working = false; this.confirm = false;
        if (this.current(owner, t)) cx.notify();
      }
    }); cx.notify();
  }
  confirmation(owner, cx, theme, button) {
    const t = this.t, s = this.status.before;
    return v_flex().id("ocq-agent-reconcile-confirm").size_full().min_h(0).p(14).gap(8).bg(theme.background).text_color(theme.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.view.key(e, owner, cx))
      .child(div().font_semibold().child("Reconcile this exact agent job via live API?"))
      .child(v_flex().flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child("Makes live API observations using the core's existing completion policy. May change the stored job and release capacity ONLY if completion is proven. Does NOT prompt, resume, cancel, send input, answer permissions, create sessions or adopt worktrees. This is not command resolution or queued cancellation. The dispatcher lock must be available; it is never stopped automatically."))
        .child(div().child(`Machine: local only\nSpace: ${t.space_id}\nQueue: ${t.queue_identity}\nAgent job: ${t.job_id}\nReceipt sequence: ${t.sequence}\nExact recorded session: ${t.recorded_session_id}\nExact recorded worktree: ${t.recorded_worktree}\nStored state/outcome: ${s.job.state} / ${s.job.outcome}\nStored reason: ${JSON.stringify(s.job.reason)}\nRequested snapshot SHA-256: ${s.snapshot_sha256}\nOperation: ${this.status.operation_id}\nIntent SHA-256: ${this.status.intent_sha256}`)))
      .child(v_flex().flex_none().gap(4)
        .child(div().text_size(12).text_color(theme.muted).child("One durable claim and one attempt. An already-launched observation may finish after leaving. Recovery reads a retained operation receipt, never reruns the API observer. Recording this operation is NOT proof of agent success or deliverable validation."))
        .when(!this.working, (e) => e.child(button("ocq-agent-reconcile-once", "Observe exact agent via API once · O", (cx) => this.send(owner, cx))))
        .when(!this.working, (e) => e.child(button("ocq-agent-reconcile-back", "Back without live observation", (cx) => { this.confirm = false; cx.notify(); })))
        .when(Boolean(this.working), (e) => e.child(div().child("Claiming once; observing and retaining the exact operation snapshot…"))));
  }
  render(content, owner, cx, theme, action) {
    const j = this.view.detail.data?.job;
    if (j?.kind !== "agent") return;
    content.child(div().font_semibold().child("Agent reconciliation · explicit live API observation"));
    if (owner.machine !== null) { content.child(div().text_color(theme.muted).child("Remote reconciliation is disabled. Stored reads remain available. Use that machine's local UI.")); return; }
    if (!this.t) { content.child(div().child("Exact recorded session/worktree or queue identity unavailable. Reconciliation disabled; no adoption or provisioning is attempted.")); return; }
    const state = this.status?.state;
    const labels = { none: "No live API observation has been authorized by this panel intent. Viewing and refreshing only read stored data.", prepared: "Exact stored intent retained, not confirmed. Review before live API observation.", armed: "Claim retained. An observation may be delayed; Check only, never resend.", uncertain: "Operation outcome uncertain. A current terminal job is NOT proof this call ran. Check the operation receipt only; never retry.", changed: "The exact stored snapshot changed before observation. Nothing may be dispatched from this intent; investigate.", blocked: "Journal blocked, corrupt or owned by another space. Return to the original context; do not retry.", ineligible: "This agent has not been admitted or is not eligible for observation. No provisioning or retry is offered.", recorded: "An exact operation receipt is retained. It records this explicit attempt, which may have been refused; NOT agent success or a validated deliverable." };
    content.child(div().text_color(theme.muted).child(this.working ? "Waiting for operation / reading stored receipt…" : labels[state] || "Read-back unavailable. Mutation disabled; Check only."));
    if (this.status) content.child(div().text_size(12).text_color(theme.muted).child("Reconciliation status/current-job observation time: not recorded. Operation receipt recorded_at is receipt evidence only; neither updates the selected detail or its read timestamp."));
    if (this.error) content.child(div().text_color(theme.ansi1).child(this.error));
    const result = this.status?.operation_receipt;
    if (result) content.child(div().text_size(12).text_color(theme.muted).child(`Operation ${result.operation_id}\nRecorded: ${result.recorded_at}\nOperation outcome: ${result.operation_outcome}\nRecorded job: ${result.snapshot.job.state} / ${result.snapshot.job.outcome}\nRecorded validation: ${result.snapshot.job.validation}\nCurrent job/ownership snapshot: ${this.status.current_snapshot_match}\nReceipt SHA-256: ${this.status.operation_receipt_sha256}\n${result.operation_outcome === "dispatcher_lock_unavailable" ? "The dispatcher lock was held or unavailable. No dispatcher was stopped or adopted." : "Unknown/blocked still holds capacity. The core alone decides completion; no permission or form is answered."}\nA first terminal notification is frozen history, not proof of this operation.`));
    if (!this.working && state === "none" && admitted(j.state)) content.child(action("ocq-agent-reconcile-prepare", "Review exact agent reconciliation…", (cx) => this.action(owner, "prepare", cx)));
    if (!this.working && state === "prepared") content.child(action("ocq-agent-reconcile-review", "Review retained API observation intent…", (cx) => this.action(owner, "review", cx)));
    if (!this.working) content.child(action("ocq-agent-reconcile-check", "Check operation receipt and stored job (no live API)", (cx) => this.action(owner, "status", cx)));
  }
}
