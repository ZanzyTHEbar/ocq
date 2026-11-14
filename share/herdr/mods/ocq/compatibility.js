import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { exec as nativeExec, spaces } from "herdr";
import { panelEpoch, panelVisible } from "./panel-state.js";

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
export const ACTIONS = Object.freeze(Object.fromEntries([["mail-ack", "mail-ack-sequence-read-before-archive-v1"], ["work-confirm", "ocq-work-v1"],
  ["main-start", "ocq-main-v1"], ["client-prepare", "ocq-client-v1/prepare"], ["client-open", "ocq-client-v1/open"],
  ...Object.entries(families).flatMap(([f, ops]) => ops.map(op => [`${f}-${op}`, `${f}/${op}/journal-v1`]))]));
const hex = s => /^[0-9a-f]{64}$/.test(s || "");
const same = (a, b) => !!a && !!b && ["id", "machine", "folder"].every(k => a[k] === b[k]);
const active = () => { const s = spaces().find(s => s.active); return s && { id: s.id, machine: s.machine, folder: s.folder }; };
const localSpace = s => s.machine !== null && s.id.startsWith(`${s.machine}/`) ? s.id.slice(s.machine.length + 1) : s.id;
const reads = new Set(["view-snapshot", "watch-wait", "compatibility", "projects", "board", "job", "recent-inbox", "inbox", "mail", "recent-results", "results", "manifest", "verify-worktree", "queue-jobs", "queue-job", "queue-log", "notifications", "notification", "agent-handoff-status", "agent-handoff-verify"]);
for (const action of ["compose-context", "compose-status", "compose-rendezvous", "intake-status", "intake-rendezvous", "notification-ack-status", "queued-cancel-status", "command-resolve-status", "command-resolve-rendezvous", "agent-reconcile-status", "command-submit-context", "command-submit-status", "command-submit-rendezvous", "agent-submit-context", "agent-submit-status", "agent-submit-rendezvous", "result-publish-context", "result-publish-list", "result-publish-status", "result-publish-rendezvous", "capture-context", "capture-list", "capture-status", "capture-rendezvous", "dispatcher-context", "dispatcher-status", "dispatcher-read"]) reads.add(action);
// Preview writes retained private review evidence, but never claims/submits.
for (const action of ["work-list", "work-status", "work-recover", "work-output", "work-handoff", "work-preview"]) reads.add(action);
for (const action of ["main-list", "main-status", "main-recover", "main-preview", "client-list", "client-status", "client-verify"]) reads.add(action);
function read(action) { return reads.has(action); }

