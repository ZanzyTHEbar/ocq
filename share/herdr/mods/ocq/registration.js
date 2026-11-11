import { div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";

// A concrete registration form, not a canonical JSON encoder. Go owns the
// envelope/manifest serializer and decoder. Values live only in this view.
export class RegistrationForm {
  constructor(host, cx, context) {
    this.page = 0;
    this.focused = false;
    this.mode = context.newProject ? "new" : "rebind";
    this.revision = context.newProject ? 0 : context.revision;
    const project = host.projects.find((p) => p.project_id === context.project);
    this.fields = [
      ["project_id", "Project ID", "Explicit proj-… ID", context.newProject ? "" : context.project || ""],
      ["label", "Project label", "Required; rebind must match stored label", context.newProject ? "" : project?.label || ""],
      ["key", "Registration idempotency key", "Explicit unique key for this registration", ""],
      ["repo", "Repository", "Exact clean absolute repository path", context.folder || ""],
      ["commit_anchor", "Commit anchor", "Explicit full 40-hex commit SHA; no HEAD lookup", ""],
      ["elephant_session_id", "Coordinator session ID", "Explicit owned ses_… ID; never latest", ""],
      ["expected_opencode_project_id", "Expected OpenCode project ID", "Exact owned session project identity", ""],
      ["expected_opencode_directory", "Expected OpenCode directory", "Exact owned session location (must equal repository)", context.folder || ""],
      ["auth_ref", "Authentication reference", "Reference only, never a password or token", ""],
      ["intent", "Handoff intent", "Required; multiline text is preserved", "", true],
      ["decision", "Handoff decision (optional)", "One optional entry; multiline preserved. Advanced raw supports multiple entries.", "", true],
      ["risk", "Handoff risk (optional)", "One optional entry; multiline preserved. Advanced raw supports multiple entries.", "", true],
    ].map(([key, label, hint, value, multiline]) => {
      const state = multiline ? TextareaState.new({ placeholder: hint, rows: 5 }) : InputState.new({ placeholder: hint });
      state.set_value(value);
      state.on("change", (_e, cx) => cx.notify());
      state.on("focus", (_e, cx) => { this.focused = true; cx.notify(); });
      state.on("blur", (_e, cx) => { this.focused = false; cx.notify(); });
      if (!multiline) state.on("submit", (_e, cx) => this.advance(1, host, cx));
      return { key, label, hint, multiline, state };
    });
  }

  advance(delta, host, cx) {
    this.page = Math.max(0, Math.min(this.fields.length - 1, this.page + delta));
    host.handle.focus(); cx.notify();
  }

  values() {
    const d = { mode: this.mode, expected_revision: this.revision };
    for (const f of this.fields) d[f.key] = f.state.value();
    // Empty means no entry, not a fabricated decision or risk. Never trim,
    // split newlines, normalize Unicode or rewrite the user's strings.
    d.decisions = d.decision === "" ? [] : [d.decision]; delete d.decision;
    d.risks = d.risk === "" ? [] : [d.risk]; delete d.risk;
    return d;
  }

  render(host, cx, t, button) {
    const f = this.fields[this.page], working = host.intake.working;
    return v_flex().id("ocq-registration-form").gap(8)
      .child(div().font_semibold().child(this.mode === "new" ? "New project · expected revision 0 (must be absent)" : `Rebind selected project · expected revision ${this.revision ?? "unavailable"}`))
      .child(div().text_size(12).child("Generated draft, not raw import. Nothing registers while building. All identities and commit must be supplied explicitly; no discovery or session creation. Rebind changes the binding only after separate confirmation."))
      .child(div().child(`Field ${this.page + 1}/${this.fields.length} · ${f.label}`))
      .child(div().text_size(12).text_color(t.muted).child(f.hint))
      .child((f.multiline ? Textarea.new(f.state).h(140) : Input.new(f.state).h(38))
        .id(`ocq-registration-${f.key}`).accessibility_label(f.label).px(8).border_1().border_color(t.border).text_color(t.foreground))
      .child(h_flex().gap(8)
        .when(!working && this.page > 0, (e) => e.child(button("ocq-registration-previous", "Previous field", (cx) => this.advance(-1, host, cx))))
        .when(!working && this.page < this.fields.length - 1, (e) => e.child(button("ocq-registration-next", "Next field", (cx) => this.advance(1, host, cx)))))
      .child(div().text_size(12).text_color(t.muted).child("Enter moves to the next single-line field. F6 leaves an input without editing it; then ←/→ changes fields and G builds a preview. Enter in multiline text adds a newline, never confirms registration."))
      .when(!working && this.page === this.fields.length - 1, (e) => e.child(button("ocq-registration-build", "Build generated preview", (cx) => host.buildRegistration(cx))));
  }
}
