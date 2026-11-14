import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { run, terminals } from "herdr";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { exec, compatibility } from "./compatibility.js";
import { decimal } from "./stored-updates.js";

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
  if (!object(value) || value.protocol !== "ocq-client-v1" || value.project_id !== scope.project || !hex(value.pointer_sha256) ||
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
  if (terminal && (!/^t[1-9][0-9]*$/.test(terminal.returned_terminal_id || "") ||
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
    try {
      this.attempts = JSON.parse(localStorage.getItem("main-client-attempts-v1") ?? "{}");
      this.navigations = JSON.parse(localStorage.getItem("native-navigation-v1") ?? "{}");
      if (!object(this.attempts) || !object(this.navigations)) throw Error("Invalid private evidence");
    } catch (_) { this.attempts = {}; this.navigations = {}; this.storageError = true; }
  }
  work() { return this.host.workOperator; }
  current(scope, generation = this.generation) { return this.work().current(scope) && this.work().visible() && this.generation === generation; }
  suspend() { this.generation++; this.review = null; this.working = false; this.error = null; }
  sync(scope) {
    const key = JSON.stringify(scope);
    if (key === this.scopeKey) return;
    this.suspend(); this.scopeKey = key; this.scope = scope;
    this.mains = []; this.clients = []; this.selectedMain = null; this.operation = null; this.client = null; this.live = null;
  }
  apply(scope, main, clients, cx) {
    this.sync(scope);
    if (main?.protocol !== "ocq-main-v1" || main.project_id !== scope.project || !Array.isArray(main.items) ||
      main.items.length > 128 || !Array.isArray(clients) || clients.length > 128) throw Error("Invalid separate Main/client observation");
    this.mains = main.items.map(item => mainStatus(item, scope.project));
    this.selectedMain = main.selected ? mainStatus(main.selected, scope.project) : null;
    this.clients = clients.map(item => clientStatus(item, scope));
    if (new Set(this.mains.map(item => item.operation_id)).size !== this.mains.length ||
      new Set(this.clients.map(item => item.pointer_sha256)).size !== this.clients.length) throw Error("Duplicate Main/client records");
    if (this.operation) this.operation = this.mains.find(item => item.operation_id === this.operation.operation_id) || null;
    if (this.client) this.client = this.clients.find(item => item.pointer_sha256 === this.client.pointer_sha256) || null;
    if (this.review?.kind === "main" && (!this.operation || identity(this.operation) !== identity(this.review.value.status)) ||
      this.review?.kind === "client" && (!this.client || this.client.state !== this.review.value.state)) this.review = null;
    cx.notify();
  }
  base(scope, target) { return [scope.space, scope.queue, scope.project, target]; }
  context(scope, bridge) { return [scope.machine ?? "local", scope.folder, scope.store, bridge.image_sha256]; }
  async call(action, scope, target, contextual = false, tail = [], guard) {
    const bridge = compatibility.status;
    const positions = [...this.base(scope, target), ...(contextual ? this.context(scope, bridge) : []), ...tail];
    const mutation = ["main-start", "client-prepare", "client-open"].includes(action);
    const args = mutation ? [action, ...positions] : [action,
      ...(contextual ? ["--work-observation-context", scope.bridge] : []), "--", ...positions];
    const response = await exec("ocq-herdr", args, { on: scope.machine, timeout: 60000, ...(mutation ? { current: guard } : {}) });
    if (response.code !== 0 || Buffer.from(response.stdout || "").length > 1048576) throw Error("Main/client response unavailable");
    return JSON.parse(response.stdout);
  }
  async check(scope, generation) {
    const checking = compatibility.check(() => this.current(scope, generation), true, true);
    // Pin this check's generation before yielding, including the promise-return
    // gap. A later Recheck cannot lend authority to its earlier response.
    const epoch = compatibility.epoch, serial = compatibility.serial;
    const bridge = await checking;
    const authorization = { epoch, serial, pins: pins(bridge), bridge };
    if (!this.current(scope, generation) || bridge.queue_identity !== scope.queue ||
      bridge.observation_store_context !== scope.store || bridge.observation_context !== scope.bridge ||
      !this.authorized(authorization, scope, generation)) throw Error("Main/client context changed");
    return authorization;
  }
  authorized(authorization, scope, generation) {
    return this.current(scope, generation) && compatibility.workReady() &&
      compatibility.epoch === authorization.epoch && compatibility.serial === authorization.serial &&
      pins(compatibility.status) === authorization.pins;
  }
  freeze(kind, value, scope, generation, authorization) {
    if (!this.authorized(authorization, scope, generation)) throw Error("Review authorization changed");
    this.review = { kind, value, scope, generation, pins: authorization.pins,
      epoch: authorization.epoch, serial: authorization.serial, pages: this.work().pagesFor(value), page: 0, seen: new Set(), confirming: false };
  }
  pinned(review) {
    const currentBinding = this.host.board?.project?.current_binding;
    const revision = review?.kind === "main" ? review.value.binding.revision : review?.value.pointer.binding_revision;
    return review && this.review === review && this.current(review.scope, review.generation) && compatibility.workReady() &&
      compatibility.epoch === review.epoch && compatibility.serial === review.serial && pins(compatibility.status) === review.pins &&
      currentBinding?.repo_root === review.scope.folder && String(currentBinding.revision) === String(revision);
  }
  markerKey(action, scope, target) { return JSON.stringify([action, scope.machine, scope.space, scope.queue, scope.project, target]); }
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
    }); cx.notify();
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
    }); cx.notify();
  }
  navigationKey(scope, kind, work) { return JSON.stringify([scope.machine, scope.space, scope.queue, scope.project, kind, work]); }
  prepareNavigation(kind, work, cx) {
    const scope = this.work().bound;
    this.sync(scope);
    const generation = this.generation;
    if (!this.current(scope) || this.working || this.storageError) return;
    const key = this.navigationKey(scope, kind, work);
    const previous = this.navigations[key];
    const found = previous && this.clients.some(item => item.pointer.navigation_id === previous.id);
    // Unknown preparation reuses its saved ID. A NEW ID requires another
    // explicit action after the original pointer is known, never an auto retry.
    const navigation = previous && previous.phase === "uncertain" && !found ? previous : { id: `open-${randomBytes(16).toString("hex")}`, phase: "uncertain" };
    this.navigations[key] = navigation;
    this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        const authorization = await this.check(scope, generation);
        if (!this.authorized(authorization, scope, generation)) return;
        const value = await this.call("client-prepare", scope, navigation.id, true, [kind, work], () => this.authorized(authorization, scope, generation));
        if (!this.authorized(authorization, scope, generation)) return;
        pointer(value, scope);
        if (value.navigation_id !== navigation.id || value.target_kind !== kind || (value.work_request_id || "") !== work || value.space !== scope.space) throw Error("Different client target");
        navigation.phase = "prepared"; navigation.pointer = value.pointer_sha256;
        localStorage.setItem("native-navigation-v1", JSON.stringify(this.navigations));
        await localStorage.flush();
        if (!this.authorized(authorization, scope, generation)) return;
        this.client = { protocol: "ocq-client-v1", project_id: scope.project, pointer_sha256: value.pointer_sha256,
          state: "ready_to_open", pointer: value, attempt_permitted: false, current_route: "unverified" };
        this.freeze("client", this.client, scope, generation, authorization); this.work().dirty = true;
      } catch (_) { if (this.current(scope, generation)) this.error = "Navigation preparation unavailable. Its ID is retained; recover that preparation, never guess another session or retry auth. No client launch was requested."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  reviewClient(item, cx) {
    const scope = this.scope, generation = this.generation;
    if (!this.current(scope) || this.working || item.pointer.space !== scope.space) return;
    this.review = null; this.client = item; this.error = null; this.working = true;
    cx.spawn(async cx => {
      try {
        const authorization = await this.check(scope, generation);
        if (!this.authorized(authorization, scope, generation)) return;
        const value = clientStatus(await this.call("client-status", scope, item.pointer_sha256), scope, item.pointer_sha256);
        if (!this.authorized(authorization, scope, generation)) return;
        this.client = value; this.freeze("client", value, scope, generation, authorization);
      } catch (_) { if (this.current(scope, generation)) this.error = "Exact navigation pointer unavailable; no client launched."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  confirm(cx) {
    const review = this.review, scope = review?.scope;
    if (!this.pinned(review) || this.working || this.storageError || !review.confirming || review.seen.size !== review.pages.length) return;
    const main = review.kind === "main", action = main ? "main-start" : "client-open";
    const target = main ? review.value.status.operation_id : review.value.pointer_sha256;
    if (this.attempted(action, scope, target)) return;
    const key = this.markerKey(action, scope, target);
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
        const value = await this.call(action, scope, target, true, main ? [review.value.preview_sha256] : [], () => this.pinned(review));
        if (!this.pinned(review)) return;
        if (main) this.live = mainStatus(value, scope.project, target);
        else this.acceptClientView(value, scope, target);
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
    }); cx.notify();
  }
  acceptClientView(value, scope, digest) {
    if (value.protocol !== "ocq-client-v1" || value.current_route !== "unverified" ||
      !["live", "finished", "unobserved"].includes(value.terminal_state)) throw Error("Invalid native terminal observation");
    this.client = clientStatus(value.status, scope, digest);
    this.terminalState = value.terminal_state;
    if (this.client.terminal && value.terminal_state === "live") this.showTerminal(scope, this.client);
  }
  showTerminal(scope, value) {
    const receipt = value.terminal;
    if (!receipt || receipt.peer !== (scope.machine ?? "local")) throw Error("Native receipt peer changed");
    const address = scope.machine === null ? receipt.returned_terminal_id : `${scope.machine}/${receipt.returned_terminal_id}`;
    const observed = terminals().find(item => item.address === address && item.state === "live");
    if (!observed || observed.cwd !== receipt.cwd || JSON.stringify(observed.argv) !== JSON.stringify(receipt.argv) ||
      observed.labels?.["ocq.handoff"] !== value.pointer_sha256) {
      this.error = "Receipt retained; this exact terminal is not yet observed live by Herdr. Inspect its retained receipt again; do not spawn another copy.";
      return;
    }
    // Native peek shows even terminals that have not been kept as a tab.
    // Original launch identity is NOT proof of its current OpenCode route.
    run("terminal.peek", { terminal: address });
  }
  inspectClient(item, cx) {
    const scope = this.scope, generation = this.generation;
    if (!this.current(scope) || this.working || item.pointer.space !== scope.space) return;
    this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const authorization = await this.check(scope, generation);
        if (!this.authorized(authorization, scope, generation)) return;
        const value = await this.call("client-verify", scope, item.pointer_sha256, true);
        if (this.authorized(authorization, scope, generation)) this.acceptClientView(value, scope, item.pointer_sha256);
      } catch (_) { if (this.current(scope, generation)) this.error = "Exact terminal/backend/package observation unavailable. No client was launched or replaced."; }
      finally { if (this.current(scope, generation)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  render(cx, theme, button) {
    this.sync(this.work().bound);
    const box = v_flex().id("ocq-main-client").gap(6);
    box.child(div().font_semibold().child("Main / Integrator"));
    box.child(div().text_size(12).child("Persistent canonical-Main conversation, not a bounded worker or the workspace shell. Setup/profile/role and optional first prompt are authored privately on the server; subsequent goals are entered in the native Main conversation."));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (this.storageError) box.child(div().text_color(theme.blocked).child("Private attempt evidence unavailable. Starts and client launches disabled."));
    const selected = this.selectedMain;
    if (!selected) box.child(div().child("No configured SQL-bound Main is observed for this project. Prepare/adopt the exact canonical checkout and explicit role/profile on the server; no agent, model or session is guessed here."));
    else {
      box.child(div().child(`Bound Main: ${selected.session_id}\nCheckout: ${selected.repo}\nBranch: ${selected.main_branch}\nAnchor: ${selected.commit_anchor}\nStored state: ${selected.state} · not a live completion claim`));
      if (!this.working) box.child(button("ocq-main-open", "Prepare NEW Main inspection client", cx => this.prepareNavigation("integrator", "", cx)))
        .child(button("ocq-main-observe", "Observe exact bound Main · GET only", cx => this.recoverMain(selected, cx)));
    }
    if (!this.working) for (const item of this.mains) box.child(button(`ocq-main-operation-${item.operation_id}`, `${item.mode} · ${item.operation_id} · ${item.state}`, cx => { this.suspend(); this.operation = item; cx.notify(); }));
    if (this.operation) {
      const item = this.operation;
      box.child(div().child(`Main operation ${item.operation_id} · ${item.state}\n${item.reason}\nSetup: ${item.setup_id}\nSession: ${item.session_id}\nPrompt ID: ${item.prompt_id || "none"}\nBinding revision: ${item.binding_revision}\nIntent: ${item.intent_sha256}`));
      if (!this.working) {
        box.child(button("ocq-main-recover", "Recover this exact operation · GET/receipt only", cx => this.recoverMain(item, cx)));
        if (!this.storageError && !this.attempted("main-start", this.scope, item.operation_id) && ["prepared", "binding_committed", "session_observed"].includes(item.state))
          box.child(button("ocq-main-preview", "Retain and review exact Main operation", cx => this.reviewMain(item, cx)));
      }
      if (this.attempted("main-start", this.scope, item.operation_id)) box.child(div().text_color(theme.blocked).child("A Start was potentially invoked. Exact recovery only; no automatic continuation or prompt resend."));
    }
    if (this.live) box.child(div().child(`Explicit Main observation: ${this.live.state} · ${this.live.read_at}\n${this.live.reason}\nDirty checkout: ${this.live.dirty ? "yes (preserved)" : "no"} · HEAD advanced: ${this.live.head_advanced ? "yes; explicit refresh required" : "no"}`));
    box.child(div().font_semibold().child("Retained inspection clients"));
    box.child(div().text_size(12).text_color(theme.muted).child("Each new navigation intent may open one ordinary native client. The backend Main ID/history remains the same. Repeating a pointer recovers its receipt, never spawns again. Current TUI route is unverified after a human session-picker change."));
    if (!this.working) for (const item of this.clients.filter(item => item.pointer.space === this.scope?.space)) {
      box.child(button(`ocq-client-record-${item.pointer_sha256}`, `${item.pointer.target_kind} · ${item.pointer.navigation_id} · ${item.state}`, cx => this.reviewClient(item, cx)));
      if (item.terminal) box.child(button(`ocq-client-show-${item.pointer_sha256}`, "Show existing inspection terminal · current session unverified", cx => this.inspectClient(item, cx)));
    }
    if (this.client && !this.working && !this.clients.some(item => item.pointer_sha256 === this.client.pointer_sha256))
      box.child(div().child(`Retained new pointer: ${this.client.pointer_sha256} · waiting for separate journal observation`));
    if (this.working) box.child(div().child("Reading/performing the exact explicit operation…"));
    const review = this.review;
    if (review) {
      review.seen.add(review.page);
      box.child(div().font_semibold().child(`${review.kind === "main" ? "Main private-byte" : "Native client pointer"} review ${review.page + 1}/${review.pages.length}`));
      box.child(div().child(review.pages[review.page]));
      box.child(h_flex().gap(4)
        .when(review.page > 0, el => el.child(button("ocq-main-review-previous", "Previous retained page", cx => { review.page--; cx.notify(); })))
        .when(review.page < review.pages.length - 1, el => el.child(button("ocq-main-review-next", "Next retained page", cx => { review.page++; cx.notify(); }))));
      const action = review.kind === "main" ? "main-start" : "client-open";
      const target = review.kind === "main" ? review.value.status.operation_id : review.value.pointer_sha256;
      const allowed = review.kind === "main" || review.value.state === "ready_to_open";
      if (!this.pinned(review)) box.child(div().text_color(theme.blocked).child("Review authority suspended; inspect and review again."));
      if (allowed && this.pinned(review) && !this.working && !this.storageError && !this.attempted(action, this.scope, target) && review.seen.size === review.pages.length) {
        if (!review.confirming) box.child(button("ocq-main-review-complete", "All pages seen · review explicit action", cx => { if (this.pinned(review)) review.confirming = true; cx.notify(); }));
        else box.child(div().text_color(theme.blocked).child(review.kind === "main"
          ? "Start only this frozen Main operation? It may create/register Main or send its retained prompt once. No worker capacity or permission approval is granted."
          : "Launch one ordinary inspection client for this exact pointer? The plugin verifies the existing target before selecting it; it never recreates a missing target. Unknown spawn outcome is not retried."))
          .child(button("ocq-main-confirm-back", "Back without invoking", cx => { review.confirming = false; cx.notify(); }))
          .child(button("ocq-main-confirm-once", review.kind === "main" ? "Start exact Main operation once" : "Open this inspection client once", cx => this.confirm(cx)));
      }
      if (review.kind === "client" && this.attempted(action, this.scope, target)) box.child(div().text_color(theme.blocked).child("Client launch may have been invoked. Only exact stored receipt recovery or observation is offered, never a second spawn for this pointer."));
    }
    return box;
  }
}
