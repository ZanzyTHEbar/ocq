import { div } from "gpui-kit";
import { exec } from "herdr";

// A human inspect destination, not a reconciliation or permission control.
// In particular this module has no terminal, URL, prompt or client launch API.
export class AgentHandoff {
  constructor(view) { this.view = view; }
  target(owner) { return this.view.reconciliation.target(owner); }
  same(a, b) { return this.view.reconciliation.same(a, b); }
  current(owner, t) {
    return this.view.handoff === this && this.t === t && this.detail === this.view.detail &&
      this.view.current(owner, this.generation) && this.same(t, this.target(owner));
  }
  load(owner, cx) {
    this.t = this.target(owner); this.detail = this.view.detail; this.generation = this.view.generation;
    if (this.t) { this.t = Object.freeze(this.t); this.read(owner, false, cx); }
  }
  read(owner, verify, cx) {
    const t = this.t, service = this.status?.service_identity;
    if (this.working || !this.current(owner, t) || (verify && !/^[0-9a-f]{64}$/.test(service || ""))) return;
    this.working = true; this.copied = false; this.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t)) return;
        const r = await exec("ocq-herdr", [`agent-handoff-${verify ? "verify" : "status"}`, t.space_id, t.queue_identity, t.job_id, t.sequence, t.recorded_session_id, t.recorded_worktree, ...(verify ? [service] : [])], { on: null, timeout: 20000 });
        if (!this.current(owner, t)) return;
        if (r.code !== 0) throw new Error("Handoff unavailable");
        const s = JSON.parse(r.stdout);
        if (!this.same(s.target, t) || !["stored_only", "verified_snapshot", "service_unavailable", "unsupported_client", "session_unavailable", "session_mismatch"].includes(s.observation) || !["Not configured", "Configured local OpenCode"].includes(s.service_label) || typeof s.open_unavailable_reason !== "string" || s.open_unavailable_reason.length > 1024 || (s.service_identity && !/^[0-9a-f]{64}$/.test(s.service_identity)) || (verify && s.service_identity !== service)) throw new Error("Handoff target or service changed");
        this.status = s;
      } catch (_e) {
        if (this.current(owner, t)) { this.status = null; this.error = "Exact handoff read unavailable or context changed. Nothing opened or sent. Refresh stored details before trying again."; }
      } finally { this.working = false; if (this.current(owner, t)) cx.notify(); }
    }); cx.notify();
  }
  text(t, s) {
    return `Human OpenCode handoff · recorded inspect destination (not a command)\nMachine: local\nSpace: ${t.space_id}\nQueue: ${t.queue_identity}\nJob: ${t.job_id}\nReceipt sequence: ${t.sequence}\nService: ${s.service_label}\nService identity: ${s.service_identity || "unavailable"}\nExact recorded session: ${t.recorded_session_id}\nExact recorded worktree: ${t.recorded_worktree}\nObservation: ${s.observation} (snapshot only; availability may change)\nStored request reasons are not live permission/form classification. Use the actual OpenCode interface for human approvals. This handoff does not resolve a request or prove completion.`;
  }
  render(content, owner, cx, theme, action) {
    if (this.view.detail.data?.job?.kind !== "agent") return;
    content.child(div().font_semibold().child("Human OpenCode handoff"));
    if (owner.machine !== null) { content.child(div().text_color(theme.muted).child("Remote handoff disabled. Nothing opens or redirects locally.")); return; }
    const t = this.t, s = this.status;
    if (!t) { content.child(div().child("Exact recorded session/worktree unavailable; no latest-session search or adoption is offered.")); return; }
    if (this.error) content.child(div().text_color(theme.ansi1).child(this.error));
    if (this.working) content.child(div().child("Reading exact handoff destination…"));
    if (s) {
      const reasons = { stored_only: "Stored location only; no live lookup has run.", verified_snapshot: "Exact session/location/profile/ownership matched the configured service at this read. Not live status or completion.", service_unavailable: "Configured exact service absent or changed. No discovery or fallback was attempted.", unsupported_client: "Installed client is not verified OpenCode 2.2.6. No session lookup ran.", session_unavailable: "Exact session unavailable at the configured service, or API/auth failed. Nothing created or resumed.", session_mismatch: "Returned session/location/profile/ownership did not match. Do not use it as this job's session." };
      content.child(div().child(reasons[s.observation]));
      if (!this.working) {
        content.child(action("ocq-agent-handoff-copy", this.copied ? "Copied recorded handoff details" : "Copy exact recorded handoff details", (cx) => {
          if (!this.current(owner, t) || this.status !== s || this.working) return;
          cx.write_to_clipboard(this.text(t, s)); this.copied = true; cx.notify();
        }));
        if (s.service_identity) content.child(action("ocq-agent-handoff-verify", "Verify exact session (read-only)", (cx) => {
          if (!this.current(owner, t) || this.status !== s) return;
          this.read(owner, true, cx);
        }));
      }
      content.child(div().text_size(12).text_color(theme.muted).child(`Service: ${s.service_label}\nService identity: ${s.service_identity || "unavailable"}\nExact recorded session: ${t.recorded_session_id}\nExact recorded worktree: ${t.recorded_worktree}`));
      content.child(div().text_size(12).text_color(theme.muted).child("Stored request reasons are not live permission/form classification. Use actual OpenCode for human approvals. Handoff does not resolve requests or prove completion."));
      content.child(div().text_size(12).text_color(theme.muted).child(s.open_unavailable_reason));
    }
    if (!this.working) content.child(action("ocq-agent-handoff-stored", "Refresh handoff details (stored only)", (cx) => { if (this.current(owner, t)) this.read(owner, false, cx); }));
  }
}
