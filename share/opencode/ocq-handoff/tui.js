import { Plugin } from "@opencode/plugin/tui";
import { createHash } from "node:crypto";

// TUI-only navigation. The launcher supplies hashes, never the owner token,
// permissions, prompt, or credentials. No server plugin or RPC is registered.
const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const text = (value, limit) => typeof value === "string" && value.length > 0 &&
  value.length <= limit && !/[\u0000-\u001f\u007f]/.test(value);
const object = value => value !== null && typeof value === "object" && !Array.isArray(value);

// Match encoding/json's sorted-object encoding, including HTML/line escaping.
// Unsupported numeric representations fail closed rather than weaken identity.
function digest(value) {
  function sorted(item) {
    if (Array.isArray(item)) return item.map(sorted);
    if (object(item)) return Object.fromEntries(Object.keys(item).sort().map(key => [key, sorted(item[key])]));
    if (typeof item === "number" && (!Number.isFinite(item) || (Number.isInteger(item) && !Number.isSafeInteger(item)))) throw Error("Unsupported identity number");
    if (!["string", "boolean", "number"].includes(typeof item) && item !== null) throw Error("Unsupported identity value");
    return item;
  }
  const raw = JSON.stringify(sorted(value)).replace(/[<>&\u2028\u2029]/g,
    char => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`);
  return createHash("sha256").update(raw, "utf8").digest("hex");
}

function matches(session, target) {
  const ownership = target.target_kind === "worker"
    ? object(session?.metadata) && session.metadata.ocq?.job_id === target.job_id
    : target.ownership_mode === "legacy_adoption" ||
      (target.ownership_mode === "ocq_main" && object(session?.metadata) &&
        session.metadata.ocq_main?.project_id === target.project_id &&
        session.metadata.ocq_main?.main_id === target.main_id && session.metadata.ocq_main?.role === "integrator");
  return object(session) && session.id === target.session_id && !session.parentID &&
    session.location?.directory === target.worktree && session.projectID === target.opencode_project_id &&
    typeof session.agent === "string" && object(session.model) && Array.isArray(session.permissions) &&
    ownership &&
    digest(session.metadata) === target.ownership_sha256 &&
    digest({ agent: session.agent, model: session.model, permissions: session.permissions }) === target.profile_sha256;
}

export default Plugin.define({
  id: "ocq.exact-handoff",
  async setup(context) {
    const target = context.options;
    const fail = (title = "Could not open this conversation") => context.ui?.toast?.show?.({
      title,
      variant: "error",
    });
    // The release number cannot prove the public V2 router/client contract.
    const router = context.ui?.router;
    if (!/^2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+[0-9A-Za-z.-]+)?$/.test(context.app?.version ?? "") ||
      typeof router?.current !== "function" || typeof router?.navigate !== "function" ||
      typeof context.client?.session?.get !== "function") {
      fail("Required OpenCode V2 navigation is unavailable"); return;
    }
    if (!object(target) || !["ocq-handoff-v1", "ocq-attention-handoff-v1"].includes(target.protocol) ||
      !/^ses_[A-Za-z0-9_-]{1,128}$/.test(target.session_id || "") ||
      !["worker", "integrator"].includes(target.target_kind) || !text(target.project_id, 128) ||
      (target.target_kind === "worker" ? !text(target.job_id, 128) :
        !/^[A-Za-z0-9_-]{1,64}$/.test(target.main_id || "") || !["ocq_main", "legacy_adoption"].includes(target.ownership_mode)) ||
      !text(target.worktree, 4096) || !target.worktree.startsWith("/") ||
      !text(target.opencode_project_id, 128) || !hex(target.queue_identity) ||
      !hex(target.ownership_sha256) || !hex(target.profile_sha256)) {
      fail(); return;
    }
    // Never hijack a route the person has already chosen during startup.
    try {
      if (router.current()?.type !== "home") { fail(); return; }
      const session = await context.client.session.get(
        { sessionID: target.session_id }, { signal: AbortSignal.timeout(10000) },
      );
      if (!matches(session, target) || router.current()?.type !== "home") { fail(); return; }
      // This is the public router, NOT --session or an API creation fallback.
      // If the session disappears now, the native session view handles absence.
      router.navigate({ type: "session", sessionID: target.session_id });
      if (target.protocol === "ocq-attention-handoff-v1") {
        const route = router.current();
        if (route?.type !== "session" || route.sessionID !== target.session_id) fail();
      }
    } catch (_) { fail(); }
  },
});
