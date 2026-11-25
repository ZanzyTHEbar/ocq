import { run, terminals, spaces, panes } from "herdr";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { decimal } from "./observation-version.js";
import { panelEpoch } from "./panel-state.js";

const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9_-]{1,128}$/.test(value);
const object = value => value && typeof value === "object" && !Array.isArray(value);
const pins = value => JSON.stringify(["image_sha256", "bridge_context", "queue_identity", "observation_context", "observation_store_context", "observation_schema"].map(key => value[key]));
const identity = value => JSON.stringify(value && Object.fromEntries(Object.entries(value).filter(([key]) => key !== "read_at")));

function mainStatus(value, project, operation) {
  if (!object(value) || value.protocol !== "ocq-main-v1" || value.project_id !== project || !id(value.operation_id) ||
    operation && value.operation_id !== operation || !["create", "adopt", "replace", "refresh", "role-refresh", "send", "reopen"].includes(value.mode) ||
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
  work() { return this.host.sessions; }
  current(scope, generation = this.generation) { return this.work().current(scope) && this.generation === generation; }
  suspend() {
    this.generation++; this.review = null; this.verification = null; this.working = false; this.error = null;
    this.openIntent = null; this.navigationProof = null;
  }
  sync(scope) {
    const key = JSON.stringify(scope);
    if (key === this.scopeKey) return;
    this.suspend(); this.scopeKey = key; this.scope = scope;
    if (scope) {
      const retentionScope = JSON.stringify([scope.source ?? scope.machine, scope.space, scope.queue, scope.project, scope.store, scope.folder]);
      if (retentionScope !== this.retentionScope) { this.retainedViewers.clear(); this.retentionScope = retentionScope; }
    }
    this.mains = []; this.clients = []; this.goals = []; this.selectedMain = null; this.retirement = null; this.client = null;
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
    if (main.retirement !== undefined && (!["deleted", "uncertain"].includes(main.retirement) || this.selectedMain))
      throw Error("Invalid Main retirement state");
    this.retirement = main.retirement ?? null;
    this.clients = clients.map(item => clientStatus(item, scope));
    for (const item of this.clients) this.rememberClient(item);
    if (new Set(this.mains.map(item => item.operation_id)).size !== this.mains.length ||
      new Set(this.clients.map(item => item.pointer_sha256)).size !== this.clients.length) throw Error("Duplicate Main/client records");
    if (this.client) {
      const observed = this.clients.find(item => item.pointer_sha256 === this.client.pointer_sha256);
      // Journal projections are separate/non-atomic. Their absence cannot erase
      // an exact pointer just returned by the owning runtime's preparation.
      if (observed) this.selectClient(observed);
    }
    if (this.review && (!this.client || this.client.state !== this.review.value.state)) this.review = null;
    cx.notify();
  }
  base(scope, target) { return [scope.space, scope.queue, scope.project, target]; }
  context(scope, bridge) { return [scope.machine ?? "local", scope.folder, scope.store, bridge.image_sha256]; }
  async call(action, scope, target, contextual = false, tail = [], guard, hcBinding) {
    const bridge = this.compatibility.status;
    const positions = [...this.base(scope, target), ...(contextual ? this.context(scope, bridge) : []), ...tail];
    const mutation = ["client-prepare", "client-attention-prepare-v1", "client-open"].includes(action);
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
  freeze(value, scope, generation, authorization) {
    if (!this.authorized(authorization, scope, generation)) throw Error("Review authorization changed");
    const hc = this.host.nativeHC.approved;
    if (!this.host.nativeHC.approvedDigest("navigation")) throw Error("Exact terminal service required");
    this.review = { value, scope, generation, pins: authorization.pins, hc,
      epoch: authorization.epoch, serial: authorization.serial,
      confirming: false };
  }
  pinned(review) {
    return review && this.review === review && this.targetCurrent(review) &&
      this.authorized(review, review.scope, review.generation) &&
      review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest("navigation");
  }
  targetCurrent(review) {
    // The detailed board can be over its history limit even when the exact
    // session projection is current. client-verify and client-open revalidate
    // the pointer's binding digest/revision against SQLite on the owner.
    const p = review.value.pointer, intent = this.openIntent;
    if (!this.intentCurrent(intent) || intent.readOnly ||
        p.space !== intent.scope.space || p.session_id !== intent.session || p.target_kind !== intent.kind ||
        (p.work_request_id || "") !== intent.work) return false;
    const main = this.selectedMain;
    return intent.kind !== "integrator" || (p.cwd === intent.scope.folder &&
      p.setup_id === main?.setup_id && p.binding_revision === main?.binding_revision);
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
  openRefused(cx, message = "The project or tab changed before opening.") {
    if (!this.host.panelCurrent()) return;
    this.error = message;
    cx.notify();
  }
  openConversation(kind, work, cx) {
    const scope = this.work().bound;
    this.sync(scope);
    if (this.working || this.tabNavigation) { cx.notify(); return; }
    this.client = null; this.verification = null; this.review = null;
    this.error = null; this.attachmentNotice = null;
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
          if (panelEpoch() !== intent.marker || this.host.selected !== project ||
              this.host.source?.generation !== sourceGeneration || !this.host.panelCurrent() ||
              this.targetSession(kind, work) !== intent.session) return this.openRefused(task);
          if (this.compatibility.workReady()) this.openConversation(kind, work, task);
          else this.error = this.host.error || "Conversation navigation needs a verified project and native launch authority. No client was opened.";
        } catch (error) { if (this.openIntent === intent) { this.working = false; this.error = error.message || "Project verification failed; no client was opened."; } }
        task.notify();
      }, { owner: this.host.root || this.host });
      cx.notify(); return;
    }
    this.navigateWithHC(kind, work, cx);
  }
  navigateWithHC(kind, work, cx) {
    if (this.host.nativeHC.approvedDigest("navigation")) this.navigateApproved(kind, work, cx);
    else {
      const intent = this.openIntent, context = this.host.nativeHC.context();
      this.working = true;
      cx.spawn(async task => {
        let handedOff = false;
        try {
          const review = await this.host.nativeHC.load(context);
          if (!this.intentCurrent(intent) || context !== this.host.nativeHC.context()) {
            if (this.openIntent === intent) this.openRefused(task, "The conversation context changed during verification.");
            return;
          }
          if (!this.host.source?.runtime?.core_id || review.value.binding.core_id !== this.host.source.runtime.core_id)
            throw Error("The verified terminal service does not match this project's authenticated core.");
          // The explicit Open action consents to navigation on this exact
          // current identity only. It never answers OpenCode permissions.
          review.navigationOnly = true;
          this.host.nativeHC.approved = review;
          this.working = false; handedOff = true; this.navigateApproved(kind, work, task);
        } catch (_) { if (this.openIntent === intent) this.error = "The owning terminal service could not be verified. Nothing was opened."; }
        finally { if (this.openIntent === intent && !handedOff) { this.working = false; task.notify(); } }
      });
    }
    cx.notify();
  }
  targetSession(kind, work) {
    if (kind === "integrator") return this.selectedMain?.session_id;
    return this.work().rows.find(v => v.request_id === work && !v.job.session_state)?.job.session_id;
  }
  navigateApproved(kind, work, cx) {
    const scope = this.scope, generation = this.generation, intent = this.openIntent;
    if (this.working) return;
    if (!intent || !this.current(scope, generation)) return this.openRefused(cx);
    this.working = true;
    cx.spawn(async task => {
      let handedOff = false;
      try {
        const authorization = await this.check(scope, generation);
        if (!intent.readOnly) {
          const attached = await this.verifyAttached(intent, authorization);
          if (attached) {
            this.acceptClientView(attached, scope, attached.status.pointer_sha256);
            this.showTerminal(scope, this.client, task, true);
            return;
          }
        }
        const proof = await this.observeViewers(intent, authorization);
        if (!this.intentCurrent(intent)) {
          if (this.openIntent === intent) this.openRefused(task);
          return;
        }
        this.navigationProof = proof;
        this.working = false; handedOff = true;
        this.navigateObserved(kind, work, task, proof);
      } catch (_) { if (this.current(scope, generation)) this.error = "Could not verify the saved opening. No new viewer was created."; }
      finally { if (!handedOff && this.current(scope, generation)) { this.working = false; task.notify(); } }
    });
    cx.notify();
  }
  async verifyAttached(intent, authorization) {
    const { scope, generation, kind, work, session } = intent;
    const hc = this.host.nativeHC.approved, context = this.host.nativeHC.context();
    const current = () => this.intentCurrent(intent) && this.authorized(authorization, scope, generation) &&
      hc === this.host.nativeHC.approved && context === this.host.nativeHC.context();
    const projectSpace = spaces().find(s => s.machine === scope.machine &&
      s.id === (scope.machine === null ? scope.space : `${scope.machine}/${scope.space}`));
    if (!projectSpace || !current()) return null;
    const candidates = this.clients.filter(v => v.state === "terminal_recorded" && v.terminal &&
      v.terminal.peer === (scope.machine ?? "local") && v.pointer.target_kind === kind &&
      v.pointer.space === scope.space && v.pointer.session_id === session && (v.pointer.work_request_id || "") === work &&
      panes().some(p => p.terminal === (scope.machine === null ? v.terminal.returned_terminal_id :
        `${scope.machine}/${v.terminal.returned_terminal_id}`) && projectSpace.tabs.some(t => t.id === p.tab)));
    if (candidates.length !== 1) return null;
    const [stored] = candidates;
    let view;
    try { view = await this.call("client-verify", scope, stored.pointer_sha256, true, [], current, hc.value.binding_sha256); }
    catch (_) { return null; }
    if (!current() || view.terminal_state !== "live") return null;
    const value = this.checkedClientView(view, scope, stored.pointer_sha256, stored);
    return this.targetCurrent({ value }) ? view : null;
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
    const fresh = new Map(listed.filter(matches).map(v => [v.pointer_sha256, v]));
    for (const [digest, old] of candidates) {
      try {
        // The list provides a fresh stored receipt. client-verify already
        // reloads it and fences exact native identity, so no separate status
        // command is needed for each viewer.
        const status = fresh.get(digest);
        const view = await this.call("client-verify", scope, digest, true, [], current, hc.value.binding_sha256);
        if (!current()) throw Error("Conversation context changed.");
        let client;
        try {
          client = this.checkedClientView(view, scope, digest, old || status);
          if (!matches(client) || status && (identity(status.pointer) !== identity(client.pointer) ||
              status.terminal && identity(status.terminal) !== identity(client.terminal))) throw Error();
        } catch (_) { throw conflict("Returned viewer identity does not match its retained record."); }
        const p = client.pointer;
        const identityPin = JSON.stringify([p.cwd, p.binding_revision, p.binding_sha256, p.service_identity,
          p.server_origin, p.opencode_project_id, p.ownership_sha256, p.profile_sha256]);
        if (targetIdentity && targetIdentity !== identityPin) throw conflict("Conflicting conversation ownership/profile/service identities.");
        targetIdentity = identityPin;
        const state = client.state === "blocked" ? "unobservable" :
          client.terminal && view.terminal_state === "live" ? "live" :
          client.terminal && view.terminal_state === "finished" && client.state === "terminal_recorded" ? "finished" :
          !client.terminal && view.terminal_state === "unobserved" && client.state === "ready_to_open" &&
            !this.attempted("client-open", scope, digest) ? "not_invoked" : "unobservable";
        rows.push({ client, view, state });
      } catch (error) {
        if (!current() || error.identityConflict) throw error;
        rows.push({ client: old || fresh.get(digest), state: "unobservable", digest });
      }
    }
    if (!current()) throw Error("Conversation context changed.");
    const unresolvedLocal = saved.some(n => !n.pointer && !rows.some(r => r.client?.pointer.navigation_id === n.id));
    this.clients = [...new Map([...retained, ...listed].map(v => [v.pointer_sha256, v])).values()];
    for (const item of this.clients) this.rememberClient(item);
    return { intent, authorization, hc, context, rows, unresolvedLocal, local: local.map(n => n.pointer) };
  }
  navigateObserved(kind, work, cx, proof) {
    if (!this.intentCurrent(proof?.intent)) return this.openRefused(cx);
    const live = proof.rows.filter(r => r.state === "live");
    const choose = rows => proof.local.map(d => rows.find(r => r.client.pointer_sha256 === d)).find(Boolean) || rows[0];
    const blocked = proof.rows.filter(r => r.state === "unobservable");
    const blockedMessage = blocked.some(r => r.client?.terminal) ? "An earlier viewer can’t be verified. OCQ won’t start a replacement." :
      "An earlier opening is unconfirmed. OCQ won’t repeat it.";
    if (proof.intent.readOnly) {
      this.error = live.length ? (blocked.length ? "The viewer is live, but an earlier opening is still unconfirmed." : "The viewer is live. Nothing was opened or moved.") :
        proof.unresolvedLocal || blocked.length ? blockedMessage :
        proof.rows.some(r => r.state === "not_invoked") ? "The opening is prepared. No viewer has been launched." :
        proof.rows.length ? "Earlier viewers are closed. Nothing was opened." : "No viewer has been recorded. Nothing was opened.";
      cx.notify(); return;
    }
    if (!this.proofCurrent(proof)) return this.openRefused(cx);
    if (live.length) {
      const selected = choose(live);
      this.acceptClientView(selected.view, proof.intent.scope, selected.client.pointer_sha256);
      this.showTerminal(proof.intent.scope, this.client, cx);
    } else if (proof.unresolvedLocal || blocked.length) {
      this.error = blockedMessage;
    } else {
      const ready = choose(proof.rows.filter(r => r.state === "not_invoked"));
      if (ready) {
        this.selectClient(ready.client);
        this.freeze(ready.client, proof.intent.scope, proof.intent.generation, proof.authorization);
        this.continueOpen(cx);
      } else {
        this.prepareNavigation(kind, work, cx, proof);
      }
    }
    cx.notify();
  }
  prepareNavigation(kind, work, cx, proof) {
    const scope = this.work().bound;
    const generation = this.generation;
    if (!this.current(scope) || this.working || this.storageError || !this.compatibility.nativeReady("navigation") ||
        !this.proofCurrent(proof) || proof.intent.kind !== kind || proof.intent.work !== work ||
        proof.unresolvedLocal || proof.rows.some(r => r.state !== "finished"))
      return this.openRefused(cx, "Could not verify a safe opening. Check opening status.");
    const key = this.navigationKey(scope, `attention-${kind}`, work);
    const former = this.navigations[key];
    const history = [...(former?.history || []), ...proof.rows.map(r => ({
      id: r.client.pointer.navigation_id, pointer: r.client.pointer_sha256, state: "viewer_finished",
    }))];
    const navigation = { id: `open-${randomBytes(16).toString("hex")}`, phase: "uncertain",
      history: [...new Map(history.map(v => [v.pointer, v])).values()] };
    this.navigations[key] = navigation;
    this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        const authorization = proof.authorization;
        if (!this.proofCurrent(proof)) return this.openRefused(cx);
        const value = await this.call("client-attention-prepare-v1", scope, navigation.id, true, [kind, work], () => this.proofCurrent(proof));
        if (!this.proofCurrent(proof)) return this.openRefused(cx);
        pointer(value, scope);
        if (value.navigation_id !== navigation.id || value.target_kind !== kind || (value.work_request_id || "") !== work || value.space !== scope.space ||
            this.openIntent && value.session_id !== this.openIntent.session) throw Error("Different client target");
        navigation.phase = "prepared"; navigation.pointer = value.pointer_sha256;
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        if (!this.proofCurrent(proof)) return this.openRefused(cx);
        this.selectClient({ protocol: "ocq-client-v1", project_id: scope.project, pointer_sha256: value.pointer_sha256,
          state: "ready_to_open", pointer: value, attempt_permitted: false, current_route: "unverified" });
        this.freeze(this.client, scope, generation, authorization);
      } catch (error) { if (this.current(scope, generation)) this.error = `${error.message || "Navigation preparation failed"} Saved preparation ID retained; no client launch was requested.`; }
      finally { if (this.current(scope, generation)) { this.working = false; if (this.review) this.continueOpen(cx); cx.notify(); } }
    }); cx.notify();
  }
  continueOpen(cx) {
    const review = this.review, scope = review?.scope;
    if (this.working) return;
    if (!this.pinned(review) || this.storageError || !this.proofCurrent() || review.value.state !== "ready_to_open")
      return this.openRefused(cx, "Could not verify this conversation. Check opening status.");
    const action = "client-open", target = review.value.pointer_sha256;
    if (this.attempted(action, scope, target))
      return this.openRefused(cx, "An opening is already recorded. Check opening status.");
    const key = this.markerKey(action, scope, target);
    const completionCurrent = () => this.authorized(review, scope, review.generation) && this.targetCurrent(review) &&
      review.hc === this.host.nativeHC.approved && !!this.host.nativeHC.approvedDigest("navigation");
    const attempt = { state: "not_invoked", digest: target };
    this.attempts[key] = attempt;
    this.working = true; this.error = null;
    cx.spawn(async cx => {
      let invoked = false;
      try {
        await this.saveAttempts();
        if (!this.pinned(review)) throw Error("Context changed");
        // Read the entire retained set again immediately before arming.
        const proof = await this.observeViewers(this.openIntent, review);
        if (!this.pinned(review) || !this.intentCurrent(proof.intent) || proof.unresolvedLocal ||
            proof.rows.some(r => r.state === "live" || r.state === "unobservable") ||
            !proof.rows.some(r => r.client?.pointer_sha256 === target && r.state === "not_invoked"))
          throw Error("Another viewer is live, pending or unobservable");
        this.navigationProof = proof;
        attempt.state = "uncertain"; await this.saveAttempts();
        if (!this.pinned(review) || !this.proofCurrent()) throw Error("Context changed");
        invoked = true;
        const value = await this.call(action, scope, target, true, [],
          () => this.pinned(review) && this.proofCurrent(), review.hc.value.binding_sha256);
        // A successful open changes the observed client state and may clear its
        // review. Keep the original dispatch fence, but validate completion by
        // the same scope/pins/HC identity rather than that expected old state.
        if (!completionCurrent()) return this.openRefused(cx, "The project or tab changed. Check opening status; no launch will be repeated.");
        this.acceptClientView(value, scope, target);
        // client-open records the exact fresh terminal.get result. Herdr
        // still checks the native terminal and pane before attaching/focusing.
        if (this.verification.state === "live") this.showTerminal(scope, this.client, cx);
        else this.error = "Launch is recorded but the viewer is not verified live. OCQ won’t repeat it.";
      } catch (_) {
        if (!invoked && this.attempts[key] === attempt) {
          attempt.state = "not_invoked";
          try { await this.saveAttempts(); } catch (_) { attempt.state = "uncertain"; this.storageError = true; }
        }
        if (this.current(scope, review.generation)) this.error = invoked
          ? "The owner did not confirm this launch. Check opening; OCQ won’t repeat it."
          : "Could not verify a safe launch. No launch was requested.";
      } finally {
        if (this.current(scope, review.generation)) { this.review = null; this.working = false; cx.notify(); }
      }
    }, { owner: this.host.root || this.host }); cx.notify();
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
  showTerminal(scope, value, cx, attachedOnly = false) {
    const receipt = value.terminal;
    if (!receipt || value !== this.client || !this.current(scope) || !this.verifiedClient(value) ||
        this.verification.state !== "live" || receipt.peer !== (scope.machine ?? "local") ||
        !this.intentCurrent() || this.openIntent.readOnly) throw Error("Native navigation intent or receipt changed");
    if (this.tabNavigation) return;
    const address = scope.machine === null ? receipt.returned_terminal_id : `${scope.machine}/${receipt.returned_terminal_id}`;
    const spaceID = scope.machine === null ? scope.space : `${scope.machine}/${scope.space}`;
    const space = spaces().find(s => s.id === spaceID && s.machine === scope.machine);
    if (!space) throw Error("The receipt's existing project space is unavailable; no replacement space was created.");
    const ticket = { address, spaceID, scope, intent: this.openIntent, attachedOnly,
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
          if (terminal && ticket.phase === "observing" && !ticket.attachedOnly) {
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
    if (!previous || !this.current(scope) || this.targetSession(previous.kind, previous.work) !== previous.session) {
      this.error = "This conversation is no longer current. Nothing was opened."; cx.notify(); return;
    }
    this.tabNavigation = null; // Cancel any pending selection; keep evidence stays.
    this.openIntent = { ...previous, scope, generation: this.generation, readOnly: true,
      marker: panelEpoch(), origin: this.navigationOrigin() };
    this.navigationProof = null;
    this.navigateWithHC(previous.kind, previous.work, cx);
  }
}