export const compatibility = {
  epoch: 0,
  observe() {
    const s = active();
    if (!same(s, this.scope)) { this.scope = s; this.epoch++; this.status = null; this.pin = null; this.pending = false; this.observationBlocked = false; this.legacyObservationBlocked = false; this.reason = s?.machine != null ? "Legacy remote mutations disabled. Prepared-work/Main/client actions require their own matched peer/context review; stored reads remain available." : "Mutations disabled: matched bridge not checked in this selected context."; }
    const marker = panelEpoch();
    if (this.panelMarker !== undefined && marker !== this.panelMarker) {
      this.epoch++; this.serial = (this.serial || 0) + 1; this.status = null; this.pending = false;
      this.reopenAuthorization = true;
      this.reason = "OCQ panel visibility changed. Prior confirmations are suspended; inspect and review again.";
    }
    this.panelMarker = marker;
    return s;
  },
  freshPanel(marker) {
    // Only the panel owner calls this AFTER resetting its retained authority.
    // Hide/Recheck alone must not rebind an already-loaded context.
    this.observe();
    if (!panelVisible() || marker !== this.panelMarker || this.freshPanelMarker === marker) return;
    this.epoch++; this.serial = (this.serial || 0) + 1; this.status = null; this.pending = false;
    this.pin = null; this.freshPanelMarker = marker; this.reopenAuthorization = true;
  },
  ready() { const s = this.observe(); return panelVisible() && !this.observationBlocked && !this.legacyObservationBlocked && !!s && s.machine === null && !!this.status && hex(this.status.queue_identity) && this.status.observation_schema === 6 && hex(this.status.observation_context) && hex(this.status.observation_store_context); },
  workReady() { const s = this.observe(); return panelVisible() && !this.observationBlocked && !!s && !!this.status && hex(this.status.queue_identity) && this.status.observation_schema === 6 && hex(this.status.observation_context) && hex(this.status.observation_store_context); },
  invalidateObservation(reason) {
    this.observe();
    if (this.observationBlocked) return;
    this.observationBlocked = true; this.legacyObservationBlocked = true; this.status = null; this.pending = false;
    this.epoch++; this.serial = (this.serial || 0) + 1; this.startedEpoch = this.epoch;
    this.reason = `${reason} Quiesce and reopen a fresh selected context before local actions; observational reconnect never restores authorization.`;
  },
  transfer(current) {
    const status = this.status, epoch = this.epoch, serial = this.serial;
    // A new check (even a successful one for the same image) cannot revive an
    // exchange whose receiver was launched under an earlier authorization.
    const valid = () => current() && this.ready() && this.status === status && this.epoch === epoch && this.serial === serial;
    if (!valid()) throw Error("Private transfer requires current compatibility");
    return valid;
  },
  validate(v, s) {
    if (!v || v.protocol !== PROTOCOL || v.journal_claims !== CLAIMS || v.build_kind !== "local-development" || !hex(v.image_sha256) || !hex(v.bridge_context) || v.space_id !== localSpace(s) || v.folder !== s.folder || !v.actions || Object.keys(v.actions).length !== Object.keys(ACTIONS).length || Object.entries(ACTIONS).some(([a, value]) => v.actions[a] !== value) || (v.queue_identity !== "" && !hex(v.queue_identity))) throw Error("Bridge protocol, action semantics or context mismatch");
    return v;
  },
  async check(current = () => true, workOnly = false, freshAuthorization = false) {
    const s = this.observe();
    if (!s || (!workOnly && s.machine !== null) || !current()) throw Error("Compatibility context unavailable");
    if (freshAuthorization) { this.epoch++; this.startedEpoch = this.epoch; }
    const epoch = this.epoch;
    const serial = this.serial = (this.serial || 0) + 1;
    this.status = null;
    this.pending = true;
    try {
      const r = await nativeExec("ocq-herdr", ["compatibility", localSpace(s), s.folder], { on: s.machine, timeout: 5000 });
      if (!same(this.observe(), s) || this.epoch !== epoch || this.serial !== serial || !current()) throw Error("Compatibility context changed");
      if (r.code !== 0) throw Error("Bridge compatibility endpoint missing or unavailable");
      const checked = this.validate(JSON.parse(r.stdout), s);
      // Never rebind already-loaded project/mail/intake views to a different
      // queue or bridge incarnation just because Recheck was clicked. A fresh
      // panel/selected context also resets those views and their old callbacks.
       const identity = ["image_sha256", "bridge_context", "queue_identity", "observation_context", "observation_store_context", "observation_schema"];
       if (!hex(checked.observation_context) || !hex(checked.observation_store_context) || !Number.isInteger(checked.observation_schema)) throw Error("Observation descriptor unavailable");
       if (this.pin && identity.some(k => checked[k] !== this.pin[k])) {
         this.invalidateObservation("Verified bridge/store/schema incarnation changed; retained reviews have no authority.");
         throw Error("Bridge incarnation changed; reopen after quiescing");
       }
      if (!this.pin && hex(checked.queue_identity)) this.pin = Object.freeze(Object.fromEntries(identity.map(k => [k, checked[k]])));
      this.status = checked;
      if (freshAuthorization) {
        this.observationBlocked = false;
        // Reopening has already reset the owner's legacy private generations.
        // A pointer-only recheck must not revive retained legacy confirmations.
        if (this.reopenAuthorization) this.legacyObservationBlocked = false;
        this.reopenAuthorization = false;
      }
      this.reason = this.observationBlocked ? "Local actions remain suspended after an observation reset/disconnect. Quiesce and reopen a fresh selected context; rechecking does not revive old confirmations." : this.status.observation_schema !== 6 ? "This automatic workspace requires the matched schema-6 observation descriptor. Local actions disabled; nothing initialized or migrated." : this.status.queue_identity ? "Matched local bridge/action contracts. Each action rechecks image/context; this does not lock executable replacement." : "Matched bridge, but no supported existing queue. Mutations disabled; nothing initialized.";
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
  authorizeWork(args, options, action = "work-confirm") {
    const lengths = { "work-confirm": 9, "main-start": 9, "client-prepare": 10, "client-open": 8 };
    if (typeof options.current !== "function" || !options.current() || !this.workReady() ||
      !lengths[action] || options.on !== this.scope.machine || args.length !== lengths[action] || args[0] !== localSpace(this.scope) ||
      args[1] !== this.status.queue_identity || args[4] !== (this.scope.machine ?? "local") ||
      args[5] !== this.scope.folder || args[6] !== this.status.observation_store_context ||
      args[7] !== this.status.image_sha256 ||
      (["work-confirm", "main-start"].includes(action) && !hex(args[8])) ||
      (action === "client-open" && !hex(args[3])) ||
      (action === "client-prepare" && (!/^[A-Za-z0-9_-]{1,64}$/.test(args[3]) ||
        !["worker", "integrator"].includes(args[8]) || (args[8] === "integrator" ? args[9] !== "" : !/^[A-Za-z0-9_-]{1,64}$/.test(args[9])))))
      throw Error("Exact versioned pointer-action context required");
    const s = this.scope, v = this.status;
    return ["--mod-contract", PROTOCOL, "--mod-action", ACTIONS[action], "--mod-image", v.image_sha256,
      "--mod-context", v.bridge_context, "--mod-space", localSpace(s), "--mod-folder", s.folder, "--mod-queue", v.queue_identity,
      "--work-observation-context", v.observation_context];
  },
  async native(queue, image, current) {
    const before = this.status;
    if (!before || before.image_sha256 !== image) throw Error("Native launch compatibility unavailable");
    const next = await this.check(current);
    if (!current() || next.image_sha256 !== before.image_sha256 || next.bridge_context !== before.bridge_context) { this.status = null; throw Error("Bridge changed before native launch; no launch retry"); }
    this.authorize("dispatcher-native-launch", [this.scope.id, queue], current);
  },
  notice(cx, theme, register = (_id, _label, action) => (_e, cx) => action(cx)) {
    this.observe();
    const epoch = this.epoch;
    const scope = this.scope;
    if (this.scope?.machine === null && this.startedEpoch !== epoch) {
      this.startedEpoch = epoch;
      cx.spawn(async cx => { try { await this.check(() => same(active(), scope), false, Boolean(this.reopenAuthorization)); } catch (_) {} cx.notify(); });
    }
    return v_flex().flex_none().px(10).py(4).gap(4).text_size(12).text_color(this.ready() ? theme.muted : theme.ansi1)
      .when(this.scope?.machine === null, e => e
      .child(div().id("ocq-compatibility-details").role("button").accessibility_label("Bridge compatibility details").cursor_pointer()
        .on_click(register("ocq-compatibility-details", "Bridge compatibility details", cx => { this.details = !this.details; cx.notify(); }))
        .child(this.ready() ? "Local actions available · bridge details" : this.pending ? "Checking local bridge…" : "Local actions unavailable · bridge details"))
      .when(Boolean(this.details), e => e
      .child(div().child(this.reason || "Mutations disabled until matched compatibility check."))
      .child(div().id("ocq-compatibility-check").role("button").accessibility_label("Recheck matched bridge, read only").cursor_pointer().child(this.pending ? "Checking bridge (read only)…" : "Recheck matched bridge (read only)").on_click(register("ocq-compatibility-check", "Recheck matched bridge, read only", cx => {
        if (this.pending || epoch !== this.epoch) return;
        cx.spawn(async cx => { try { await this.check(() => same(active(), scope), false, true); } catch (_) {} cx.notify(); }); cx.notify();
      })))));
  },
};

// Read-only commands deliberately retain their existing DTO validation. Unknown
// actions fail closed; every mutation requires a caller's ORIGINAL live guard.
export async function exec(bin, args, options = {}) {
  const { current, ...transport } = options;
  const observationRead = args.length === 6 && args[0] === "--mod-space" && typeof args[1] === "string" && args[2] === "--mod-folder" && typeof args[3] === "string" && ["view-snapshot", "watch-wait"].includes(args[4]) && typeof args[5] === "string";
  if (bin !== "ocq-herdr" || read(args[0]) || observationRead) return nativeExec(bin, args, transport);
  if (["work-confirm", "main-start", "client-prepare", "client-open"].includes(args[0])) {
    const flags = compatibility.authorizeWork(args.slice(1), options, args[0]);
    try {
      const response = await nativeExec(bin, [args[0], ...flags, "--", ...args.slice(1)], transport);
      if (response.code !== 0) compatibility.invalidateObservation("Prepared-work response refused or unavailable; retained attempts require exact read-only recovery.");
      return response;
    } catch (error) {
      compatibility.invalidateObservation("Prepared-work transport outcome unknown; retained attempts require exact read-only recovery.");
      throw error;
    }
  }
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
export function enabledButton(id) { return compatibility.ready() || ["ocq-work-", "ocq-main-", "ocq-client-"].some(prefix => id.startsWith(prefix)) || id === "ocq-open-queue" || id === "ocq-dispatch-read" || readButtons.has(id) || ["-check", "-status", "-back", "-close", "-refresh"].some(s => id.endsWith(s)) || ["ocq-queue-job-", "ocq-queue-event-", "ocq-agent-handoff-", "ocq-log-", "ocq-project-", "ocq-lane-", "ocq-read-", "ocq-check-mail-", "ocq-attachment-", "ocq-result-manifest-", "ocq-result-check-", "ocq-page-", "ocq-worktree-"].some(p => id.startsWith(p)); }
