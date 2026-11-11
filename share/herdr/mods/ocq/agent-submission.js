import { h_flex, InputState, TextareaState } from "gpui-base";
import { Buffer } from "buffer";
import { composeDecode } from "./private-frames.js";
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
  get formHint() { return this.mode === "import" ? 'Advanced exact JSON: {"key":"explicit-key","request":"EXACT canonical v2 JSON text"}; request may also be an exact canonical object/wrapper. Go validates without reserializing imported bytes. Complete version/kind/repo/full base/prompt/profile/ordered permissions/provision_policy/memory required. No JS parse/rewrite. F6 leaves text unchanged; G builds a private review, no admission.' : "Every field is explicit. Base may be an explicit Git commit selector; Go pins it before review (no inferred HEAD). Empty variant explicitly means omitted. Ordered permissions are ORIGINAL JSON text with action/resource/effect; Go rejects duplicate keys/invalid rules without reordering. Memory is exact nonnegative decimal bytes; 0 is zero demand, not a runtime limit. Provision policy is the fixed core constant. F6 leaves text unchanged; ←/→ fields, G private draft."; }
  static inputs(view) {
    return [["key", "Immutable idempotency key"], ["repo", "Explicit Git repository"], ["base", "Explicit base commit selector"], ["prompt", "Exact prompt (UTF-8, nonempty, <=16 KiB)", true], ["agent", "Exact agent ID (no substitution)"], ["provider_id", "Explicit provider ID"], ["model_id", "Explicit model ID"], ["variant", "Optional variant (blank explicitly omits)"], ["memory_bytes", "Exact nonnegative memory bytes"], ["permissions", "Original ordered permission JSON array", true], ["raw", "Exact advanced {key,request} JSON", true]].map(([key, label, multiline]) => {
      const state = multiline ? TextareaState.new({ placeholder: label, rows: 4 }) : InputState.new({ placeholder: label });
      state.on("change", (_e, cx) => cx.notify());
      return { key, label, multiline, state };
    });
  }
  draftInput() {
    if (this.mode === "import") {
      const raw = this.activeFields[0].state.value();
      if (!raw || composeDecode(Buffer.from(raw)) !== raw) throw new Error("Exact UTF-8 import required");
      return raw;
    }
    const d = Object.fromEntries(this.activeFields.map((f) => [f.key, f.state.value()]));
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
      .child(button("ocq-agent-import", this.mode === "import" ? "Exact JSON import ✓" : "Exact JSON import", (cx) => change("import", cx)));
  }
}
