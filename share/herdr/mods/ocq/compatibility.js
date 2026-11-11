import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { exec as nativeExec, spaces } from "herdr";

export const PROTOCOL = "ocq-herdr-local-v1";
const CLAIMS = "action-contracts-v1-explicit-claims-no-implicit-retry";
const families = {
  compose: ["receive", "preview", "dispatch-intent", "arm", "dispatch", "reconcile", "cancel", "retire"],
  intake: ["implementation-draft", "review-draft", "registration-draft", "receive", "receive-text", "preview", "review-preview", "arm", "dispatch", "reconcile", "cancel", "retire"],
  "notification-ack": ["prepare", "arm", "attempt"], "queued-cancel": ["prepare", "arm", "attempt"],
  "command-resolve": ["receive", "preview", "arm", "attempt"], "agent-reconcile": ["prepare", "arm", "attempt"],
  "command-submit": ["draft", "receive", "preview", "arm", "attempt", "reconcile", "cancel", "retire"],
  "agent-submit": ["draft", "import", "receive", "preview", "arm", "attempt", "reconcile", "cancel", "retire"],
  "result-publish": ["receive", "preview", "arm", "attempt"], capture: ["prepare", "preview", "arm", "attempt"],
  dispatcher: ["prepare", "arm", "attempt", "bind", "cancel", "drain-prepare", "drain-arm", "drain-attempt", "retire", "native-launch"],
};
export const ACTIONS = Object.freeze(Object.fromEntries([["mail-ack", "mail-ack-sequence-read-before-archive-v1"], ...Object.entries(families).flatMap(([f, ops]) => ops.map(op => [`${f}-${op}`, `${f}/${op}/journal-v1`]))]));
const hex = s => /^[0-9a-f]{64}$/.test(s || "");
const same = (a, b) => !!a && !!b && ["id", "machine", "folder"].every(k => a[k] === b[k]);
const active = () => { const s = spaces().find(s => s.active); return s && { id: s.id, machine: s.machine, folder: s.folder }; };
const reads = new Set(["compatibility", "projects", "board", "job", "recent-inbox", "inbox", "mail", "recent-results", "results", "manifest", "verify-worktree", "queue-jobs", "queue-job", "queue-log", "notifications", "notification", "agent-handoff-status", "agent-handoff-verify"]);
for (const action of ["compose-context", "compose-status", "compose-rendezvous", "intake-status", "intake-rendezvous", "notification-ack-status", "queued-cancel-status", "command-resolve-status", "command-resolve-rendezvous", "agent-reconcile-status", "command-submit-context", "command-submit-status", "command-submit-rendezvous", "agent-submit-context", "agent-submit-status", "agent-submit-rendezvous", "result-publish-context", "result-publish-list", "result-publish-status", "result-publish-rendezvous", "capture-context", "capture-list", "capture-status", "capture-rendezvous", "dispatcher-context", "dispatcher-status", "dispatcher-read"]) reads.add(action);
function read(action) { return reads.has(action); }

