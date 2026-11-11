import { div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";

// Concrete implementation/review fields only. JSON lists are form data; the
// core alone encodes canonical v2, validates policies, and builds review prompts.
export class SubmissionForm {
  constructor(host, context) {
    this.role = context.reviewTarget ? "review" : "implementation";
    this.page = 0; this.focused = false;
    const common = [
      ["key", "Job idempotency key", "Required explicit unique key", ""],
      ["lane_id", "Lane ID (optional)", "Empty: core derives lane- plus first 16 SHA-256 digits of key", ""],
      ["repo", "Repository", "Must equal the selected bound local repository", context.folder || ""],
      ["base", "Base commit", "Required full 40-hex commit SHA; review must match target base", ""],
      ["agent", "Exact agent ID", "Required; never guessed or substituted", ""],
      ["provider_id", "Model provider ID", "Required explicit provider ID", ""],
      ["model_id", "Model ID", "Required explicit model ID within provider", ""],
      ["variant", "Model variant (optional)", "Empty means no variant override", ""],
      ["memory_bytes", "Memory demand in bytes", "Required nonnegative whole number; 0 explicitly declares zero demand (queue floor still applies)", ""],
      ["permissions", "Ordered session permission rules (JSON)", "Required full JSON array of action/resource/effect objects. Original JSON text reaches Go; order matters. No profile is added or relaxed.", "", true],
    ];
    const implementation = [
      ["goal", "Implementation goal", "Required; retained operator intent, not validation", "", true],
      ["prompt", "Implementation instructions", "Required exact prompt (up to 16 KiB); no instructions are invented", "", true],
      ["scope", "Scope (JSON string array)", "Required nonempty array", "", true, true],
      ["exclusions", "Exclusions (JSON string array)", "Explicit empty default []; replace when needed", "[]", true, true],
      ["acceptance_criteria", "Acceptance criteria (JSON string array)", "Required nonempty array; claimed intent only", "", true, true],
      ["allowed_actions", "Brief allowed actions (JSON string array)", "Required nonempty documentary list. Does not grant session permissions.", "", true, true],
      ["context_references", "Context references (JSON string array)", "Explicit empty default []; not execution authority", "[]", true, true],
    ];
    const review = [
      ["criteria", "Review criteria", "Required complete criteria, preserved verbatim", "", true],
      ["rules", "Review rules", "Required complete rules. Core additionally enforces static read/glob/grep policy.", "", true],
    ];
    this.fields = [...common, ...(this.role === "review" ? review : implementation),
      ["referenced_manifest_digests", "Additional stored reference digests (JSON)", this.role === "review" ? "Default []; exact selected result is included automatically. Other stored manifests sorted by digest; duplicates rejected." : "Default []; full referenced stored manifests included, sorted by digest; duplicates rejected.", "[]", true, true],
    ].map(([key, label, hint, value, multiline, json]) => {
      const state = multiline ? TextareaState.new({ placeholder: hint, rows: 5 }) : InputState.new({ placeholder: hint });
      state.set_value(value);
      state.on("change", (_e, cx) => cx.notify());
      state.on("focus", (_e, cx) => { this.focused = true; cx.notify(); });
      state.on("blur", (_e, cx) => { this.focused = false; cx.notify(); });
      if (!multiline) state.on("submit", (_e, cx) => this.advance(1, host, cx));
      return { key, label, hint, multiline, json, state };
    });
  }

  advance(delta, host, cx) {
    this.page = Math.max(0, Math.min(this.fields.length - 1, this.page + delta));
    host.handle.focus(); cx.notify();
  }

  values(context) {
    const d = { project_id: context.project, binding_revision: context.revision };
    for (const f of this.fields) {
      const text = f.state.value();
      d[f.key] = f.json ? JSON.parse(text) : text;
    }
    if (!/^(0|[1-9][0-9]*)$/.test(d.memory_bytes) || !Number.isSafeInteger(Number(d.memory_bytes))) throw new Error("explicit safe memory count required");
    d.memory_bytes = Number(d.memory_bytes);
    if (this.role === "review") {
      if (!context.reviewTarget) throw new Error("selected result required");
      d.target_association_id = context.reviewTarget.association;
      d.target_job_id = context.reviewTarget.job;
      d.target_result_digest = context.reviewTarget.digest;
    }
    return d;
  }

  snapshot() { return JSON.stringify(this.fields.map((f) => f.state.value())); }

  render(host, cx, t, button) {
    const f = this.fields[this.page], context = host.intake.context, working = host.intake.working;
    return v_flex().id("ocq-submission-form").gap(8)
      .child(div().font_semibold().child(`Guided ${this.role} · binding revision ${context.revision ?? "unavailable"}`))
      .child(div().text_size(12).child(`Project: ${context.project || "select a project"}. Generated new draft, not raw import. Operator-supplied brief claims are not validation or execution authority. Repo/revision stay pinned; no agent, model or permissions are inferred.`))
      .when(this.role === "review", (e) => e.child(div().text_size(12).child("Go reads the exact selected immutable result. Its full diff and changed-file contents feed the existing deterministic prompt. Full result/reference bytes remain reviewable. The core 16 KiB prompt gate rejects oversized targets; nothing is truncated. Enter the exact ordered permission list; no policy is silently added.")))
      .child(div().child(`Field ${this.page + 1}/${this.fields.length} · ${f.label}`))
      .child(div().text_size(12).text_color(t.muted).child(f.hint))
      .child((f.multiline ? Textarea.new(f.state).h(140) : Input.new(f.state).h(38))
        .id(`ocq-submission-${f.key}`).accessibility_label(f.label).px(8).border_1().border_color(t.border).text_color(t.foreground))
      .child(h_flex().gap(8)
        .when(!working && this.page > 0, (e) => e.child(button("ocq-submission-previous", "Previous field", (cx) => this.advance(-1, host, cx))))
        .when(!working && this.page < this.fields.length - 1, (e) => e.child(button("ocq-submission-next", "Next field", (cx) => this.advance(1, host, cx)))))
      .child(div().text_size(12).text_color(t.muted).child("Enter advances single-line fields. F6 leaves an input unchanged, then ←/→ changes fields and G builds. Multiline Enter adds a newline, never submits a job."))
      .when(!working && this.page === this.fields.length - 1, (e) => e.child(button("ocq-submission-build", "Build generated preview", (cx) => host.buildSubmission(cx))));
  }
}
