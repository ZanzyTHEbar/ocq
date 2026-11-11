import { div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";

// The two existing explicit mail kinds only. Stored identities are routing
// metadata, not authentication or OS permission; no current session is guessed.
export class ComposeForm {
  constructor(host, context, kind = "lane.blocked") {
    this.kind = kind; this.page = 0; this.focused = false;
    this.from = kind === "lane.blocked" ? `lane:${context.lane}` : "";
    this.to = kind === "lane.blocked" ? `integrator:${context.project}` : "";
    this.pathMode = "default";
    this.fields = [
      ["Subject", "Message subject", false], ["Body", "Message body", true],
      ["Manifest", "Optional retained manifest digest (blank means none)", false],
      ["ReplyTo", "Optional retained same-project message ID (blank means no reply)", false],
      ["FsforkPath", "Literal worktree metadata override", true],
    ].map(([key, label, multiline]) => {
      const state = multiline ? TextareaState.new({ placeholder: label, rows: 4 }) : InputState.new({ placeholder: label });
      state.on("change", (_e, cx) => cx.notify());
      state.on("focus", (_e, cx) => { this.focused = true; cx.notify(); });
      state.on("blur", (_e, cx) => { this.focused = false; cx.notify(); });
      if (!multiline) state.on("submit", (_e, cx) => this.advance(1, host, cx));
      return { key, label, multiline, state };
    });
  }

  advance(delta, host, cx) { if (host.compose.form !== this || host.compose.working || !host.composeCurrent(host.compose, host.compose.context)) return; this.page = Math.max(0, Math.min(6, this.page + delta)); host.handle.focus(); cx.notify(); }
  snapshot() { return JSON.stringify([this.kind, this.from, this.to, this.pathMode, ...this.fields.map((f) => f.state.value())]); }

  request(context, options) {
    if (options.project_id !== context.project || options.lane_id !== context.lane || options.job_id !== context.job) throw new Error("owner changed");
    const blocked = this.kind === "lane.blocked";
    if (!blocked && this.kind !== "lane.assigned") throw new Error("automatic/unknown kind unavailable");
    if (blocked ? ![`lane:${context.lane}`, `job:${context.job}`].includes(this.from) || this.to !== `integrator:${context.project}` : !options.integrator_senders.includes(this.from) || !options.recipient_lanes.some((lane) => this.to === `lane:${lane}`)) throw new Error("select an exact stored route");
    const r = { ProjectID: context.project, LaneID: context.lane, JobID: context.job, From: this.from, To: this.to, Kind: this.kind };
    for (const f of this.fields) r[f.key] = f.state.value();
    if (this.pathMode === "default") r.FsforkPath = "";
    else if (this.pathMode !== "override" || r.FsforkPath === "") throw new Error("literal override must be nonempty; empty always means lane default");
    if (r.Manifest !== "" && !/^[0-9a-f]{64}$/.test(r.Manifest) || r.ReplyTo !== "" && !/^msg-[0-9a-f]{32}$/.test(r.ReplyTo)) throw new Error("exact retained link identifiers required");
    return r;
  }

  render(host, cx, t, button, options) {
    const c = host.compose, context = c.context, f = this.fields[this.page - 2];
    const pick = (id, text, selected, action) => button(id, `${text}${selected ? " ✓" : ""}`, action);
    return v_flex().id("ocq-compose-form").gap(8)
      .child(div().child(`Project: ${context.project}\nMessage lane: ${context.lane}\nExact owner job: ${context.job}`))
      .child(div().text_size(12).text_color(t.muted).child("Identities are stored routing metadata, not OS authentication or permission. Only lane.blocked and lane.assigned are manual; work.submitted/review.submitted are automatic. Changing kind clears the form. Core generates the idempotency key once when preparing."))
      .child(h_flex().gap(8)
        .child(pick("ocq-compose-blocked", "lane.blocked", this.kind === "lane.blocked", (cx) => host.composeMode("lane.blocked", cx)))
        .child(pick("ocq-compose-assigned", "lane.assigned", this.kind === "lane.assigned", (cx) => host.composeMode("lane.assigned", cx))))
      .child(div().font_semibold().child(`Field ${this.page + 1}/7 · ${this.page === 0 ? "Sender" : this.page === 1 ? "Recipient" : f.label}`))
      .when(this.page === 0, (e) => e.child(v_flex().gap(6)
        .child(div().text_size(12).child(this.kind === "lane.blocked" ? "Exact message lane or its owner job → project integrator inbox." : "Choose a retained integrator sender or the recorded binding session identity. No live session lookup; no new identity invented."))
        .children((this.kind === "lane.blocked" ? [`lane:${context.lane}`, `job:${context.job}`] : options.integrator_senders).map((sender, n) => pick(`ocq-compose-from-${n}`, sender, this.from === sender, (cx) => { this.from = sender; cx.notify(); })))))
      .when(this.page === 1, (e) => e.child(v_flex().gap(6)
        .child(div().text_size(12).child(this.kind === "lane.blocked" ? "The core route is the project integrator inbox (not an inferred session ID)." : "Recipient may be ANY retained lane in this project; it is separate from the message-owner lane above."))
        .children((this.kind === "lane.blocked" ? [`integrator:${context.project}`] : options.recipient_lanes.map((lane) => `lane:${lane}`)).map((to, n) => pick(`ocq-compose-to-${n}`, to, this.to === to, (cx) => { this.to = to; cx.notify(); })))))
      .when(this.page === 4 || this.page === 5, (e) => e.child(div().text_size(12).child("Paste an exact retained ID from project inspection/inbox. A read-only core lookup verifies same-project existence before preparation; the send transaction rechecks it. Blank is explicitly no link.")))
      .when(this.page === 6, (e) => e.child(v_flex().gap(6)
        .child(pick("ocq-compose-path-default", "Explicit empty → use lane default", this.pathMode === "default", (cx) => { this.pathMode = "default"; cx.notify(); }))
        .child(pick("ocq-compose-path-override", "Nonempty literal override", this.pathMode === "override", (cx) => { this.pathMode = "override"; cx.notify(); }))
        .child(div().text_size(12).child(`Stored lane default (quoted, not normalized):\n${host.composeQuoted(options.lane_path)}\nAn empty default remains empty. A literal such as "lane default" is NOT a default sentinel. This field is metadata; no path is opened or created.`))))
      .when(Boolean(f) && (this.page !== 6 || this.pathMode === "override"), (e) => e.child((f.multiline ? Textarea.new(f.state).h(120) : Input.new(f.state).h(38))
        .id(`ocq-compose-${f.key}`).accessibility_label(f.label).px(8).border_1().border_color(t.border).text_color(t.foreground)))
      .child(h_flex().gap(8)
        .when(this.page > 0, (e) => e.child(button("ocq-compose-form-previous", "Previous field", (cx) => this.advance(-1, host, cx))))
        .when(this.page < 6, (e) => e.child(button("ocq-compose-form-next", "Next field", (cx) => this.advance(1, host, cx)))))
      .child(div().text_size(12).text_color(t.muted).child("F6 leaves text unchanged; then ←/→ changes fields. Enter advances single-line fields; multiline Enter adds a newline. P prepares only, never sends."))
      .when(this.page === 6, (e) => e.child(button("ocq-compose-prepare", "Prepare retained message (no send)", (cx) => host.prepareCompose(cx))));
  }
}
