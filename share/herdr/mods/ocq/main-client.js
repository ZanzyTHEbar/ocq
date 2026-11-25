import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { run, terminals, spaces, panes } from "herdr";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { decimal } from "./stored-updates.js";
import { mainAction, mainSummary, roleSummary, technicalDetails } from "./action-review.js";
import { panelEpoch } from "./panel-state.js";

const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9_-]{1,128}$/.test(value);
const object = value => value && typeof value === "object" && !Array.isArray(value);
const pins = value => JSON.stringify(["image_sha256", "bridge_context", "queue_identity", "observation_context", "observation_store_context", "observation_schema"].map(key => value[key]));
const identity = value => JSON.stringify(value && Object.fromEntries(Object.entries(value).filter(([key]) => key !== "read_at")));

function mainStatus(value, project, operation) {
  if (!object(value) || value.protocol !== "ocq-main-v1" || value.project_id !== project || !id(value.operation_id) ||
    operation && value.operation_id !== operation || !["create", "adopt", "refresh", "role-refresh", "send", "reopen"].includes(value.mode) ||
    !sessionID(value.session_id) || !id(value.setup_id) || !hex(value.intent_sha256) || !decimal(value.binding_revision) ||
    !hex(value.ownership_sha256) || !hex(value.profile_sha256) ||
    ["state", "reason", "repo", "main_branch", "head", "commit_anchor", "opencode_project_id", "ownership_mode", "read_at"].some(key => typeof value[key] !== "string") ||
    typeof value.dirty !== "boolean" || typeof value.head_advanced !== "boolean") throw Error("Invalid exact Main status");
  return value;
}

function pointer(value, scope) {
  if (!object(value) || !["ocq-client-v1", "ocq-attention-client-v1"].includes(value.protocol) || value.project_id !== scope.project || !hex(value.pointer_sha256) ||
    !id(value.navigation_id) || !["worker", "integrator"].includes(value.target_kind) || !sessionID(value.session_id) ||
    value.queue_identity !== scope.queue || value.store_context !== scope.store || !/^[a-z0-9]+$/.test(value.space || "") ||
    !decimal(value.binding_revision) || !hex(value.binding_sha256) ||
    ["service_identity", "ownership_sha256", "profile_sha256", "launcher_sha256", "opencode_sha256", "asset_sha256", "overlay_sha256"].some(key => !hex(value[key])) ||
    typeof value.cwd !== "string" || !value.cwd.startsWith("/") || typeof value.server_origin !== "string" ||
    !Array.isArray(value.launcher_argv) || value.launcher_argv.length !== 8 || value.launcher_argv.some(arg => typeof arg !== "string") ||
    value.launcher_argv[7] !== value.pointer_sha256 || value.options?.session_id !== value.session_id ||
    value.options.target_kind !== value.target_kind || value.options.project_id !== scope.project ||
    value.options.ownership_sha256 !== value.ownership_sha256 || value.options.profile_sha256 !== value.profile_sha256) throw Error("Invalid retained native client pointer");
  return value;
}

function clientStatus(value, scope, digest) {
  if (!object(value) || value.protocol !== "ocq-client-v1" || value.project_id !== scope.project ||
    !hex(value.pointer_sha256) || digest && value.pointer_sha256 !== digest ||
    !["ready_to_open", "armed", "attempted_unknown", "terminal_recorded", "blocked"].includes(value.state) ||
    typeof value.attempt_permitted !== "boolean" || value.current_route !== "unverified") throw Error("Invalid exact client record");
  pointer(value.pointer, scope);
  if (value.pointer.pointer_sha256 !== value.pointer_sha256) throw Error("Different client pointer");
  const terminal = value.terminal;
  if (terminal && (!/^t[1-9][0-9bcdfghjkmnpqrstvwxyz]*$/.test(terminal.returned_terminal_id || "") ||
    terminal.returned_terminal_id !== terminal.observed_terminal_id || typeof terminal.peer !== "string" ||
    terminal.cwd !== value.pointer.cwd || JSON.stringify(terminal.argv) !== JSON.stringify(value.pointer.launcher_argv) ||
    terminal.labels?.["ocq.handoff"] !== value.pointer_sha256)) throw Error("Invalid native terminal receipt");
  return value;
}

