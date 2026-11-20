import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { run, terminals } from "herdr";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { decimal } from "./stored-updates.js";
import { mainAction, mainSummary, roleSummary, technicalDetails } from "./action-review.js";

const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9_-]{1,128}$/.test(value);
const object = value => value && typeof value === "object" && !Array.isArray(value);
const pins = value => JSON.stringify(["image_sha256", "bridge_context", "queue_identity", "observation_context", "observation_store_context", "observation_schema"].map(key => value[key]));
const identity = value => JSON.stringify(value && Object.fromEntries(Object.entries(value).filter(([key]) => key !== "read_at")));

function mainStatus(value, project, operation) {
  if (!object(value) || value.protocol !== "ocq-main-v1" || value.project_id !== project || !id(value.operation_id) ||
    operation && value.operation_id !== operation || !["create", "adopt", "refresh", "send", "reopen"].includes(value.mode) ||
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
    this.host = host; this.generation = 0; this.mains = []; this.clients = [];
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
  suspend() { this.generation++; this.review = null; this.verification = null; this.working = false; this.error = null; this.navigationTarget = null; this.navigationCandidates = null; this.waitingForTerminal = null; this.navigationRecovery = null; }
  sync(scope) {
    const key = JSON.stringify(scope);
    if (key === this.scopeKey) return;
    this.suspend(); this.scopeKey = key; this.scope = scope;
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
    if (kind === "client" && !this.host.nativeHC.approvedDigest()) throw Error("Review exact private HC first");
    this.review = { kind, value, scope, generation, pins: authorization.pins, hc,
      epoch: authorization.epoch, serial: authorization.serial,
      pages: this.work().pagesFor(hc ? { client: value, hc_binding: hc.value } : value), page: 0, confirming: false };
  }
  pinned(review) {
    const currentBinding = this.host.board?.project?.current_binding;
    const revision = review?.kind === "main" ? review.value.binding.revision : review?.value.pointer.binding_revision;
    return review && !review.readOnly && this.review === review && this.current(review.scope, review.generation) && this.compatibility.workReady() &&
      (review.kind !== "client" || review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest()) &&
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
    if (!this.current(scope) || this.working) return;
    this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const value = await this.call("main-recover", scope, item.operation_id);
        if (!this.current(scope, generation)) return;
        this.live = mainStatus(value, scope.project, item.operation_id);
        this.work().dirty = true;
      } catch (_) { if (this.current(scope, generation)) this.error = "Exact Main observation unavailable. No replacement, prompt resend, permission reply or continuation was attempted."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  navigationKey(scope, kind, work) { return JSON.stringify([scope.source ?? scope.machine, scope.space, scope.queue, scope.project, kind, work]); }
  openConversation(kind, work, cx) {
    const scope = this.work().bound;
    this.sync(scope);
    if (this.working) { cx.notify(); return; }
    this.navigationTarget = { kind, work };
    this.navigationCandidates = null; this.client = null; this.verification = null; this.review = null; this.waitingForTerminal = null; this.navigationRecovery = null;
    this.revealNavigation = true; this.error = null;
    const generation = this.generation;
    if (!this.host.panelCurrent()) { this.error = "The owning project context changed. Reopen the project before opening its conversation."; cx.notify(); return; }
    if (!this.current(scope) || !this.compatibility.workReady()) {
      if (!this.host.initialize) { this.error = "Verify this project connection before opening a conversation."; cx.notify(); return; }
      const project = this.host.selected, sourceGeneration = this.host.source?.generation;
      this.working = true;
      cx.spawn(async task => {
        try {
          await this.host.initialize(task); // Fresh read-only repository/connection verification.
          this.working = false;
          if (this.host.selected !== project || this.host.source?.generation !== sourceGeneration || !this.host.panelCurrent()) return;
          if (this.compatibility.workReady()) this.openConversation(kind, work, task);
          else this.error = this.host.error || "Conversation navigation needs a verified project and native launch authority. No client was opened.";
        } catch (error) { this.working = false; this.error = error.message || "Project verification failed; no client was opened."; }
        task.notify();
      }, { owner: this.host.root || this.host });
      cx.notify(); return;
    }
    if (this.host.nativeHC.approvedDigest()) this.navigateApproved(kind, work, cx);
    else {
      const requested = this.host.nativeHC.read(cx, task => {
        if (this.current(scope, generation)) this.navigateApproved(kind, work, task);
      });
      if (!requested) this.error = this.host.nativeHC.error || "Terminal service verification is already running. Complete that review, then open the conversation.";
    }
    cx.notify();
  }
  targetSession(kind, work) {
    if (kind === "integrator") return this.selectedMain?.session_id;
    return this.work().rows.find(v => v.request_id === work && ["implementation", "review"].includes(v.kind))?.job?.session_id;
  }
  navigateApproved(kind, work, cx) {
    const session = this.targetSession(kind, work);
    if (!session) { this.error = "This worker has no recorded owned session yet. Open Main for its current status."; cx.notify(); return; }
    const known = this.clients.filter(v => v.pointer.target_kind === kind && v.pointer.session_id === session &&
      (v.pointer.work_request_id || "") === work && v.pointer.space === this.scope.space);
    if (known.length > 1) { this.navigationCandidates = known; cx.notify(); return; }
    if (known.length === 1) { this.openRetained(known[0], cx); return; }
    const row = this.work().rows.find(v => v.request_id === work);
    this.prepareNavigation(kind, work, cx, kind === "integrator" || !row?.job?.terminal_proven);
  }
  openRetained(item, cx) {
    this.navigationCandidates = null;
    const target = this.navigationTarget;
    if (target && (item.pointer.target_kind !== target.kind || item.pointer.session_id !== this.targetSession(target.kind, target.work) ||
        (item.pointer.work_request_id || "") !== target.work)) {
      this.error = "The saved client belongs to a different conversation. Nothing was opened."; cx.notify(); return;
    }
    if (item.terminal) this.inspectClient(item, cx, true);
    else {
      this.reviewClient(item, cx);
      if (["armed", "attempted_unknown"].includes(item.state))
        this.error = "A prior inspection launch is uncertain. Check its exact receipt; another client will not be spawned.";
    }
  }
  prepareNavigation(kind, work, cx, attention = false, recoverID = null) {
    const scope = this.work().bound;
    this.sync(scope);
    const generation = this.generation;
    if (!this.current(scope) || this.working || this.storageError || !this.compatibility.nativeReady()) return;
    const key = this.navigationKey(scope, attention ? `attention-${kind}` : kind, work);
    const sameMode = this.navigations[key];
    const previous = sameMode ||
      this.navigations[this.navigationKey(scope, attention ? kind : `attention-${kind}`, work)];
    const found = previous && this.clients.find(item => item.pointer.navigation_id === previous.id);
    if (found) { this.openRetained(found, cx); return; }
    if (previous?.pointer) { this.reviewClient({ pointer_sha256: previous.pointer }, cx); return; }
    if (previous && (!sameMode || recoverID !== previous.id)) {
      this.navigationRecovery = { kind, work, attention: sameMode ? attention : !attention, id: previous.id };
      this.error = "An earlier inspection preparation has no returned pointer. Recover that saved preparation; no client launch is being retried.";
      cx.notify(); return;
    }
    if (recoverID && (!previous || previous.id !== recoverID)) {
      this.error = "Saved preparation changed. Reopen the conversation to inspect its exact record.";
      cx.notify(); return;
    }
    // Unknown preparation reuses its saved ID. A NEW ID requires another
    // explicit action after the original pointer is known, never an auto retry.
    const navigation = previous || { id: `open-${randomBytes(16).toString("hex")}`, phase: "uncertain" };
    this.navigations[key] = navigation;
    this.review = null; this.navigationRecovery = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        const authorization = await this.check(scope, generation);
        if (!this.authorized(authorization, scope, generation)) return;
        const value = await this.call(attention ? "client-attention-prepare-v1" : "client-prepare", scope, navigation.id, true, [kind, work], () => this.authorized(authorization, scope, generation));
        if (!this.authorized(authorization, scope, generation)) return;
        pointer(value, scope);
        if (value.navigation_id !== navigation.id || value.target_kind !== kind || (value.work_request_id || "") !== work || value.space !== scope.space) throw Error("Different client target");
        navigation.phase = "prepared"; navigation.pointer = value.pointer_sha256;
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        if (!this.authorized(authorization, scope, generation)) return;
        this.selectClient({ protocol: "ocq-client-v1", project_id: scope.project, pointer_sha256: value.pointer_sha256,
          state: "ready_to_open", pointer: value, attempt_permitted: false, current_route: "unverified" });
        this.freeze("client", this.client, scope, generation, authorization);
        this.review.navigateOnOpen = true; this.work().dirty = true;
      } catch (error) { if (this.current(scope, generation)) this.error = `${error.message || "Navigation preparation failed"} Saved preparation ID retained; no client launch was requested.`; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
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
        if (this.compatibility.nativeReady() && value.pointer.space === scope.space) {
          const authorization = await this.check(scope, generation);
          if (!this.authorized(authorization, scope, generation)) return;
          this.freeze("client", value, scope, generation, authorization);
          this.review.navigateOnOpen = !!this.navigationTarget;
        } else this.review = { kind: "client", value, scope, generation, readOnly: true,
          pages: this.work().pagesFor(value), page: 0 };
      } catch (_) { if (this.current(scope, generation)) this.error = "Exact navigation pointer unavailable; no client launched."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  confirm(cx) {
    const review = this.review, scope = review?.scope;
    if (!this.pinned(review) || this.working || this.storageError || !review.confirming) return;
    const main = review.kind === "main", action = main ? "main-start" : "client-open";
    if (!main && review.value.state !== "ready_to_open") return;
    const target = main ? review.value.status.operation_id : review.value.pointer_sha256;
    if (this.attempted(action, scope, target)) return;
    const key = this.markerKey(action, scope, target);
    const completionCurrent = () => {
      const binding = this.host.board?.project?.current_binding;
      return this.authorized(review, scope, review.generation) && (main ||
        review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest() &&
        binding?.repo_root === scope.folder &&
        String(binding.revision) === String(review.value.pointer.binding_revision));
    };
    this.attempts[key] = { state: "not_invoked", digest: main ? review.value.preview_sha256 : target };
    this.working = true; this.error = null; review.confirming = false;
    cx.spawn(async cx => {
      let invoked = false;
      try {
        await this.saveAttempts();
        if (!this.pinned(review)) throw Error("Context changed");
        this.attempts[key].state = "uncertain"; await this.saveAttempts();
        if (!this.pinned(review)) throw Error("Context changed");
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
              if (this.verification.state === "live") this.showTerminal(scope, this.client);
              else this.error = "The saved inspection client is not live. Its receipt is retained; no replacement was launched.";
            }
          }
        }
        this.work().dirty = true;
      } catch (_) {
        if (!invoked) {
          this.attempts[key].state = "not_invoked";
          try { await this.saveAttempts(); } catch (_) { this.attempts[key].state = "uncertain"; this.storageError = true; }
        }
        if (this.current(scope, review.generation)) this.error = invoked
          ? "Operation outcome unknown/refused. Read the exact record; no repeat Start or client spawn is offered."
          : "Transport was not invoked. Review again in a fresh validated context; unavailable durable evidence keeps actions blocked.";
      } finally {
        if (this.current(scope, review.generation)) { this.review = null; this.working = false; cx.notify(); }
      }
    }, { owner: this.host.root || this.host }); cx.notify();
  }
  acceptClientView(value, scope, digest) {
    this.verification = null;
    if (value.protocol !== "ocq-client-v1" || value.current_route !== "unverified" ||
      !["live", "finished", "unobserved"].includes(value.terminal_state)) throw Error("Invalid native terminal observation");
    const client = clientStatus(value.status, scope, digest);
    if (this.client?.pointer_sha256 === digest && (identity(this.client.pointer) !== identity(client.pointer) ||
        this.client.terminal && identity(this.client.terminal) !== identity(client.terminal)))
      throw Error("Retained client identity changed; inspect afresh");
    if (client.terminal && client.terminal.labels?.["ocq.hc_binding"] !== this.host.nativeHC.approvedDigest())
      throw Error("Retained terminal HC binding mismatch");
    this.client = client;
    this.verification = { scope, generation: this.generation, hc: this.host.nativeHC.approved,
      context: this.host.nativeHC.context(), pointer: identity(client.pointer), receipt: identity(client.terminal),
      digest: client.pointer_sha256, state: value.terminal_state };
  }
  verifiedClient(value = this.client) {
    const v = this.verification;
    return !!v && !!value?.terminal && this.current(v.scope, v.generation) && this.compatibility.nativeReady() &&
      v.hc === this.host.nativeHC.approved && v.context === this.host.nativeHC.context() &&
      v.digest === value.pointer_sha256 && v.pointer === identity(value.pointer) && v.receipt === identity(value.terminal);
  }
  selectClient(value) {
    if (!this.verifiedClient(value)) this.verification = null;
    this.client = value;
  }
  observedTerminal(scope, value) {
    const receipt = value?.terminal;
    if (!receipt) return null;
    const address = scope.machine === null ? receipt.returned_terminal_id : `${scope.machine}/${receipt.returned_terminal_id}`;
    const observed = terminals().find(item => item.address === address && item.state === "live");
    return observed && observed.cwd === receipt.cwd && JSON.stringify(observed.argv) === JSON.stringify(receipt.argv) &&
      observed.labels?.["ocq.handoff"] === value.pointer_sha256 &&
      observed.labels?.["ocq.hc_binding"] === this.host.nativeHC.approvedDigest() &&
      observed.labels?.["herdr.space"] === scope.space ? address : null;
  }
  showTerminal(scope, value) {
    const receipt = value.terminal;
    if (!receipt || value !== this.client || !this.current(scope) || !this.verifiedClient(value) ||
        this.verification.state !== "live" || receipt.peer !== (scope.machine ?? "local")) throw Error("Native receipt peer/binding changed");
    const address = this.observedTerminal(scope, value);
    if (!address) {
      this.waitingForTerminal = { digest: value.pointer_sha256, scope, generation: this.generation };
      return;
    }
    // Native peek shows even terminals that have not been kept as a tab.
    // Original launch identity is NOT proof of its current OpenCode route.
    run("terminal.peek", { terminal: address });
    this.navigationTarget = null; this.navigationCandidates = null; this.waitingForTerminal = null; this.error = null;
  }
  inspectClient(item, cx, show = false) {
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
          this.host.nativeHC.approvedDigest());
        if (current() && this.authorized(authorization, scope, generation) && hc === this.host.nativeHC.approved) {
          this.acceptClientView(value, scope, item.pointer_sha256);
          if (show && this.verification.state === "live") this.showTerminal(scope, this.client);
          else if (show) this.error = "This retained inspection terminal has finished or is unavailable. No replacement was spawned.";
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
      .child(this.host.panelControl("ocq-main-details", this.showDetails ? "Hide technical details" : "Technical details",
        cx => { this.showDetails = !this.showDetails; cx.notify(); }, theme, this.host.renderTargets)));
    if (this.storageError) box.child(div().text_color(theme.blocked).child("Private attempt evidence unavailable. Starts and client launches disabled."));
    const selected = this.selectedMain;
    if (selected) box.child(this.host.panelControl("ocq-main-open", "Open Main", task => {
      this.work().scroll.scroll_to_item(0); this.openConversation("integrator", "", task);
    }, theme, this.host.renderTargets).font_semibold().bg(theme.hover));
    box.child(this.host.workflow.render(cx, theme, button, selected));
    for (const [index, goal] of (this.goals || []).entries()) {
      box.child(v_flex().min_w(0).gap(3).pt(8)
        .child(div().font_semibold().child(`Goal ${index + 1}`))
        .child(div().text_size(12).text_color(theme.muted).child(this.goalSummary(goal)))
        .when(this.showDetails, e => e.child(div().text_size(12).text_color(theme.muted).child(
          `Last checked: ${this.live?.operation_id === goal.main.operation_id ? this.live.read_at : goal.main.read_at}`)))
        .when(!goal.work.some(v => v.job) && goal.main.state !== "prepared", e =>
          e.child(button(`ocq-main-goal-check-${goal.main.operation_id}`, "Check exact goal status", cx => this.recoverMain(goal.main, cx)))));
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
    if (this.navigationTarget?.kind === "integrator" || this.review?.kind === "main" || this.showClients && this.navigationTarget?.kind !== "worker") {
      this.revealNavigation = false;
      box.child(this.renderNavigation(cx, theme, button));
    }
    return box;
  }
  renderNavigation(cx, theme, button) {
    const box = v_flex().w_full().min_w(0).gap(8).p(10).border(1).border_color(theme.border).rounded(6);
    box.child(div().font_semibold().child(this.navigationTarget?.kind === "worker" ? "Worker conversation" : "Main conversation"));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (this.navigationRecovery && !this.working) {
      const saved = this.navigationRecovery;
      box.child(button("ocq-client-prepare-recover", "Recover saved navigation", task =>
        this.prepareNavigation(saved.kind, saved.work, task, saved.attention, saved.id)));
    }
    if (this.working) box.child(div().text_size(12).text_color(theme.muted).child("Verifying the exact saved conversation…"));
    if (this.waitingForTerminal) {
      const pending = this.waitingForTerminal;
      if (!this.current(pending.scope, pending.generation) || !this.verifiedClient() || this.client.pointer_sha256 !== pending.digest) {
        this.waitingForTerminal = null;
        box.child(div().text_color(theme.blocked).child("The navigation context changed. Verify the saved receipt again; no client will be respawned."));
      } else if (this.observedTerminal(pending.scope, this.client)) {
        this.waitingForTerminal = null;
        cx.spawn(() => {
          if (this.current(pending.scope, pending.generation) && this.client?.pointer_sha256 === pending.digest && this.verifiedClient())
            this.showTerminal(pending.scope, this.client);
        }, { owner: this.host.root || this.host });
      } else box.child(div().text_size(12).text_color(theme.muted).child("Waiting for Herdr to observe the recorded inspection terminal. No additional client is being launched."));
    }
    if (this.navigationTarget && !this.host.nativeHC.approvedDigest())
      box.child(this.host.nativeHC.render(cx, theme, button));
    for (const item of this.navigationCandidates || []) box.child(button(`ocq-client-record-${item.pointer_sha256}`,
      item.terminal ? `Open retained client ${item.terminal.returned_terminal_id}` : "Inspect saved client preparation", task => this.openRetained(item, task)));
    if (this.client?.terminal && !this.working) {
      if (this.verifiedClient() && this.verification.state === "live")
        box.child(button("ocq-client-show-returned", "Open saved conversation", () => this.showTerminal(this.scope, this.client)));
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
      const allowed = review.kind === "main" || review.value.state === "ready_to_open";
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
