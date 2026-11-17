import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { spaces } from "herdr";
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
  ["capture-receipt-preview", "capture/recorded-receipt-preview/v1"],
  ...Object.entries(families).flatMap(([f, ops]) => ops.map(op => [`${f}-${op}`, `${f}/${op}/journal-v1`]))]));
const hex = s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s);
const unavailable = () => { throw Error("Native client/dispatcher execution requires an explicitly pinned HC host/core; this socket has no such launch authority. No native call, helper rendezvous or exec fallback was invoked."); };
const localSpace = s => s.machine !== null && s.id.startsWith(`${s.machine}/`) ? s.id.slice(s.machine.length + 1) : s.id;
const pointerActions = new Set(["work-confirm", "main-start", "client-prepare", "client-open"]);
const supported = action => pointerActions.has(action) || action === "mail-ack" ||
  ["compose-", "intake-", "notification-ack-", "queued-cancel-", "command-resolve-", "agent-reconcile-", "command-submit-", "agent-submit-", "result-publish-", "capture-", "dispatcher-"].some(f => action.startsWith(f)) && action !== "dispatcher-native-launch" && !!ACTIONS[action];

// A descriptor proves the read protocol/context, NEVER mutation authority.
// Keep the original caller-side review/attempt fences intact. Endpoint opt-in
// enables fresh reviews, never an old confirmation or an automatic attempt.
export const compatibility = {
  epoch: 0, serial: 0,
  observe() {
    const s = spaces().find(s => s.active);
    const scope = s && { id: s.id, machine: s.machine, folder: s.folder };
    const marker = panelEpoch();
    if (JSON.stringify(scope) !== JSON.stringify(this.scope) || marker !== this.panelMarker) {
      this.scope = scope; this.panelMarker = marker;
      this.invalidateObservation("Selected context or panel changed. Reconnect explicitly.");
    }
    return scope;
  },
  freshPanel() { this.invalidateObservation("Panel changed. Prior confirmations are invalid."); },
  invalidateObservation(reason) {
    this.status = null; this.pending = false; this.pin = null; this.recovery = null;
    this.epoch++; this.serial++;
    this.allowActions = false;
    this.reason = reason;
  },
  ready() { const s = this.observe(); return this.workReady() && s?.machine === null; },
  workReady() { this.observe(); return panelVisible() && !!this.status && this.allowActions === true && this.pushConnection?.state === "connected" && this.pushConnection.updates.current(); },
  nativeReady() { return this.ready() && !!this.hc?.approvedDigest(); },
  transfer(current) {
    const status = this.status, epoch = this.epoch, serial = this.serial;
    const valid = () => current() && this.ready() && this.status === status && this.epoch === epoch && this.serial === serial;
    if (!valid()) throw Error("Current reviewed local action context required");
    return valid;
  },
  async check(current = () => true, workOnly = false, fresh = false) {
    const s = this.observe(), before = this.status, connection = this.pushConnection;
    if (!s || !before || !current() || connection?.state !== "connected" || !workOnly && s.machine !== null) throw Error("Connect selected context explicitly");
    if (fresh) this.epoch++;
    const epoch = this.epoch, serial = ++this.serial;
    const result = await connection.command(["compatibility", localSpace(s), s.folder]);
    if (!current() || this.epoch !== epoch || this.serial !== serial || result.code !== 0) throw Error("Compatibility changed");
    const checked = this.validate(JSON.parse(result.stdout), s);
    if (["image_sha256", "bridge_context", "queue_identity", "observation_context", "observation_store_context", "observation_schema"].some(k => checked[k] !== before[k])) {
      this.invalidateObservation("Endpoint incarnation changed. Disconnect and inspect before reconnecting.");
      throw Error("Endpoint incarnation changed");
    }
    this.status = checked;
    return checked;
  },
  authorize(action, args, current) {
    if (typeof current !== "function" || !current() || !this.ready() || !supported(action)) throw Error("Fresh exact local action authority required");
    const s = this.scope, v = this.status;
    if (["notification-ack-", "queued-cancel-", "command-resolve-", "agent-reconcile-", "command-submit-", "agent-submit-", "result-publish-", "capture-", "dispatcher-"].some(f => action.startsWith(f)) &&
      (args[0] !== localSpace(s) || args[1] !== v.queue_identity)) throw Error("Action queue/space changed");
    return ["--mod-contract", PROTOCOL, "--mod-action", ACTIONS[action], "--mod-image", v.image_sha256, "--mod-context", v.bridge_context, "--mod-space", localSpace(s), "--mod-folder", s.folder, "--mod-queue", v.queue_identity];
  },
  authorizeWork(args, options, action) {
    const lengths = { "work-confirm": 9, "main-start": 9, "client-prepare": 10, "client-open": 8 };
    const s = this.scope, v = this.status;
    if (!pointerActions.has(action) || typeof options.current !== "function" || !options.current() || !this.workReady() ||
      options.on !== s.machine || args.length !== lengths[action] || args[0] !== localSpace(s) || args[1] !== v.queue_identity ||
      args[4] !== (s.machine ?? "local") || args[5] !== s.folder || args[6] !== v.observation_store_context || args[7] !== v.image_sha256 ||
      (action === "client-open" ? !hex(args[3]) : action !== "client-prepare" && !hex(args[8])) ||
      (action === "client-prepare" && (!/^[A-Za-z0-9_-]{1,64}$/.test(args[3]) || !["worker", "integrator"].includes(args[8]) || (args[8] === "integrator" ? args[9] !== "" : !/^[A-Za-z0-9_-]{1,64}$/.test(args[9])))))
      throw Error("Exact versioned pointer-action context required");
    return ["--mod-contract", PROTOCOL, "--mod-action", ACTIONS[action], "--mod-image", v.image_sha256, "--mod-context", v.bridge_context,
      "--mod-space", localSpace(s), "--mod-folder", s.folder, "--mod-queue", v.queue_identity, "--work-observation-context", v.observation_context];
  },
  native: async () => unavailable(),
  async exchange(action, args, payload, current) {
    const flags = this.authorize(action, args, current);
    const result = await this.pushConnection.command([action, ...args, ...flags], payload === null ? undefined : payload);
    if (!current() || result.code !== 0) throw Error("Private exchange unavailable or uncertain; inspect exact retained status");
    return JSON.parse(result.stdout);
  },
  validate(v, s) {
    const space = s.machine !== null && s.id.startsWith(`${s.machine}/`) ? s.id.slice(s.machine.length + 1) : s.id;
    if (!v || v.protocol !== PROTOCOL || v.journal_claims !== CLAIMS || v.build_kind !== "local-development" ||
      !hex(v.image_sha256) || !hex(v.bridge_context) || v.space_id !== space || v.folder !== s.folder ||
      !v.actions || Object.keys(v.actions).length !== Object.keys(ACTIONS).length ||
      Object.entries(ACTIONS).some(([a, value]) => v.actions[a] !== value) || !hex(v.queue_identity))
      throw Error("Endpoint read protocol or explicit context mismatch");
    return v;
  },
  notice(_cx, theme) {
    return v_flex().px(10).py(4).text_size(12).text_color(theme.muted)
      .child(div().child(this.allowActions ? "Reviewed socket actions. Native operations additionally require the exact reviewed private HC binding." : "Read-only endpoint · stored reads and explicit read-only verification remain available; owner opt-in is required for reviewed actions."))
      .child(div().child(this.reason || "Configure an explicit endpoint. HC machine names do not supply a socket route."));
  },
};

