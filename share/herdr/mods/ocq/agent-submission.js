import { h_flex, InputState, TextareaState } from "gpui-base";
import { Buffer } from "buffer";
import { composeDecode, readInputFile } from "./private-frames.js";
import { CommandSubmission } from "./command-submission.js";

// Concrete v2 adapter. It cannot choose a command route, runtime tool, model or
// permission policy from imported metadata; Go owns all v2 validation/encoding.
export class AgentSubmission extends CommandSubmission {
  get slot() { return "agentSubmission"; }
  get route() { return "agent-submit"; }
  get kind() { return "agent"; }
  get version() { return 2; }
  get activeFields() { return this.mode === "import" ? this.fields.filter((f) => f.key === "raw") : this.fields.filter((f) => f.key !== "raw"); }
  get draftRoute() { return this.mode === "import" ? "import" : "draft"; }
  get warning() { return "TRUSTED AGENT REQUEST: an ALREADY RUNNING dispatcher may execute after admission. This form NEVER starts dispatch, provisions a worktree/session, sends a prompt or answers permissions. No agent/model availability or live API is checked here. Exact supplied profile and ORDERED rules are retained; this is not approval or sandboxing."; }
  get formHint() { return this.mode === "import" ? "Select an absolute local path to an existing agent request file. Original bytes are validated by Go and retained unchanged. Building a private review is not admission." : "Every field is explicit. Go pins the base before review. Select an existing ordered permission policy file; no policy is added or relaxed. Memory is an exact nonnegative byte count. Building a private review is not admission."; }
  static inputs(view) {
    return [["key", "Immutable idempotency key"], ["repo", "Explicit Git repository"], ["base", "Explicit base commit selector"], ["prompt", "Exact prompt (UTF-8, nonempty, <=16 KiB)", true], ["agent", "Exact agent ID (no substitution)"], ["provider_id", "Explicit provider ID"], ["model_id", "Explicit model ID"], ["variant", "Optional variant (blank explicitly omits)"], ["memory_bytes", "Exact nonnegative memory bytes"], ["permissions", "Absolute path to existing ordered permission policy"], ["raw", "Absolute path to retained agent request file"]].map(([key, label, multiline]) => {
      const state = multiline ? TextareaState.new({ placeholder: label, rows: 4 }) : InputState.new({ placeholder: label });
      state.on("change", (_e, cx) => cx.notify());
      return { key, label, multiline, state };
    });
  }
  async draftInput() {
    if (this.mode === "import") {
      const raw = await readInputFile(this.activeFields[0].state.value());
      if (!raw || composeDecode(Buffer.from(raw)) !== raw) throw new Error("Exact UTF-8 import required");
      return raw;
    }
    const d = Object.fromEntries(this.activeFields.map((f) => [f.key, f.state.value()]));
    d.permissions = await readInputFile(d.permissions, 16384);
    if (!/^(0|[1-9][0-9]*)$/.test(d.memory_bytes) || Object.values(d).some((v) => composeDecode(Buffer.from(v)) !== v)) throw new Error("Exact UTF-8 fields and decimal memory required");
    // Permissions remain ORIGINAL TEXT; no parse/stringify can erase duplicate
    // keys or ordered rules before the real Go decoder sees them.
    return JSON.stringify(d);
  }
  modeControls(owner, cx, button) {
    const change = (mode, cx) => {
      if (this.working || this.status?.state !== "idle" || !this.current(owner)) return;
      const next = new AgentSubmission(this.view, this.fields);
      Object.assign(next, { screen: true, generation: this.generation, space: this.space, folder: this.folder, t: this.t, status: this.status, mode });
      next.boundCurrent = () => next.current(owner);
      for (const f of next.fields) f.state.set_value("");
      this.view.agentSubmission = next; owner.handle.focus(); cx.notify();
    };
    return h_flex().gap(8)
      .child(button("ocq-agent-guided", this.mode !== "import" ? "Guided form ✓" : "Guided form", (cx) => change("guided", cx)))
      .child(button("ocq-agent-import", this.mode === "import" ? "Import file ✓" : "Import file", (cx) => change("import", cx)));
  }
}
