import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { exec } from "./compatibility.js";

// Cancellation has its own outcomes: a dispatcher win is not cancellation.
// Reload only inspects the retained job and journal, never resumes a claim.
export class QueuedCancellation {
  constructor(view) { this.view = view; }

  scope(owner) {
    const d = this.view.detail.data, j = d?.job;
    if (owner.machine !== null || !j || !/^[0-9a-f]{64}$/.test(d.queue_identity || "") || typeof j.sequence !== "string" || !/^[1-9][0-9]*$/.test(j.sequence)) return null;
    return { machine: "local", space_id: owner.spaceID, queue_identity: d.queue_identity, job_id: j.id, sequence: j.sequence, displayed_state: "queued" };
  }
  same(a, b) { return a && b && ["machine", "space_id", "queue_identity", "job_id", "sequence", "displayed_state"].every((k) => a[k] === b[k]); }
  current(owner, scope = this.s) { return this.s === scope && this.view.cancel === this && this.detail === this.view.detail && this.view.current(owner, this.generation) && this.same(scope, this.scope(owner)); }
  load(owner, cx) {
    const scope = this.scope(owner);
    this.s = scope && Object.freeze(scope); this.detail = this.view.detail; this.generation = this.view.generation;
    this.boundCurrent = () => this.current(owner);
    if (this.s) this.action(owner, "status", cx);
  }
  async call(action, id, s) {
    const r = await exec("ocq-herdr", [`queued-cancel-${action}`, s.space_id, s.queue_identity, s.job_id, s.sequence, s.displayed_state, ...(id ? [id] : [])], { on: null, current: () => this.boundCurrent?.() && this.s === s });
    if (r.code !== 0) throw new Error("Exact cancellation read-back unavailable");
    const out = JSON.parse(r.stdout), o = out.observation;
    if (!this.same(out.scope, s) || !["none", "prepared", "armed", "uncertain", "blocked", "not_queued", "cancelled_observed"].includes(out.state)) throw new Error("Cancellation scope mismatch");
    if (["prepared", "armed", "uncertain"].includes(out.state) && !/^[0-9a-f]{64}$/.test(out.intent_id || "")) throw new Error("Intent missing");
    if (o && (o.queue_identity !== s.queue_identity || o.job?.id !== s.job_id || o.job?.sequence !== s.sequence)) throw new Error("Receipt mismatch");
    if (out.state === "cancelled_observed" && (o?.job?.state !== "cancelled" || o.job.outcome !== "cancelled_before_launch" || o.completion?.job_id !== s.job_id || o.completion.state !== "cancelled" || o.completion.outcome !== "cancelled_before_launch" || o.completion.id !== o.job.completion_event_id)) throw new Error("Cancellation not established");
    return out;
  }
  apply(out, owner, cx) {
    this.status = out;
    if (out.observation) this.view.noteHelperObservation(owner, this.generation, this.detail, out.observation, cx);
  }
  action(owner, action, cx) {
    const scope = this.s;
    if (this.working || !this.current(owner, scope)) return;
    if (action === "prepare" && this.status?.state !== "none") return;
    if (action === "review" && this.status?.state !== "prepared") return;
    this.working = true; this.error = null; this.confirm = false;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, scope)) return;
        const out = await this.call(action === "review" ? "status" : action, undefined, scope);
        if (!this.current(owner, scope)) return;
        this.apply(out, owner, cx); this.confirm = action !== "status" && out.state === "prepared";
      } catch (_e) { if (this.current(owner, scope)) { this.status = null; this.error = "Outcome unavailable. Check only; never resend cancellation."; } }
      finally { this.working = false; if (this.current(owner, scope)) cx.notify(); }
    }); cx.notify();
  }
  send(owner, cx) {
    const id = this.status?.intent_id, scope = this.s;
    if (this.working || !this.confirm || this.status?.state !== "prepared" || !/^[0-9a-f]{64}$/.test(id || "") || !this.current(owner, scope)) return;
    this.working = true; this.status = { state: "uncertain", intent_id: id };
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, scope)) return;
        const arm = await this.call("arm", id, scope);
        if (!this.current(owner, scope)) return;
        if (arm.state === "armed" && arm.intent_id === id) {
          if (!this.current(owner, scope)) return;
          await this.call("attempt", id, scope);
        } else if (!["cancelled_observed", "not_queued"].includes(arm.state)) throw new Error("Claim unavailable");
      } catch (_e) { if (this.current(owner, scope)) this.error = "Claim or cancellation response unavailable; checking only."; }
      finally {
        if (this.current(owner, scope)) {
          try {
            const out = await this.call("status", undefined, scope);
            if (this.current(owner, scope)) {
              this.apply(out, owner, cx);
              if (["cancelled_observed", "not_queued"].includes(out.state)) this.error = null;
            }
          } catch (_e) { if (this.current(owner, scope)) { this.status = null; this.error = "Exact read-back unavailable. Outcome uncertain. Use Check only."; } }
        }
        this.working = false; this.confirm = false;
        if (this.current(owner, scope)) cx.notify();
      }
    }); cx.notify();
  }
  confirmation(owner, cx, t, button) {
    const s = this.s;
    return v_flex().id("ocq-cancel-confirmation").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.view.key(e, owner, cx))
      .child(div().font_semibold().child("Cancel this queued job?"))
      .child(v_flex().flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child("Cancels only queued work. Does NOT stop a running process. Creates an immutable cancelled-before-launch outcome and global completion notification. If dispatch wins the race, cancellation is refused; the actual job outcome is preserved."))
        .child(div().child(`Machine: this computer (local only)\nSpace: ${s.space_id}\nQueue identity: ${s.queue_identity}\nJob: ${s.job_id}\nReceipt sequence: ${s.sequence}\nDisplayed state: ${s.displayed_state}`))
        .child(div().text_color(t.muted).child("Confirmation durably claims one attempt. A launched call may finish after you leave. Lost responses are uncertain; recovery only reads and never retries.")))
      .child(v_flex().flex_none().gap(4)
        .child(div().child(this.working ? "Claiming once and reading exact outcome…" : "No running process will be stopped."))
        .when(!this.working, (e) => e.child(button("ocq-cancel-once", "Cancel queued job once · C", (cx) => this.send(owner, cx))))
        .when(!this.working, (e) => e.child(button("ocq-cancel-back", "Back without cancelling", (cx) => { this.confirm = false; cx.notify(); }))));
  }
  render(content, owner, cx, t, action) {
    content.child(div().font_semibold().child("Queued-job cancellation"));
    if (owner.machine !== null) { content.child(div().text_color(t.muted).child("Cancellation is local-only; disabled on this selected machine. Stored reads remain available.")); return; }
    if (!this.s) { content.child(div().child("Exact queue identity unavailable; cancellation disabled.")); return; }
    const state = this.status?.state;
    const labels = {
      none: "No cancellation intent. Only queued work may be cancelled.", prepared: "Retained intent, not confirmed. Review before cancelling.",
      armed: "Confirmed claim retained. A call may be delayed. Check only; never resend.", uncertain: "Outcome uncertain. Check only; never resend.",
      cancelled_observed: "Cancelled-before-launch job and immutable completion observed. This does not establish which caller caused it.",
      not_queued: "Cancellation journal observed a job no longer queued. No cancellation is claimed. Its snapshot is separate from the selected detail.",
      blocked: "Journal blocked or owned by another space. Return to the original context; investigate, never resend."
    };
    content.child(div().text_color(t.muted).child(this.working ? "Reading exact job and cancellation journal…" : labels[state] || "Observation unavailable; cancellation disabled."));
    if (this.status) content.child(div().text_size(12).text_color(t.muted).child("Cancellation status/job observation time: not recorded. Journal evidence does not update the selected detail or its read timestamp."));
    if (this.error) content.child(div().text_color(t.ansi1).child(this.error));
    if (!this.working && state === "none" && this.detail.data.job.state === "queued") content.child(action("ocq-cancel-prepare", "Review queued-job cancellation…", (cx) => this.action(owner, "prepare", cx)));
    if (!this.working && state === "prepared") content.child(action("ocq-cancel-review", "Review retained cancellation…", (cx) => this.action(owner, "review", cx)));
    if (!this.working) content.child(action("ocq-cancel-check", "Check exact job, completion and journal (read-only)", (cx) => this.action(owner, "status", cx)));
  }
}