export const compatibility = {
  epoch: 0,
  observe() {
    const s = active();
    if (!same(s, this.scope)) { this.scope = s; this.epoch++; this.status = null; this.pin = null; this.pending = false; this.reason = s?.machine != null ? "Remote mutations disabled. Compatibility checks are local-only; stored reads remain available." : "Mutations disabled: matched bridge not checked in this selected context."; }
    return s;
  },
  ready() { const s = this.observe(); return !!s && s.machine === null && !!this.status && hex(this.status.queue_identity); },
  transfer(current) {
    const status = this.status, epoch = this.epoch, serial = this.serial;
    // A new check (even a successful one for the same image) cannot revive an
    // exchange whose receiver was launched under an earlier authorization.
    const valid = () => current() && this.ready() && this.status === status && this.epoch === epoch && this.serial === serial;
    if (!valid()) throw Error("Private transfer requires current compatibility");
    return valid;
  },
  validate(v, s) {
    if (!v || v.protocol !== PROTOCOL || v.journal_claims !== CLAIMS || v.build_kind !== "local-development" || !hex(v.image_sha256) || !hex(v.bridge_context) || v.space_id !== s.id || v.folder !== s.folder || !v.actions || Object.keys(v.actions).length !== Object.keys(ACTIONS).length || Object.entries(ACTIONS).some(([a, value]) => v.actions[a] !== value) || (v.queue_identity !== "" && !hex(v.queue_identity))) throw Error("Bridge protocol, action semantics or context mismatch");
    return v;
  },
  async check(current = () => true) {
    const s = this.observe(), epoch = this.epoch;
    if (!s || s.machine !== null || !current()) throw Error("Compatibility is local-only and context-bound");
    const serial = this.serial = (this.serial || 0) + 1;
    this.status = null;
    this.pending = true;
    try {
      const r = await nativeExec("ocq-herdr", ["compatibility", s.id, s.folder], { on: null, timeout: 5000 });
      if (!same(this.observe(), s) || this.epoch !== epoch || this.serial !== serial || !current()) throw Error("Compatibility context changed");
      if (r.code !== 0) throw Error("Bridge compatibility endpoint missing or unavailable");
      const checked = this.validate(JSON.parse(r.stdout), s);
      // Never rebind already-loaded project/mail/intake views to a different
      // queue or bridge incarnation just because Recheck was clicked. A fresh
      // panel/selected context also resets those views and their old callbacks.
      const identity = ["image_sha256", "bridge_context", "queue_identity"];
      if (this.pin && identity.some(k => checked[k] !== this.pin[k])) throw Error("Bridge incarnation changed; reopen after quiescing");
      if (!this.pin && hex(checked.queue_identity)) this.pin = Object.freeze(Object.fromEntries(identity.map(k => [k, checked[k]])));
      this.status = checked;
      this.reason = this.status.queue_identity ? "Matched local bridge/action contracts. Each action rechecks image/context; this does not lock executable replacement." : "Matched bridge, but no supported existing queue. Mutations disabled; nothing initialized.";
      return this.status;
    } catch (_e) {
      if (same(this.observe(), s) && this.epoch === epoch && this.serial === serial) { this.status = null; this.reason = "Mutations disabled: bridge absent, malformed, incompatible, timed out or changed. Recheck reads only; if image/queue/context changed, quiesce and reopen in a fresh selected context."; }
      throw Error("Bridge compatibility unavailable; no mutation attempted");
    } finally { if (this.epoch === epoch && this.serial === serial) this.pending = false; }
  },
  authorize(action, args, current) {
    if (typeof current !== "function" || !current() || !this.ready() || !ACTIONS[action]) throw Error("Compatible bridge and exact current action context required; mutations disabled");
    const s = this.scope, v = this.status;
    if (["notification-ack-", "queued-cancel-", "command-resolve-", "agent-reconcile-", "command-submit-", "agent-submit-", "result-publish-", "capture-", "dispatcher-"].some(f => action.startsWith(f)) && (args[0] !== s.id || args[1] !== v.queue_identity)) throw Error("Action queue/space differs from matched bridge context");
    return ["--mod-contract", PROTOCOL, "--mod-action", ACTIONS[action], "--mod-image", v.image_sha256, "--mod-context", v.bridge_context, "--mod-space", s.id, "--mod-folder", s.folder, "--mod-queue", v.queue_identity];
  },
  async native(queue, image, current) {
    const before = this.status;
    if (!before || before.image_sha256 !== image) throw Error("Native launch compatibility unavailable");
    const next = await this.check(current);
    if (!current() || next.image_sha256 !== before.image_sha256 || next.bridge_context !== before.bridge_context) { this.status = null; throw Error("Bridge changed before native launch; no launch retry"); }
    this.authorize("dispatcher-native-launch", [this.scope.id, queue], current);
  },
  notice(cx, theme) {
    this.observe();
    const epoch = this.epoch;
    if (this.scope?.machine === null && this.startedEpoch !== epoch) {
      this.startedEpoch = epoch;
      cx.spawn(async cx => { try { await this.check(() => this.epoch === epoch); } catch (_) {} cx.notify(); });
    }
    return v_flex().flex_none().p(8).gap(4).text_size(12).text_color(this.ready() ? theme.muted : theme.ansi1)
      .child(div().child(this.reason || "Mutations disabled until matched compatibility check."))
      .child(div().id("ocq-compatibility-check").role("button").cursor_pointer().child(this.pending ? "Checking bridge (read only)…" : "Recheck matched bridge (read only)").on_click((_e, cx) => {
        if (this.pending || epoch !== this.epoch) return;
        cx.spawn(async cx => { try { await this.check(() => this.epoch === epoch); } catch (_) {} cx.notify(); }); cx.notify();
      }));
  },
};

// Read-only commands deliberately retain their existing DTO validation. Unknown
// actions fail closed; every mutation requires a caller's ORIGINAL live guard.
export async function exec(bin, args, options = {}) {
  const { current, ...transport } = options;
  if (bin !== "ocq-herdr" || read(args[0])) return nativeExec(bin, args, transport);
  if (options.on !== null) throw Error("Remote mutations disabled");
  const flags = compatibility.authorize(args[0], args.slice(1), current);
  const status = compatibility.status;
  if (!current()) throw Error("Context changed before bridge mutation");
  const r = await nativeExec(bin, [...args, ...flags], transport);
  if (r.code !== 0 && compatibility.status === status) { compatibility.status = null; compatibility.reason = "Bridge action refused or response lost. Mutations disabled; read stored receipt only, never retry."; }
  return r;
}

// Conservative UI default: unknown controls stay disabled while unmatched.
// Keyboard paths are independently fenced at exec/native boundaries above.
const readButtons = new Set(["ocq-queue-refresh", "ocq-queue-next", "ocq-queue-first", "ocq-queue-back", "ocq-queue-lookup-job", "ocq-queue-lookup-event", "ocq-queue-job-event", "ocq-queue-event-job", "ocq-queue-show-summary", "ocq-queue-projects", "ocq-refresh", "ocq-queue", "ocq-projects", "ocq-project-overview", "ocq-return-board", "ocq-manifest-original", "ocq-manifest-fields", "ocq-results-more", "ocq-results-reload", "ocq-inbox-more", "ocq-inbox-reload", "ocq-inspection-check-mail"]);
export function enabledButton(id) { return compatibility.ready() || id === "ocq-open-queue" || id === "ocq-dispatch-read" || readButtons.has(id) || ["-check", "-status", "-back", "-close", "-refresh"].some(s => id.endsWith(s)) || ["ocq-queue-job-", "ocq-queue-event-", "ocq-agent-handoff-", "ocq-log-", "ocq-project-", "ocq-lane-", "ocq-read-", "ocq-check-mail-", "ocq-attachment-", "ocq-result-manifest-", "ocq-result-check-", "ocq-page-", "ocq-worktree-"].some(p => id.startsWith(p)); }