// Main is not a worker. This surface only consumes server-authored setup/send
// intents and explicit native navigation pointers; no prompt/profile upload.
export class MainClientControls {
  constructor(host) {
    this.host = host; this.generation = 0; this.mains = []; this.clients = []; this.retainedViewers = new Map();
    this.compatibility = host.compatibility || compatibility;
    this.command = (args, options) => bridgeCommand(args, { ...options, compatibility: this.compatibility });
    try {
      this.attempts = JSON.parse(localStorage.getItem("main-client-attempts-v1") ?? "{}");
      this.navigations = JSON.parse(localStorage.getItem("native-navigation-v1") ?? "{}");
      if (!object(this.attempts) || !object(this.navigations)) throw Error("Invalid private evidence");
    } catch (_) { this.attempts = {}; this.navigations = {}; this.storageError = true; }
  }
  work() { return this.host.workOperator; }
  current(scope, generation = this.generation) { return this.work().current(scope) && this.work().visible() && this.generation === generation; }
  suspend() {
    if (this.goalCheck?.state === "checking") this.goalCheck = { ...this.goalCheck, state: "canceled",
      message: "Status check canceled because the project context changed. No prompt was resent." };
    this.generation++; this.review = null; this.verification = null; this.working = false; this.error = null; this.navigationTarget = null; this.navigationCandidates = null; this.waitingForTerminal = null; this.navigationRecovery = null; this.openIntent = null; this.navigationProof = null;
  }
  sync(scope) {
    const key = JSON.stringify(scope);
    if (key === this.scopeKey) return;
    this.suspend(); this.scopeKey = key; this.scope = scope; this.goalCheck = null;
    if (scope) {
      const retentionScope = JSON.stringify([scope.source ?? scope.machine, scope.space, scope.queue, scope.project, scope.store, scope.folder]);
      if (retentionScope !== this.retentionScope) { this.retainedViewers.clear(); this.retentionScope = retentionScope; }
    }
    this.mains = []; this.clients = []; this.goals = []; this.selectedMain = null; this.operation = null; this.client = null; this.live = null;
  }
  apply(scope, main, clients, cx) {
    this.sync(scope);
    if (main?.protocol !== "ocq-main-v1" || main.project_id !== scope.project || !Array.isArray(main.items) ||
      main.items.length > 128 || !Array.isArray(clients) || clients.length > 128) throw Error("Invalid separate Main/client observation");
    this.mains = main.items.map(item => mainStatus(item, scope.project));
    this.goals = (main.goals || []).map(goal => {
      if (goal.protocol !== "ocq-goal-lineage-v1" || goal.integration_authorized !== false ||
        !Array.isArray(goal.links) || !Array.isArray(goal.work) || goal.work.length !== goal.links.length ||
        goal.main?.project_id !== scope.project || goal.main.mode !== "send" ||
        goal.links.some((link, i) => link.protocol !== goal.protocol || link.operation_id !== goal.main.operation_id ||
          link.prompt_id !== goal.main.prompt_id || link.main_intent_sha256 !== goal.main.intent_sha256 ||
          link.request_id !== goal.work[i]?.request_id || link.work_intent_sha256 !== goal.work[i]?.intent_sha256))
        throw Error("Exact goal lineage unavailable");
      return goal;
    });
    this.selectedMain = main.selected ? mainStatus(main.selected, scope.project) : null;
    this.clients = clients.map(item => clientStatus(item, scope));
    for (const item of this.clients) this.rememberClient(item);
    if (new Set(this.mains.map(item => item.operation_id)).size !== this.mains.length ||
      new Set(this.clients.map(item => item.pointer_sha256)).size !== this.clients.length) throw Error("Duplicate Main/client records");
    if (this.operation) this.operation = this.mains.find(item => item.operation_id === this.operation.operation_id) || null;
    if (this.client) {
      const observed = this.clients.find(item => item.pointer_sha256 === this.client.pointer_sha256);
      // Journal projections are separate/non-atomic. Their absence cannot erase
      // an exact pointer just returned by the owning runtime's preparation.
      if (observed) this.selectClient(observed);
    }
    if (this.review?.kind === "main" && (!this.operation || identity(this.operation) !== identity(this.review.value.status)) ||
      this.review?.kind === "client" && (!this.client || this.client.state !== this.review.value.state)) this.review = null;
    cx.notify();
  }
  base(scope, target) { return [scope.space, scope.queue, scope.project, target]; }
  context(scope, bridge) { return [scope.machine ?? "local", scope.folder, scope.store, bridge.image_sha256]; }
  async call(action, scope, target, contextual = false, tail = [], guard, hcBinding) {
    const bridge = this.compatibility.status;
    const positions = [...this.base(scope, target), ...(contextual ? this.context(scope, bridge) : []), ...tail];
    const mutation = ["main-start", "client-prepare", "client-attention-prepare-v1", "client-open"].includes(action);
    const args = mutation ? [action, ...positions] : [action,
      ...(contextual ? ["--work-observation-context", scope.bridge] : []), "--", ...positions];
    const response = await this.command(args, { on: scope.machine, hcBinding, ...(guard ? { current: guard } : {}) });
    if (response.code !== 0) throw Error(`${action} was refused by the owning runtime (exit ${response.code}).`);
    if (Buffer.from(response.stdout || "").length > 1048576) throw Error(`${action} exceeded the bounded response size.`);
    return JSON.parse(response.stdout);
  }
  async check(scope, generation) {
    const checking = this.compatibility.check(() => this.current(scope, generation), true, true);
    // Pin this check's generation before yielding, including the promise-return
    // gap. A later Recheck cannot lend authority to its earlier response.
    const epoch = this.compatibility.epoch, serial = this.compatibility.serial;
    const bridge = await checking;
    const authorization = { epoch, serial, pins: pins(bridge), bridge };
    if (!this.current(scope, generation) || bridge.queue_identity !== scope.queue ||
      bridge.observation_store_context !== scope.store || bridge.observation_context !== scope.bridge ||
      !this.authorized(authorization, scope, generation)) throw Error("Main/client context changed");
    return authorization;
  }
  authorized(authorization, scope, generation) {
    return this.current(scope, generation) && this.compatibility.workReady() &&
      this.compatibility.epoch === authorization.epoch && this.compatibility.serial === authorization.serial &&
      pins(this.compatibility.status) === authorization.pins;
  }
  freeze(kind, value, scope, generation, authorization) {
    if (!this.authorized(authorization, scope, generation)) throw Error("Review authorization changed");
    const hc = kind === "client" ? this.host.nativeHC.approved : null;
    if (kind === "client" && !this.host.nativeHC.approvedDigest("navigation")) throw Error("Review exact private HC first");
    this.review = { kind, value, scope, generation, pins: authorization.pins, hc,
      epoch: authorization.epoch, serial: authorization.serial,
      pages: this.work().pagesFor(hc ? { client: value, hc_binding: hc.value } : value), page: 0, confirming: false };
  }
  pinned(review) {
    const currentBinding = this.host.board?.project?.current_binding;
    const revision = review?.kind === "main" ? review.value.binding.revision : review?.value.pointer.binding_revision;
    return review && !review.readOnly && this.review === review && this.current(review.scope, review.generation) && this.compatibility.workReady() &&
      (review.kind !== "client" || review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest("navigation")) &&
      this.compatibility.epoch === review.epoch && this.compatibility.serial === review.serial && pins(this.compatibility.status) === review.pins &&
      currentBinding?.repo_root === review.scope.folder && String(currentBinding.revision) === String(revision);
  }
  markerKey(action, scope, target) { return JSON.stringify([action, scope.source ?? scope.machine, scope.space, scope.queue, scope.project, target]); }
  attempted(action, scope, target) {
    const marker = this.attempts[this.markerKey(action, scope, target)];
    return marker && marker.state !== "not_invoked";
  }
  async saveAttempts() {
    localStorage.setItem("main-client-attempts-v1", JSON.stringify(this.attempts));
    await localStorage.flush();
  }
  reviewMain(item, cx) {
    const scope = this.scope, generation = this.generation;
    if (!this.current(scope) || this.working || this.storageError || this.attempted("main-start", scope, item.operation_id)) return;
    this.operation = item; this.review = null; this.live = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const authorization = await this.check(scope, generation);
        if (!this.authorized(authorization, scope, generation)) return;
        const bridge = authorization.bridge;
        const value = await this.call("main-preview", scope, item.operation_id, true);
        if (!this.authorized(authorization, scope, generation)) return;
        mainStatus(value.status, scope.project, item.operation_id);
        const c = value.context;
        const role = value.role;
        if (value.protocol !== "ocq-main-v1" || !hex(value.preview_sha256) || value.status.intent_sha256 !== item.intent_sha256 ||
          role?.protocol !== "ocq-main-role-v1" || role.key !== "ocq-main-role-v1" || role.project_id !== scope.project ||
          role.setup_id !== value.status.setup_id || role.session_id !== value.status.session_id ||
          role.queue_identity !== scope.queue || role.store_context !== scope.store || role.repo !== scope.folder ||
          role.main_branch !== value.status.main_branch || !hex(role.sha256) || !hex(role.cli_sha256) ||
          typeof role.instructions !== "string" || !role.instructions ||
          role.selection?.protocol !== "ocq-main-role-v1" ||
          typeof role.selection.implementation_profile !== "string" || typeof role.selection.review_profile !== "string" ||
          ["input_json", "session_json", "registration_json", "prompt_json"].some(key => typeof value[key] !== "string") ||
          c?.protocol !== "ocq-main-v1" || c.action !== "start" || c.image_role !== "bridge" ||
          c.peer !== (scope.machine ?? "local") || c.space !== scope.space || c.folder !== scope.folder ||
          c.queue_identity !== scope.queue || c.store_context !== scope.store || c.image_sha256 !== bridge.image_sha256 ||
          value.status.repo !== scope.folder || !object(value.binding)) throw Error("Main preview mismatch");
        this.freeze("main", value, scope, generation, authorization);
      } catch (_) { if (this.current(scope, generation)) this.error = "Main preview unavailable or context changed. Nothing started; inspect server-local setup/auth/package state."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  recoverMain(item, cx) {
    const scope = this.scope, generation = this.generation;
    if (this.working) {
      this.error = "Another Main check or navigation is in progress. Wait for it to finish; no prompt was resent.";
      cx.notify(); return;
    }
    const check = { operation: item.operation_id, state: "checking", message: "Checking saved goal status… No prompt is being resent." };
    this.goalCheck = check;
    if (!this.current(scope)) {
      check.state = "canceled"; check.message = "Project context changed. Reopen this project before checking its saved goal. No prompt was resent.";
      cx.notify(); return;
    }
    this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const value = await this.call("main-recover", scope, item.operation_id, false, [],
          () => this.current(scope, generation));
        if (!this.current(scope, generation)) return;
        const observed = mainStatus(value, scope.project, item.operation_id);
        if (observed.intent_sha256 !== item.intent_sha256 || observed.session_id !== item.session_id)
          throw Error("Different saved Main operation");
        this.live = observed;
        check.state = "checked"; check.readAt = observed.read_at;
        check.message = observed.mode === "send" && observed.prompt_id && ["ready", "prompt_pending", "busy"].includes(observed.state)
          ? "Goal received · not proof that the goal is complete. No prompt was resent."
          : observed.state === "prompt_attempted_unknown"
          ? "Saved goal receipt is unverified. Do not resend. Recorded work and review are unchanged."
          : `Saved status: ${observed.state.replaceAll("_", " ")}. No prompt was resent.`;
        if (["blocked", "unknown", "missing_or_unknown", "mismatch", "role_unverified", "prompt_attempted_unknown"].includes(observed.state))
          check.state = "attention";
        this.work().dirty = true;
      } catch (_) {
        if (this.current(scope, generation)) {
          check.state = "error"; check.message = "Saved goal status unavailable. Check this project's connection, then try the read-only check again. No prompt was resent.";
          this.error = check.message;
        }
      } finally {
        if (this.goalCheck === check && check.state === "checking") {
          check.state = "canceled"; check.message = "Status check canceled because the project context changed. No prompt was resent.";
        }
        if (this.current(scope, generation)) this.working = false;
        cx.notify();
      }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  navigationKey(scope, kind, work) { return JSON.stringify([scope.source ?? scope.machine, scope.space, scope.queue, scope.project, kind, work]); }
  navigationOrigin() {
    const active = spaces().find(s => s.active);
    return JSON.stringify([active?.id, active?.tabs.find(t => t.active)?.id]);
  }
  intentCurrent(intent = this.openIntent) {
    return !!intent && intent === this.openIntent && this.current(intent.scope, intent.generation) &&
      panelEpoch() === intent.marker && this.navigationOrigin() === intent.origin &&
      this.targetSession(intent.kind, intent.work) === intent.session;
  }
  proofCurrent(proof = this.navigationProof) {
    return !!proof && this.intentCurrent(proof.intent) && !proof.intent.readOnly &&
      proof.hc === this.host.nativeHC.approved && proof.context === this.host.nativeHC.context() &&
      this.authorized(proof.authorization, proof.intent.scope, proof.intent.generation);
  }
  openConversation(kind, work, cx) {
    const scope = this.work().bound;
    this.sync(scope);
    if (this.working || this.tabNavigation) { cx.notify(); return; }
    this.navigationTarget = { kind, work };
    this.navigationCandidates = null; this.client = null; this.verification = null; this.review = null; this.waitingForTerminal = null; this.navigationRecovery = null;
    this.revealNavigation = true; this.error = null; this.attachmentNotice = null;
    const generation = this.generation;
    this.navigationProof = null;
    this.openIntent = { scope, generation, kind, work, session: this.targetSession(kind, work), marker: panelEpoch(), origin: this.navigationOrigin() };
    if (!this.host.panelCurrent()) { this.error = "The owning project context changed. Reopen the project before opening its conversation."; cx.notify(); return; }
    if (!this.current(scope) || !this.compatibility.workReady()) {
      if (!this.host.initialize) { this.error = "Verify this project connection before opening a conversation."; cx.notify(); return; }
      const project = this.host.selected, sourceGeneration = this.host.source?.generation, intent = this.openIntent;
      this.working = true;
      cx.spawn(async task => {
        try {
          await this.host.initialize(task); // Fresh read-only repository/connection verification.
          if (this.openIntent !== intent) return;
          this.working = false;
          if (this.openIntent !== intent || panelEpoch() !== intent.marker || this.host.selected !== project ||
              this.host.source?.generation !== sourceGeneration || !this.host.panelCurrent()) return;
          if (this.compatibility.workReady()) this.openConversation(kind, work, task);
          else this.error = this.host.error || "Conversation navigation needs a verified project and native launch authority. No client was opened.";
        } catch (error) { if (this.openIntent === intent) { this.working = false; this.error = error.message || "Project verification failed; no client was opened."; } }
        task.notify();
      }, { owner: this.host.root || this.host });
      cx.notify(); return;
    }
    if (this.host.nativeHC.approvedDigest("navigation")) this.navigateApproved(kind, work, cx);
    else {
      const intent = this.openIntent, context = this.host.nativeHC.context();
      this.working = true;
      cx.spawn(async task => {
        let handedOff = false;
        try {
          const review = await this.host.nativeHC.load(context);
          if (!this.intentCurrent(intent) ||
              context !== this.host.nativeHC.context() || this.targetSession(kind, work) !== intent.session) return;
          if (!this.host.source?.runtime?.core_id || review.value.binding.core_id !== this.host.source.runtime.core_id)
            throw Error("The verified terminal service does not match this project's authenticated core.");
          // The explicit Open action consents to navigation on this exact
          // current identity only. It never answers OpenCode permissions.
          review.navigationOnly = true;
          this.host.nativeHC.approved = review;
          this.working = false; handedOff = true; this.navigateApproved(kind, work, task);
        } catch (error) { if (this.openIntent === intent) this.error = error.message || "The exact terminal service could not be verified. Nothing was opened."; }
        finally { if (this.openIntent === intent && !handedOff) { this.working = false; task.notify(); } }
      });
    }
    cx.notify();
  }
  targetSession(kind, work) {
    if (kind === "integrator") return this.selectedMain?.session_id;
    return this.work().rows.find(v => v.request_id === work && ["implementation", "review"].includes(v.kind))?.job?.session_id;
  }
  navigateApproved(kind, work, cx) {
    const scope = this.scope, generation = this.generation, intent = this.openIntent;
    if (this.working || !intent || !this.current(scope, generation)) return;
    this.working = true;
    cx.spawn(async task => {
      let handedOff = false;
      try {
        const authorization = await this.check(scope, generation);
        const proof = await this.observeViewers(intent, authorization);
        if (!this.intentCurrent(intent)) return;
        this.navigationProof = proof;
        this.working = false; handedOff = true;
        this.navigateObserved(kind, work, task, proof);
      } catch (error) { if (this.current(scope, generation)) this.error = `${error.message || "Saved viewer history could not be verified."} No new viewer was created.`; }
      finally { if (!handedOff && this.current(scope, generation)) { this.working = false; task.notify(); } }
    });
    cx.notify();
  }
  async observeViewers(intent, authorization) {
    const { scope, generation, kind, work, session } = intent;
    const hc = this.host.nativeHC.approved, context = this.host.nativeHC.context();
    const current = () => this.intentCurrent(intent) && this.authorized(authorization, scope, generation) &&
      hc === this.host.nativeHC.approved && context === this.host.nativeHC.context();
    if (!session || !current()) throw Error("Exact conversation context is unavailable.");
    const matches = v => v.pointer.target_kind === kind && v.pointer.session_id === session &&
      (v.pointer.work_request_id || "") === work && v.pointer.space === scope.space;
    // Preserve previously observed pointers even when a non-atomic list omits
    // one. Missing observations never prove a viewer closed.
    const retained = [...this.retainedViewers.values(), ...this.clients, ...(this.client?.pointer ? [this.client] : [])].filter(matches);
    const response = await this.command(["client-list", "--", scope.space, scope.queue, scope.project],
      { on: scope.machine, current });
    if (!current()) throw Error("Conversation context changed.");
    if (response.code !== 0 || Buffer.byteLength(response.stdout || "") > 1048576) throw Error("Saved viewer list is unavailable.");
    const values = JSON.parse(response.stdout);
    if (!Array.isArray(values) || values.length > 128) throw Error("Saved viewer list is unsupported.");
    const listed = values.map(v => clientStatus(v, scope));
    if (new Set(listed.map(v => v.pointer_sha256)).size !== listed.length) throw Error("Saved viewer identities conflict.");
    const candidates = new Map();
    for (const v of retained) {
      const old = candidates.get(v.pointer_sha256);
      if (old && (identity(old.pointer) !== identity(v.pointer) ||
          old.terminal && v.terminal && identity(old.terminal) !== identity(v.terminal)))
        throw Error("Retained viewer identities conflict.");
      candidates.set(v.pointer_sha256, old?.terminal ? old : v);
    }
    for (const v of listed.filter(matches)) {
      const old = candidates.get(v.pointer_sha256);
      if (old && identity(old.pointer) !== identity(v.pointer)) throw Error("A retained viewer identity changed.");
      candidates.set(v.pointer_sha256, old?.terminal ? old : v);
    }
    const local = [this.navigations[this.navigationKey(scope, `attention-${kind}`, work)],
      this.navigations[this.navigationKey(scope, kind, work)]].filter(Boolean);
    const saved = local.flatMap(n => [n, ...(n.history || [])]);
    for (const n of saved) if (n.pointer && !candidates.has(n.pointer))
      candidates.set(n.pointer, null);
    if (candidates.size > 128) throw Error("Saved viewer history exceeds the supported bound.");
    const rows = [];
    let targetIdentity;
    const conflict = message => Object.assign(Error(message), { identityConflict: true });
    for (const [digest, old] of [...candidates].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) {
      try {
        const status = clientStatus(await this.call("client-status", scope, digest, false, [], current), scope, digest);
        if (!current()) throw Error("Conversation context changed.");
        if (!matches(status) || old && (identity(old.pointer) !== identity(status.pointer) ||
            old.terminal && identity(old.terminal) !== identity(status.terminal)))
          throw conflict("Conflicting retained viewer identity.");
        const p = status.pointer;
        const identityPin = JSON.stringify([p.cwd, p.binding_revision, p.binding_sha256, p.service_identity,
          p.server_origin, p.opencode_project_id, p.ownership_sha256, p.profile_sha256]);
        if (targetIdentity && targetIdentity !== identityPin) throw conflict("Conflicting conversation ownership/profile/service identities.");
        targetIdentity = identityPin;
        const view = await this.call("client-verify", scope, digest, true, [], current, hc.value.binding_sha256);
        if (!current()) throw Error("Conversation context changed.");
        let client;
        try { client = this.checkedClientView(view, scope, digest, status); }
        catch (_) { throw conflict("Returned viewer identity does not match its retained record."); }
        const state = client.state === "blocked" ? "unobservable" :
          client.terminal && view.terminal_state === "live" ? "live" :
          client.terminal && view.terminal_state === "finished" && client.state === "terminal_recorded" ? "finished" :
          !client.terminal && view.terminal_state === "unobserved" && client.state === "ready_to_open" &&
            !this.attempted("client-open", scope, digest) ? "not_invoked" : "unobservable";
        rows.push({ client, view, state });
      } catch (error) {
        if (!current() || error.identityConflict) throw error;
        rows.push({ client: old, state: "unobservable", digest });
      }
    }
    if (!current()) throw Error("Conversation context changed.");
    const unresolvedLocal = saved.some(n => !n.pointer && !rows.some(r => r.client?.pointer.navigation_id === n.id));
    this.clients = [...new Map([...retained, ...listed].map(v => [v.pointer_sha256, v])).values()];
    for (const item of this.clients) this.rememberClient(item);
    return { intent, authorization, hc, context, rows, unresolvedLocal, local: local.map(n => n.pointer) };
  }
  navigateObserved(kind, work, cx, proof) {
    if (!this.intentCurrent(proof?.intent)) return;
    const live = proof.rows.filter(r => r.state === "live");
    const choose = rows => proof.local.map(d => rows.find(r => r.client.pointer_sha256 === d)).find(Boolean) || rows[0];
    if (proof.intent.readOnly) {
      this.error = live.length ? `${live.length} exact live viewer${live.length === 1 ? "" : "s"} observed${proof.unresolvedLocal || proof.rows.some(r => r.state === "unobservable") ? "; other saved openings remain unobservable" : ""}. Status checked only; no launch, attachment or selection was requested.` :
        proof.unresolvedLocal || proof.rows.some(r => r.state === "unobservable") ? "Opening status remains unobservable; prior IDs are retained. No preparation, launch or keep was repeated." :
        "Prior viewers are closed or not invoked. Status checked only; use an explicit Open conversation action to navigate.";
      cx.notify(); return;
    }
    if (!this.proofCurrent(proof)) return;
    if (live.length) {
      const selected = choose(live);
      this.acceptClientView(selected.view, proof.intent.scope, selected.client.pointer_sha256);
      this.showTerminal(proof.intent.scope, this.client, cx);
    } else if (proof.unresolvedLocal || proof.rows.some(r => r.state === "unobservable")) {
      this.error = "A matching viewer or preparation is pending or unobservable. Check opening status; no replacement will be prepared or launched.";
    } else {
      const ready = choose(proof.rows.filter(r => r.state === "not_invoked"));
      if (ready) {
        this.selectClient(ready.client);
        this.freeze("client", ready.client, proof.intent.scope, proof.intent.generation, proof.authorization);
        this.review.navigateOnOpen = true;
        this.continueOpen(cx);
      } else {
        const row = this.work().rows.find(v => v.request_id === work);
        this.prepareNavigation(kind, work, cx, kind === "integrator" || !row?.job?.terminal_proven, proof);
      }
    }
    cx.notify();
  }
  openRetained(item, cx) {
    this.navigationCandidates = null;
    const target = this.navigationTarget;
    if (target && (item.pointer.target_kind !== target.kind || item.pointer.session_id !== this.targetSession(target.kind, target.work) ||
        (item.pointer.work_request_id || "") !== target.work)) {
      this.error = "The saved client belongs to a different conversation. Nothing was opened."; cx.notify(); return;
    }
    if (item.terminal) this.inspectClient(item, cx, !this.openIntent?.readOnly);
    else {
      this.reviewClient(item, cx);
      if (["armed", "attempted_unknown"].includes(item.state))
        this.error = "A prior inspection launch is uncertain. Check its exact receipt; another client will not be spawned.";
    }
  }
  prepareNavigation(kind, work, cx, attention = false, proof = this.navigationProof) {
    const scope = this.work().bound;
    const generation = this.generation;
    if (!this.current(scope) || this.working || this.storageError || !this.compatibility.nativeReady("navigation") ||
        !this.proofCurrent(proof) || proof.intent.kind !== kind || proof.intent.work !== work ||
        proof.unresolvedLocal || proof.rows.some(r => r.state !== "finished")) return;
    const key = this.navigationKey(scope, attention ? `attention-${kind}` : kind, work);
    const former = this.navigations[key];
    const history = [...(former?.history || []), ...proof.rows.map(r => ({
      id: r.client.pointer.navigation_id, pointer: r.client.pointer_sha256, state: "viewer_finished",
    }))];
    const navigation = { id: `open-${randomBytes(16).toString("hex")}`, phase: "uncertain",
      history: [...new Map(history.map(v => [v.pointer, v])).values()] };
    this.navigations[key] = navigation;
    this.review = null; this.navigationRecovery = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        const authorization = proof.authorization;
        if (!this.proofCurrent(proof)) return;
        const value = await this.call(attention ? "client-attention-prepare-v1" : "client-prepare", scope, navigation.id, true, [kind, work], () => this.proofCurrent(proof));
        if (!this.proofCurrent(proof)) return;
        pointer(value, scope);
        if (value.navigation_id !== navigation.id || value.target_kind !== kind || (value.work_request_id || "") !== work || value.space !== scope.space ||
            this.openIntent && value.session_id !== this.openIntent.session) throw Error("Different client target");
        navigation.phase = "prepared"; navigation.pointer = value.pointer_sha256;
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        if (!this.proofCurrent(proof)) return;
        this.selectClient({ protocol: "ocq-client-v1", project_id: scope.project, pointer_sha256: value.pointer_sha256,
          state: "ready_to_open", pointer: value, attempt_permitted: false, current_route: "unverified" });
        this.freeze("client", this.client, scope, generation, authorization);
        this.review.navigateOnOpen = true; this.work().dirty = true;
      } catch (error) { if (this.current(scope, generation)) this.error = `${error.message || "Navigation preparation failed"} Saved preparation ID retained; no client launch was requested.`; }
      finally { if (this.current(scope, generation)) { this.working = false; this.continueOpen(cx); cx.notify(); } }
    }); cx.notify();
  }
  reviewClient(item, cx) {
    const scope = this.scope, generation = this.generation;
    if (!this.current(scope) || this.working) return;
    this.review = null; this.selectClient(item); this.error = null; this.working = true;
    cx.spawn(async cx => {
      try {
        const value = clientStatus(await this.call("client-status", scope, item.pointer_sha256), scope, item.pointer_sha256);
        if (!this.current(scope, generation)) return;
        this.selectClient(value);
        if (this.compatibility.nativeReady("navigation") && value.pointer.space === scope.space) {
          const authorization = await this.check(scope, generation);
          if (!this.authorized(authorization, scope, generation)) return;
          this.freeze("client", value, scope, generation, authorization);
          this.review.navigateOnOpen = !!this.navigationTarget;
        } else this.review = { kind: "client", value, scope, generation, readOnly: true,
          pages: this.work().pagesFor(value), page: 0 };
      } catch (_) { if (this.current(scope, generation)) this.error = "Exact navigation pointer unavailable; no client launched."; }
      finally { if (this.current(scope, generation)) { this.working = false; this.continueOpen(cx); cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  confirm(cx) {
    const review = this.review, scope = review?.scope;
    if (!this.pinned(review) || this.working || this.storageError || !review.confirming) return;
    const main = review.kind === "main", action = main ? "main-start" : "client-open";
    if (!main && review.value.state !== "ready_to_open") return;
    if (!main && !this.proofCurrent()) {
      if (!this.openIntent?.readOnly) this.openConversation(review.value.pointer.target_kind, review.value.pointer.work_request_id || "", cx);
      return;
    }
    const target = main ? review.value.status.operation_id : review.value.pointer_sha256;
    if (this.attempted(action, scope, target)) return;
    const key = this.markerKey(action, scope, target);
    const completionCurrent = () => {
      const binding = this.host.board?.project?.current_binding;
      return this.authorized(review, scope, review.generation) && (main || this.intentCurrent() &&
        review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest("navigation") &&
        binding?.repo_root === scope.folder &&
        String(binding.revision) === String(review.value.pointer.binding_revision));
    };
    const attempt = { state: "not_invoked", digest: main ? review.value.preview_sha256 : target };
    this.attempts[key] = attempt;
    this.working = true; this.error = null; review.confirming = false;
    cx.spawn(async cx => {
      let invoked = false;
      try {
        await this.saveAttempts();
        if (!this.pinned(review)) throw Error("Context changed");
        if (!main) {
          // A prepared pointer is not a lifetime-wide launch authorization.
          // Read/verify the entire retained set again immediately before arm.
          const proof = await this.observeViewers(this.openIntent, review);
          if (!this.pinned(review) || !this.intentCurrent(proof.intent) || proof.unresolvedLocal ||
              proof.rows.some(r => r.state === "live" || r.state === "unobservable") ||
              !proof.rows.some(r => r.client?.pointer_sha256 === target && r.state === "not_invoked"))
            throw Error("Another viewer is live, pending or unobservable");
          this.navigationProof = proof;
        }
        attempt.state = "uncertain"; await this.saveAttempts();
        if (!this.pinned(review) || !main && !this.proofCurrent()) throw Error("Context changed");
        invoked = true;
        const value = await this.call(action, scope, target, true, main ? [review.value.preview_sha256] : [],
          () => this.pinned(review), main ? undefined : review.hc.value.binding_sha256);
        // A successful open changes the observed client state and may clear its
        // review. Keep the original dispatch fence, but validate completion by
        // the same scope/pins/HC identity rather than that expected old state.
        if (!completionCurrent()) return;
        if (main) this.live = mainStatus(value, scope.project, target);
        else {
          this.acceptClientView(value, scope, target);
          if (review.navigateOnOpen) {
            this.verification = null;
            const observed = await this.call("client-verify", scope, target, true, [],
              completionCurrent,
              review.hc.value.binding_sha256);
            if (completionCurrent()) {
              this.acceptClientView(observed, scope, target);
              if (this.verification.state === "live") this.showTerminal(scope, this.client, cx);
              else this.error = "The saved inspection client is not live. Its receipt is retained; no replacement was launched.";
            }
          }
        }
        this.work().dirty = true;
      } catch (_) {
        if (!invoked && this.attempts[key] === attempt) {
          attempt.state = "not_invoked";
          try { await this.saveAttempts(); } catch (_) { attempt.state = "uncertain"; this.storageError = true; }
        }
        if (this.current(scope, review.generation)) this.error = invoked
          ? "Operation outcome unknown/refused. Read the exact record; no repeat Start or client spawn is offered."
          : "Transport was not invoked. Review again in a fresh validated context; unavailable durable evidence keeps actions blocked.";
      } finally {
        if (this.current(scope, review.generation)) { this.review = null; this.working = false; cx.notify(); }
      }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  continueOpen(cx) {
    const intent = this.openIntent, review = this.review;
    if (!intent || !this.proofCurrent() || this.working ||
        this.targetSession(intent.kind, intent.work) !== intent.session || review?.kind !== "client" ||
        review.value.state !== "ready_to_open" || !this.pinned(review)) return;
    review.confirming = true;
    this.confirm(cx);
  }
  checkedClientView(value, scope, digest, previous = null) {
    if (value.protocol !== "ocq-client-v1" || value.current_route !== "unverified" ||
      !["live", "finished", "unobserved"].includes(value.terminal_state)) throw Error("Invalid native terminal observation");
    const client = clientStatus(value.status, scope, digest);
    if (previous?.pointer_sha256 === digest && (identity(previous.pointer) !== identity(client.pointer) ||
        previous.terminal && identity(previous.terminal) !== identity(client.terminal)))
      throw Error("Retained client identity changed; inspect afresh");
    if (client.terminal && client.terminal.labels?.["ocq.hc_binding"] !== this.host.nativeHC.approvedDigest("navigation"))
      throw Error("Retained terminal HC binding mismatch");
    return client;
  }
  acceptClientView(value, scope, digest) {
    this.verification = null;
    const client = this.checkedClientView(value, scope, digest, this.client);
    this.rememberClient(client);
    this.client = client;
    this.verification = { scope, generation: this.generation, hc: this.host.nativeHC.approved,
      context: this.host.nativeHC.context(), pointer: identity(client.pointer), receipt: identity(client.terminal),
      digest: client.pointer_sha256, state: value.terminal_state };
  }
  verifiedClient(value = this.client) {
    const v = this.verification;
    return !!v && !!value?.terminal && this.current(v.scope, v.generation) && this.compatibility.nativeReady("navigation") &&
      v.hc === this.host.nativeHC.approved && v.context === this.host.nativeHC.context() &&
      v.digest === value.pointer_sha256 && v.pointer === identity(value.pointer) && v.receipt === identity(value.terminal);
  }
  selectClient(value) {
    if (value?.pointer) this.rememberClient(value);
    if (!this.verifiedClient(value)) this.verification = null;
    this.client = value;
  }
  rememberClient(value) {
    const old = this.retainedViewers.get(value.pointer_sha256);
    if (old && (identity(old.pointer) !== identity(value.pointer) ||
        old.terminal && value.terminal && identity(old.terminal) !== identity(value.terminal)))
      throw Error("Conflicting retained viewer identity.");
    this.retainedViewers.set(value.pointer_sha256, old?.terminal && !value.terminal ? old : value);
  }
  showTerminal(scope, value, cx) {
    const receipt = value.terminal;
    if (!receipt || value !== this.client || !this.current(scope) || !this.verifiedClient(value) ||
        this.verification.state !== "live" || receipt.peer !== (scope.machine ?? "local") ||
        !this.intentCurrent() || this.openIntent.readOnly) throw Error("Native navigation intent or receipt changed");
    if (this.tabNavigation) return;
    const address = scope.machine === null ? receipt.returned_terminal_id : `${scope.machine}/${receipt.returned_terminal_id}`;
    const spaceID = scope.machine === null ? scope.space : `${scope.machine}/${scope.space}`;
    const space = spaces().find(s => s.id === spaceID && s.machine === scope.machine);
    if (!space) throw Error("The receipt's existing project space is unavailable; no replacement space was created.");
    const ticket = { address, spaceID, scope, intent: this.openIntent,
      sourceGeneration: this.host.source?.generation, context: JSON.stringify(this.host.setupContext),
      marker: panelEpoch(), origin: this.navigationOrigin(), digest: value.pointer_sha256,
      cwd: receipt.cwd, argv: JSON.stringify(receipt.argv), hc: receipt.labels["ocq.hc_binding"],
      key: this.markerKey("terminal.keep", scope, value.pointer_sha256), phase: "observing" };
    this.tabNavigation = ticket; this.attachmentNotice = null; this.error = null;
    const expiry = cx.timer.after(6000, task => {
      if (this.tabNavigation !== ticket) return;
      this.tabNavigation = null; this.attachmentNotice = ticket;
      this.error = "Project-tab attachment is not yet observed. Check opening status; prior attempts remain uncertain and will not be repeated.";
      task.notify();
    });
    // Event/task only, implicitly owned. Native observation is bounded; expiry
    // ends the spinner, never proves failure or permits another keep.
    cx.spawn(async task => {
      try {
        for (let read = 0; read < 40; read++) {
          if (!this.tabTicketCurrent(ticket)) {
            ticket.message = "Navigation context changed. Further selection and attachment were cancelled; prior attempts remain retained.";
            return;
          }
          const terminal = terminals().find(t => t.address === ticket.address);
          if (terminal && (terminal.state !== "live" || terminal.cwd !== ticket.cwd ||
              JSON.stringify(terminal.argv) !== ticket.argv || terminal.labels?.["ocq.handoff"] !== ticket.digest ||
              terminal.labels?.["ocq.hc_binding"] !== ticket.hc || terminal.labels?.["herdr.space"] !== scope.space))
            throw Error("The native viewer identity or state changed. No attachment was repeated.");
          const nativePanes = panes().filter(p => p.terminal === ticket.address);
          if (nativePanes.length > 1) throw Error("The viewer has conflicting pane identities; nothing was selected.");
          const pane = nativePanes[0], currentSpace = spaces().find(s => s.id === ticket.spaceID && s.machine === scope.machine);
          if (!currentSpace) throw Error("The exact existing project space is unavailable.");
          if (terminal && pane) {
            if (!currentSpace.tabs.some(t => t.id === pane.tab)) throw Error("The viewer is attached outside its exact project space; nothing was moved.");
            if (this.attempted("terminal.keep", scope, ticket.digest)) {
              this.attempts[ticket.key] = { ...this.attempts[ticket.key], state: "attached",
                digest: ticket.digest, terminal: ticket.address, tab: pane.tab };
              await this.saveAttempts();
            }
            if (!this.tabTicketCurrent(ticket)) return;
            if (!panes().some(p => p.terminal === ticket.address && p.tab === pane.tab)) throw Error("The observed pane changed before selection.");
            ticket.phase = "attached";
            run("space.select", { id: currentSpace.id });
            run("tab.select", { id: pane.tab });
            run("pane.focus", { terminal: ticket.address });
            return;
          }
          if (terminal && ticket.phase === "observing") {
            ticket.phase = "waiting";
            if (!this.attempted("terminal.keep", scope, ticket.digest)) {
              if (!this.intentCurrent(ticket.intent) || !this.verifiedClient(value)) return;
              let invoked = false;
              const attempt = { state: "not_invoked", digest: ticket.digest };
              try {
                this.attempts[ticket.key] = attempt;
                await this.saveAttempts();
                if (!this.tabTicketCurrent(ticket) || !this.intentCurrent(ticket.intent) || !this.verifiedClient(value)) return;
                attempt.state = "uncertain"; await this.saveAttempts();
                if (!this.tabTicketCurrent(ticket) || !this.intentCurrent(ticket.intent) || !this.verifiedClient(value)) return;
                invoked = true;
                run("terminal.keep", { terminal: ticket.address, place: "tab" });
              } finally {
                if (!invoked && this.attempts[ticket.key] === attempt) {
                  attempt.state = "not_invoked";
                  try { await this.saveAttempts(); }
                  catch (_) { attempt.state = "uncertain"; this.storageError = true; }
                }
              }
            }
          }
          await task.sleep(125);
        }
        ticket.message = "Project-tab attachment is not yet observed. Check opening status; no viewer launch or keep will be repeated.";
      } catch (error) { ticket.message = error.message || "Attachment is unconfirmed; no keep was repeated."; }
      finally {
        expiry.cancel();
        if (this.tabNavigation === ticket) {
          this.tabNavigation = null;
          this.attachmentNotice = ticket.phase === "attached" ? null : ticket;
          if (ticket.message) this.error = ticket.message;
          task.notify();
        }
      }
    });
    cx.notify();
  }
  tabTicketCurrent(ticket) {
    const h = this.host;
    return this.tabNavigation === ticket && !h.closed && (!h.root || h.root.detail === h) &&
      (!h.source || h.source.current() && h.source.generation === ticket.sourceGeneration) &&
      h.selected === ticket.scope.project && JSON.stringify(h.setupContext) === ticket.context &&
      h.nativeHC.approvedDigest("navigation") === ticket.hc &&
      panelEpoch() === ticket.marker && this.navigationOrigin() === ticket.origin;
  }
  checkOpeningStatus(cx) {
    if (this.working) return;
    const previous = this.openIntent || this.attachmentNotice?.intent;
    const scope = this.work().bound;
    if (!previous || !this.current(scope) || this.targetSession(previous.kind, previous.work) !== previous.session ||
        !this.host.nativeHC.approvedDigest("navigation")) {
      this.error = "The saved opening context is no longer verified. No action was repeated."; cx.notify(); return;
    }
    this.tabNavigation = null; // Cancel any pending selection; keep evidence stays.
    this.openIntent = { ...previous, scope, generation: this.generation, readOnly: true,
      marker: panelEpoch(), origin: this.navigationOrigin() };
    this.navigationProof = null;
    this.navigateApproved(previous.kind, previous.work, cx);
  }
  inspectClient(item, cx, show = false) {
    if (show) { this.openConversation(item.pointer.target_kind, item.pointer.work_request_id || "", cx); return; }
    const scope = this.scope, generation = this.generation;
    if (!this.current(scope) || this.working || item.pointer.space !== scope.space) return;
    this.selectClient(item); this.verification = null;
    this.review = null; this.working = true; this.error = null;
    const pointerIdentity = identity(item.pointer), receiptIdentity = identity(item.terminal);
    const current = () => this.current(scope, generation) && this.client?.pointer_sha256 === item.pointer_sha256 &&
      identity(this.client.pointer) === pointerIdentity && identity(this.client.terminal) === receiptIdentity;
    cx.spawn(async cx => {
      try {
        const authorization = await this.check(scope, generation);
        if (!current() || !this.authorized(authorization, scope, generation)) return;
        const hc = this.host.nativeHC.approved;
        const value = await this.call("client-verify", scope, item.pointer_sha256, true, [],
          () => current() && this.authorized(authorization, scope, generation) && hc === this.host.nativeHC.approved,
          this.host.nativeHC.approvedDigest("navigation"));
        if (current() && this.authorized(authorization, scope, generation) && hc === this.host.nativeHC.approved) {
          this.acceptClientView(value, scope, item.pointer_sha256);
        }
      } catch (error) { if (this.current(scope, generation)) this.error = `${error.message || "Exact terminal verification failed"} No client was launched or replaced.`; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  goalSummary(goal) {
    const observed = this.live?.operation_id === goal.main.operation_id ? this.live : goal.main;
    if (observed.state === "prepared") return "Awaiting your confirmation · goal not sent";
    if (observed.state === "prompt_pending") return "Goal received · waiting for Main to consume it";
    const workers = goal.work.filter(v => ["implementation", "review"].includes(v.kind));
    if (workers.some(v => v.job?.state === "unknown"))
      return "Worker verification blocked · capacity held; native done is not OCQ completion proof";
    const review = goal.work.find(v => v.kind === "review_result")?.result?.verdict;
    if (review) return `Review ${review.replaceAll("_", " ")} · recorded`;
    if (workers.some(v => v.job)) return `${workers.filter(v => v.job?.terminal_proven).length} of ${workers.length} workers finished`;
    if (workers.length) return "Work prepared · not yet assigned to a worker";
    if (["blocked", "unknown", "missing_or_unknown", "mismatch", "role_unverified", "prompt_attempted_unknown"].includes(observed.state))
      return "Goal needs attention · check its exact saved status; do not resend";
    return observed.state === "busy" ? "Main last checked working · no owned workers assigned yet" :
      "No owned workers assigned · open Main to check progress";
  }
  render(cx, theme, button) {
    this.sync(this.work().bound);
    const box = v_flex().id("ocq-main-client").w_full().min_w(0).gap(8);
    box.child(h_flex().min_w(0).items_center().gap(6)
      .child(div().flex_1().min_w(0).font_semibold().text_size(16).child("Main"))
      .child(this.host.panelControl("ocq-main-details", this.showDetails ? "Hide details" : "Details",
        cx => { this.showDetails = !this.showDetails; cx.notify(); }, theme, this.host.renderTargets)));
    if (this.storageError) box.child(div().text_color(theme.blocked).child("Private attempt evidence unavailable. Starts and client launches disabled."));
    if (this.error) box.child(div().text_size(12).text_color(theme.blocked).child(this.error));
    const selected = this.selectedMain;
    if (selected) box.child(this.host.panelControl("ocq-main-open", "Open Main", task => {
      this.work().scroll.scroll_to_item(0); this.openConversation("integrator", "", task);
    }, theme, this.host.renderTargets).font_semibold().bg(theme.hover));
    if (selected && !this.compatibility.workReady()) box.child(div().text_size(12).text_color(theme.blocked)
      .child(this.compatibility.reason || "This endpoint is read-only or its project context is unverified. Conversation opening is unavailable; nothing will be launched."));
    const workflow = this.host.workflow, scope = workflow.scope(), saved = scope && workflow.saved?.[workflow.key(scope)];
    const currentGoal = this.goals.find(g => saved?.mode === "send" && g.main.operation_id === saved.operation) || this.goals.at(-1);
    // Keep frozen operation recovery accessible, without presenting a second
    // setup/send wizard over work the user has already started.
    if (!currentGoal || currentGoal.main.state === "prepared" || this.showDetails || workflow.review)
      box.child(workflow.render(cx, theme, button, selected));
    for (const [index, goal] of (this.goals || []).entries()) {
      if (goal !== currentGoal && !this.showDetails) continue;
      box.child(v_flex().min_w(0).gap(3).pt(8)
        .child(div().font_semibold().child(goal === currentGoal ? "Current goal" : `Earlier goal ${index + 1}`))
        .child(div().text_size(12).text_color(theme.muted).child(this.goalSummary(goal)))
        .when(this.showDetails, e => e.child(div().text_size(12).text_color(theme.muted).child(
          `Last checked: ${this.live?.operation_id === goal.main.operation_id ? this.live.read_at : goal.main.read_at}`)))
        .when(this.goalCheck?.operation === goal.main.operation_id, e =>
          e.child(div().text_size(12).text_color(["error", "canceled", "attention"].includes(this.goalCheck.state) ? theme.blocked : theme.muted)
            .child(`${this.goalCheck.message}${this.goalCheck.readAt ? `\nLast checked: ${this.goalCheck.readAt}` : ""}`)))
        .when(goal === currentGoal && goal.main.state !== "prepared", e => {
          if (this.working) e.child(div().text_size(12).text_color(theme.muted).child(
            this.goalCheck?.state === "checking" ? "Check in progress · read only" : "Goal check unavailable while another Main action is in progress."));
          else e.child(button(`ocq-main-goal-check-${goal.main.operation_id}`, "Check saved goal status · no resend", cx => this.recoverMain(goal.main, cx)));
          return e;
        }));
    }
    if (!selected) box.child(div().text_color(theme.muted).child("Main will stay linked to this project."));
    else {
      if (this.showDetails) box.child(div().text_size(12).child(`Session: ${selected.session_id}\nCheckout: ${selected.repo}\nBranch: ${selected.main_branch}\nAnchor: ${selected.commit_anchor}\nStored state is not live completion proof.`));
      if (!this.working && this.showDetails) box.child(button("ocq-main-observe", "Check exact Main record", cx => this.recoverMain(selected, cx)));
    }
    if (!this.working && this.showDetails) for (const item of this.mains) box.child(button(`ocq-main-operation-${item.operation_id}`, `${item.mode} · ${item.operation_id} · ${item.state}`, cx => { this.suspend(); this.operation = item; cx.notify(); }));
    if (this.operation && this.showDetails) {
      const item = this.operation;
      box.child(div().child(`Main operation ${item.operation_id} · ${item.state}\n${item.reason}\nSetup: ${item.setup_id}\nSession: ${item.session_id}\nPrompt ID: ${item.prompt_id || "none"}\nBinding revision: ${item.binding_revision}\nIntent: ${item.intent_sha256}`));
      if (!this.working) {
        box.child(button("ocq-main-recover", "Recover this exact operation · GET/receipt only", cx => this.recoverMain(item, cx)));
        if (!this.storageError && !this.attempted("main-start", this.scope, item.operation_id) && ["prepared", "binding_committed", "session_observed"].includes(item.state))
          box.child(button("ocq-main-preview", "Retain and review exact Main operation", cx => this.reviewMain(item, cx)));
      }
      if (this.attempted("main-start", this.scope, item.operation_id)) box.child(div().text_color(theme.blocked).child("A Start was potentially invoked. Exact recovery only; no automatic continuation or prompt resend."));
    }
    if (this.live && this.showDetails) box.child(div().child(`Explicit Main observation: ${this.live.state} · ${this.live.read_at}\n${this.live.reason}\nDirty checkout: ${this.live.dirty ? "yes (preserved)" : "no"} · HEAD advanced: ${this.live.head_advanced ? "yes; explicit refresh required" : "no"}`));
    if (this.showDetails) {
      box.child(this.host.panelControl("ocq-client-list", this.showClients ? "Hide retained clients" : "Retained clients",
        cx => { this.showClients = !this.showClients; cx.notify(); }, theme, this.host.renderTargets));
      if (!this.working && this.showClients) for (const item of this.clients) {
        box.child(button(`ocq-client-record-${item.pointer_sha256}`, `${item.pointer.target_kind} · ${item.state}`, cx => this.reviewClient(item, cx)));
        if (item.terminal && item.pointer.space === this.scope?.space)
          box.child(button(`ocq-client-verify-${item.pointer_sha256}`, "Verify and open retained client", cx => this.inspectClient(item, cx, true)));
      }
    }
    if (this.tabNavigation || this.attachmentNotice || this.navigationTarget?.kind === "integrator" || this.review?.kind === "main" || this.showClients && this.navigationTarget?.kind !== "worker") {
      this.revealNavigation = false;
      box.child(this.renderNavigation(cx, theme, button));
    }
    return box;
  }
  renderNavigation(cx, theme, button) {
    const box = v_flex().w_full().min_w(0).gap(8).p(10).border(1).border_color(theme.border).rounded(6);
    box.child(div().font_semibold().child((this.navigationTarget || this.attachmentNotice?.intent)?.kind === "worker" ? "Worker conversation" : "Main conversation"));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (this.navigationRecovery && !this.working) {
      const saved = this.navigationRecovery;
      box.child(div().text_size(12).child(`Opening status is uncertain for saved navigation ${saved.id}. Its ID and mode remain retained; no new preparation or launch will be attempted.`));
    }
    if (!this.working && (this.openIntent || this.attachmentNotice))
      box.child(button("ocq-client-record-opening-status", "Check opening status", task => this.checkOpeningStatus(task)));
    if (this.working) box.child(div().text_size(12).text_color(theme.muted).child("Verifying the exact saved conversation…"));
    if (this.tabNavigation) box.child(div().text_size(12).text_color(theme.muted).child("Observing the exact project tab… No client is being relaunched."));
    if (this.attachmentNotice) box.child(div().text_size(12).text_color(theme.muted).child(
      this.attachmentNotice.message || "Attachment is not yet observed. Prior evidence is retained; no keep will be repeated."));
    if (this.navigationTarget && !this.host.nativeHC.approvedDigest("navigation") && !this.openIntent)
      box.child(this.host.nativeHC.render(cx, theme, button));
    for (const item of this.navigationCandidates || []) box.child(button(`ocq-client-record-${item.pointer_sha256}`,
      item.terminal ? `Open retained client ${item.terminal.returned_terminal_id}` : "Inspect saved client preparation", task => this.openRetained(item, task)));
    if (this.client?.terminal && !this.working && !this.tabNavigation && !this.openIntent) {
      if (this.verifiedClient() && this.verification.state === "live")
        box.child(button("ocq-client-show-returned", "Open saved conversation", task => this.openConversation(this.client.pointer.target_kind, this.client.pointer.work_request_id || "", task)));
      else box.child(button(`ocq-client-verify-${this.client.pointer_sha256}`, "Verify and open saved conversation", task => this.inspectClient(this.client, task, true)));
    }
    const review = this.review;
    if (review) {
      if (review.kind === "main") {
        box.child(mainSummary(review.value, review.scope.machine))
          .child(button("ocq-main-review-roles", review.roles ? "Hide Advanced" : "Advanced · roles", cx => { review.roles = !review.roles; cx.notify(); }));
        if (review.roles) box.child(roleSummary(review.value));
      } else {
        const p = review.value.pointer;
        const worker = this.work().rows.find(v => v.request_id === p.work_request_id);
        box.child(div().child(`${review.scope.machine ?? "This computer"} · ${this.host.project?.label || "Project"}`))
          .child(div().text_size(12).text_color(theme.muted).child(review.value.state === "ready_to_open"
            ? `Open ${p.target_kind === "integrator" ? "Main" : worker?.kind === "review" ? "independent review" : "implementation"} history in a new inspection terminal. No prompt or permission response is sent.`
            : "Saved inspection receipt for this conversation. Verification never respawns a client."));
      }
      const action = review.kind === "main" ? "main-start" : "client-open";
      const target = review.kind === "main" ? review.value.status.operation_id : review.value.pointer_sha256;
      const allowed = review.kind === "main" || !this.openIntent?.readOnly && review.value.state === "ready_to_open";
      if (!this.pinned(review)) box.child(div().text_color(theme.blocked).child(review.readOnly
        ? "Stored record only. Review the private HC binding, then read a fresh pointer review before launch."
        : "Review authority suspended; inspect and review again."));
      if (allowed && this.pinned(review) && !this.working && !this.storageError && !this.attempted(action, this.scope, target)) {
        box.child(button("ocq-main-confirm-once", review.kind === "main" ? mainAction(review.value.status.mode) : "Open conversation", cx => {
          if (this.pinned(review)) { review.confirming = true; this.confirm(cx); }
        }));
      }
      box.child(technicalDetails(review, button, "ocq-main-review"));
      if (review.kind === "client" && this.attempted(action, this.scope, target)) box.child(div().text_color(theme.blocked).child("Client launch may have been invoked. Only exact stored receipt recovery or observation is offered, never a second spawn for this pointer."));
    }
    return box;
  }
}