// Legacy helpers use the selected socket's explicit command allowlist. They
// cannot fall back to execution, polling or a separately provisioned listener.
export async function exec(bin, args, options = {}) {
  compatibility.observe();
  const connection = compatibility.pushConnection;
  if (bin !== "ocq-herdr" || options.on !== compatibility.scope?.machine || connection?.state !== "connected") return unavailable();
  let request = args;
  const native = args[0] === "client-open" || args[0] === "client-verify" || args[0] === "dispatcher-status" ||
    args[0].startsWith("dispatcher-") && ACTIONS[args[0]];
  if (native && (!compatibility.nativeReady() || options.hcBinding !== compatibility.hc.approvedDigest() ||
      typeof options.current !== "function" || !options.current())) throw Error("Exact reviewed private HC binding required");
  const hc = native ? ["--hc-binding-sha256", options.hcBinding] : [];
  if (ACTIONS[args[0]]) {
    const flags = pointerActions.has(args[0]) ? compatibility.authorizeWork(args.slice(1), options, args[0]) :
      compatibility.authorize(args[0], args.slice(1), options.current);
    request = pointerActions.has(args[0]) ? [args[0], ...flags, ...hc, "--", ...args.slice(1)] : [...args, ...flags, ...hc];
  } else if (native) {
    const s = compatibility.scope;
    request = [args[0], "--mod-space", localSpace(s), "--mod-folder", s.folder, ...hc, ...args.slice(1)];
  }
  return connection.command(request); // Never retries; transport loss is uncertain.
}

const readButtons = new Set(["ocq-queue-refresh", "ocq-queue-next", "ocq-queue-first", "ocq-queue-back",
  "ocq-queue-lookup-job", "ocq-queue-lookup-event", "ocq-queue-job-event", "ocq-queue-event-job",
  "ocq-queue-show-summary", "ocq-queue-projects", "ocq-refresh", "ocq-queue", "ocq-projects",
  "ocq-project-overview", "ocq-return-board", "ocq-manifest-original", "ocq-manifest-fields",
  "ocq-results-more", "ocq-results-reload", "ocq-inbox-more", "ocq-inbox-reload", "ocq-open-queue",
   "ocq-work-job", "ocq-work-result", "ocq-work-status", "ocq-work-recover", "ocq-work-output", "ocq-work-handoff",
   "ocq-work-output-previous", "ocq-work-output-next", "ocq-inspection-check-mail",
    "ocq-main-observe", "ocq-main-recover", "ocq-main-review-previous", "ocq-main-review-next",
    "ocq-agent-handoff-stored", "ocq-agent-handoff-copy", "ocq-agent-handoff-verify",
   "ocq-agent-reconcile-back", "ocq-agent-reconcile-check", "ocq-dispatch-read", "ocq-dispatch-close", "ocq-dispatch-back-confirm"]);
export function enabledButton(id) {
  // Reading/recovery never borrows mutation readiness, including after an
  // uncertain attempt reconnects to a default read-only endpoint.
  if (readButtons.has(id) || id.startsWith("ocq-result-check-") || id.startsWith("ocq-client-record-")) return true;
  if (id === "ocq-main-open" || id === "ocq-client-worker-new" || /^ocq-(client-(open|verify|show)|dispatch-)/.test(id)) return compatibility.nativeReady();
  if (compatibility.ready() || compatibility.workReady() && /^(ocq-work-|ocq-main-|ocq-client-prepare)/.test(id)) return true;
  return ["ocq-work-select-", "ocq-project-job-", "ocq-queue-job-", "ocq-queue-event-",
    "ocq-log-", "ocq-project-", "ocq-lane-", "ocq-read-", "ocq-attachment-", "ocq-result-manifest-", "ocq-page-"].some(p => id.startsWith(p));
}
