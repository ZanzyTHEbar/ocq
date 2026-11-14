import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { Buffer } from "buffer";
import { createHash } from "crypto";
import { exec, compatibility } from "./compatibility.js";
import { decimal } from "./stored-updates.js";

const protocol = "ocq-work-v1";
const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const kinds = ["implementation", "capture", "review", "review_result"];
const states = ["preparing", "prepared", "armed", "uncertain", "recorded_observed", "blocked"];
const object = value => value && typeof value === "object" && !Array.isArray(value);
const keys = (value, allowed) => object(value) && Object.keys(value).every(key => allowed.includes(key));
const hash = value => createHash("sha256").update(value, "utf8").digest("hex");
const pins = v => JSON.stringify([v.image_sha256, v.bridge_context, v.queue_identity, v.observation_context, v.observation_store_context, v.observation_schema]);

function status(value, project, request) {
  if (!keys(value, ["protocol", "project_id", "request_id", "kind", "state", "intent_sha256", "created_at", "binding_revision", "parent_request_id", "receipt", "job", "result"]) ||
    value.protocol !== protocol || value.project_id !== project || !id(value.request_id) || (request && value.request_id !== request) ||
    !kinds.includes(value.kind) || !states.includes(value.state) || (value.intent_sha256 !== "" && !hex(value.intent_sha256)) ||
    typeof value.created_at !== "string" || !decimal(value.binding_revision) || (value.parent_request_id !== undefined && !id(value.parent_request_id))) throw Error("Invalid exact work status");
  if (value.job && (!keys(value.job, ["job_id", "sequence", "state", "outcome", "reason", "session_id", "worktree", "terminal_proven"]) ||
    typeof value.job.job_id !== "string" || !decimal(value.job.sequence) || typeof value.job.terminal_proven !== "boolean" ||
    ["state", "outcome", "reason", "session_id", "worktree"].some(key => typeof value.job[key] !== "string"))) throw Error("Invalid work job");
  if (value.result && (!keys(value.result, ["association_id", "sequence", "job_id", "manifest_digest", "source", "verification", "verdict"]) ||
    !hex(value.result.manifest_digest) || !decimal(value.result.sequence) ||
    ["association_id", "job_id", "source", "verification", "verdict"].some(key => typeof value.result[key] !== "string"))) throw Error("Invalid work association");
  if (value.receipt && (!keys(value.receipt, ["key", "job_id", "project_id", "binding_revision", "role", "brief_digest", "target_job_id", "target_result_digest", "envelope_digest", "recorded_at"]) ||
    value.receipt.project_id !== project || !decimal(value.receipt.binding_revision))) throw Error("Invalid work receipt");
  return value;
}

// One project-scoped auxiliary of StoredUpdates, not another poller/scheduler.
export class WorkOperator {
  constructor(host) {
    this.host = host; this.generation = 0; this.dirty = true; this.rows = [];
    try {
      this.attempts = JSON.parse(localStorage.getItem("work-attempts-v1") ?? "{}");
      if (!object(this.attempts)) throw Error("Invalid attempt evidence");
    } catch (_) { this.attempts = {}; this.storageError = true; }
  }

  scope() {
    const h = this.host, observation = h.updates.binding;
    if (!h.selected || !observation || !hex(observation.queue_identity)) return null;
    const machine = h.machine, space = h.updates.scope?.localSpace;
    if (!/^sp[0-9a-f]{32}$/.test(space || "")) return null;
    return { project: h.selected, machine, space, folder: h.folder, queue: observation.queue_identity,
      store: observation.store_context, bridge: observation.bridge_context, epoch: h.panelEpoch, owner: h.generation };
  }
  current(scope) {
    return !this.host.closed && this.host.panelCurrent() && scope && JSON.stringify(scope) === JSON.stringify(this.scope());
  }
  visible() {
    const h = this.host;
    return !h.queueMode && !h.keyboardMode && h.workspaceView === "projects" && h.projectView === "operator" &&
      !h.worktreeCheck.screen && !h.publication.screen && !(h.intake.screen && !h.intake.authoritySuspended) &&
      !(h.compose.screen && !h.compose.authoritySuspended);
  }
  sync() {
    const scope = this.scope(), key = JSON.stringify(scope);
    if (key !== this.scopeKey) {
      const sameProject = scope && this.bound && ["project", "machine", "space", "folder", "queue", "store", "bridge"].every(key => scope[key] === this.bound[key]);
      const selectedID = sameProject ? this.selectedID : null;
      this.suspend(); this.scopeKey = key; this.bound = scope;
      this.rows = []; this.cursor = null; this.readAt = null; this.dirty = true; this.selectedID = selectedID; this.selected = null; this.output = null;
    }
  }
  suspend(reason = null) {
    this.host.mainClient?.suspend();
    this.generation++; this.preview = null; this.pages = null; this.confirming = false;
    this.reviewScope = null; this.working = false; this.dirty = true; this.error = reason;
  }
  observation() {
    this.sync();
    if (!this.bound || !this.visible()) return null;
    return { target: this, scope: this.bound, generation: this.generation,
      cursor: this.cursor || { project_id: this.bound.project, fingerprint: "" },
      needsRead: this.dirty || this.sqlRevision !== this.host.updates.revision };
  }
  apply(capture, value, revision, cx, main, clients) {
    if (!capture || capture.target !== this || capture.generation !== this.generation || !this.current(capture.scope)) return;
    if (!keys(value, ["protocol", "project_id", "items", "journal"]) || value.protocol !== protocol || value.project_id !== capture.scope.project ||
      !Array.isArray(value.items) || value.items.length > 128 || value.journal?.project_id !== value.project_id ||
      value.journal.consistency !== "filesystem_non_atomic" || !hex(value.journal.fingerprint) || typeof value.journal.read_at !== "string") throw Error("Invalid separate work observation");
    const rows = value.items.map(item => status(item, value.project_id));
    if (new Set(rows.map(item => item.request_id)).size !== rows.length) throw Error("Duplicate work pointers");
    const selected = rows.find(item => item.request_id === this.selectedID);
    if (this.preview && (!selected || JSON.stringify(selected) !== JSON.stringify(this.preview.status))) {
      this.preview = null; this.pages = null; this.confirming = false;
      this.error = "Exact stored request state changed. Review a fresh preview; no confirmation was sent.";
    }
    this.rows = rows; this.selected = selected || null;
    this.cursor = { project_id: value.project_id, fingerprint: value.journal.fingerprint };
    this.readAt = value.journal.read_at; this.sqlRevision = revision; this.dirty = false;
    this.host.mainClient.apply(capture.scope, main, clients, cx);
    cx.notify();
  }
  hint(capture) {
    if (capture && capture.target === this && capture.generation === this.generation && this.current(capture.scope)) this.dirty = true;
  }
  attemptKey(scope, item) { return JSON.stringify([scope.machine, scope.space, scope.queue, scope.project, item.request_id, item.intent_sha256]); }
  attempted() {
    const marker = this.selected && this.bound && this.attempts[this.attemptKey(this.bound, this.selected)];
    return marker && marker.state !== "not_invoked";
  }
  choose(item, cx) {
    this.suspend(); this.selectedID = item.request_id; this.selected = item; this.page = 0; this.output = null; cx.notify();
  }
  async call(action, scope, extra = []) {
    const args = [action, ...(action === "work-preview" ? ["--work-observation-context", scope.bridge] : []),
      "--", scope.space, scope.queue, scope.project, ...(action === "work-list" ? [] : [this.selectedID]), ...extra];
    const r = await exec("ocq-herdr", args, { on: scope.machine, timeout: 20000 });
    if (r.code !== 0 || Buffer.from(r.stdout || "").length > 1048576) throw Error("Work read unavailable");
    return JSON.parse(r.stdout);
  }
  async check(scope, generation) {
    return compatibility.check(() => this.current(scope) && this.generation === generation, true, true);
  }
  pinned() {
    const p = this.reviewScope;
    const binding = this.host.board?.project?.current_binding;
    return p && this.visible() && this.current(p.scope) && this.generation === p.generation && compatibility.workReady() &&
      compatibility.epoch === p.epoch && compatibility.serial === p.serial && pins(compatibility.status) === p.pins &&
      binding && String(binding.revision) === String(this.preview?.binding.revision) && binding.repo_root === p.scope.folder;
  }
  contextArgs(scope, bridge) { return [scope.machine ?? "local", scope.folder, scope.store, bridge.image_sha256]; }
  pagesFor(value) {
    const escaped = JSON.stringify(value).replace(/[\u007f-\uffff]/g, char => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`);
    const pages = [];
    for (let offset = 0; offset < escaped.length; offset += 1200) pages.push(escaped.slice(offset, offset + 1200).match(/.{1,60}/g).join("\n"));
    return pages;
  }
  previewRequest(cx) {
    const scope = this.bound, selected = this.selected;
    if (!this.visible() || !this.current(scope) || this.working || !selected || selected.state !== "prepared" || this.attempted() || this.storageError) return;
    this.working = true; this.error = null; this.output = null; this.preview = null;
    const generation = this.generation;
    cx.spawn(async cx => {
      try {
        const bridge = await this.check(scope, generation);
        if (!this.visible() || !this.current(scope) || this.generation !== generation || bridge.queue_identity !== scope.queue ||
          bridge.observation_store_context !== scope.store || bridge.observation_context !== scope.bridge) throw Error("Observation/authorization binding changed");
        const serial = compatibility.serial, epoch = compatibility.epoch;
        const value = await this.call("work-preview", scope, this.contextArgs(scope, bridge));
        if (!this.visible() || !this.current(scope) || this.generation !== generation || serial !== compatibility.serial || epoch !== compatibility.epoch) return;
        status(value.status, scope.project, selected.request_id);
        const c = value.context;
        if (!keys(value, ["protocol", "status", "context", "preview_sha256", "binding", "input_json", "payload", "payload_sha256"]) ||
          value.protocol !== protocol || !hex(value.preview_sha256) || !hex(value.payload_sha256) ||
          typeof value.input_json !== "string" || typeof value.payload !== "string" || hash(value.payload) !== value.payload_sha256 ||
          value.status.intent_sha256 !== selected.intent_sha256 || value.status.state !== "prepared" ||
          c?.protocol !== protocol || c.peer !== (scope.machine ?? "local") || c.space !== scope.space || c.folder !== scope.folder ||
          c.queue_identity !== scope.queue || c.store_context !== scope.store || c.image_sha256 !== bridge.image_sha256 || c.image_role !== "bridge" || c.action !== "confirm" ||
          value.binding?.project_id !== scope.project || value.binding.repo_root !== scope.folder ||
          String(value.binding.revision) !== value.status.binding_revision) throw Error("Preview did not match exact request/context");
        this.preview = value; this.pages = this.pagesFor(value); this.page = 0; this.seen = new Set();
        this.reviewScope = { scope, generation, serial, epoch, pins: pins(bridge) };
      } catch (_) {
        if (this.current(scope) && this.generation === generation) this.error = "Preview/context unavailable. Nothing confirmed. Private preview evidence may be retained on the server.";
      } finally { if (this.current(scope) && this.generation === generation) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  recover(cx) {
    this.read("work-recover", cx);
  }
  read(action, cx) {
    const scope = this.bound, request = this.selectedID, generation = this.generation;
    if (!this.current(scope) || this.working || !id(request)) return;
    this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const value = await this.call(action, scope);
        if (!this.current(scope) || this.generation !== generation || this.selectedID !== request) return;
        if (["work-status", "work-recover"].includes(action)) {
          this.selected = status(value, scope.project, request);
          this.preview = null; this.pages = null; this.confirming = false; this.dirty = true;
          this.output = { title: "Exact server record / receipt · read only", value };
        } else if (action === "work-output") {
          status(value.status, scope.project, request);
          if (value.protocol !== protocol || typeof value.text !== "string" || Buffer.from(value.text).length > 16384 ||
            !hex(value.sha256) || hash(value.text) !== value.sha256 || typeof value.provenance !== "string") throw Error("Invalid exact output");
          this.output = { title: `Final assistant output · ${value.provenance} · not validation`, value };
        } else {
          if (value.protocol !== protocol || value.integration_authorized !== false || value.validation !== "not_run" ||
            !["ready_for_integrator_review", "not_ready"].includes(value.readiness) || value.review_result?.request_id !== request ||
            typeof value.implementation_manifest !== "string" || typeof value.review_manifest !== "string") throw Error("Invalid integration handoff");
          for (const part of ["implementation", "capture", "review", "review_result"]) status(value[part], scope.project);
          this.output = { title: `${value.readiness} · integrator review only; integration NOT authorized`, value };
        }
        this.output.pages = this.pagesFor(this.output.value); this.output.page = 0;
      } catch (_) {
        if (this.current(scope) && this.generation === generation) this.error = "Exact stored record/output unavailable. Missing evidence never permits resend, recapture, replacement, or integration.";
      } finally { if (this.current(scope) && this.generation === generation) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  confirm(cx) {
    const preview = this.preview, scope = this.bound;
    if (this.working || !preview || !this.confirming || !this.pinned() || this.attempted() ||
      this.storageError || this.seen.size !== this.pages.length) return;
    // Retain only nonsecret pointers BEFORE invoking the transport. A missing
    // server receipt does not undo this client uncertainty marker.
    const key = this.attemptKey(scope, this.selected);
    this.attempts[key] = { preview_sha256: preview.preview_sha256, state: "not_invoked" };
    try { localStorage.setItem("work-attempts-v1", JSON.stringify(this.attempts)); }
    catch (_) { this.storageError = true; this.error = "Cannot retain attempt evidence. Nothing sent."; cx.notify(); return; }
    this.working = true; this.confirming = false; this.error = null;
    const generation = this.generation;
    cx.spawn(async cx => {
      let invoked = false;
      try {
        await localStorage.flush();
        if (!this.pinned() || this.preview !== preview) throw Error("Context changed");
        this.attempts[key].state = "uncertain";
        localStorage.setItem("work-attempts-v1", JSON.stringify(this.attempts));
        await localStorage.flush();
        if (!this.pinned() || this.preview !== preview) throw Error("Context changed");
        invoked = true;
        const r = await exec("ocq-herdr", ["work-confirm", scope.space, scope.queue, scope.project, preview.status.request_id,
          ...this.contextArgs(scope, compatibility.status), preview.preview_sha256],
          { on: scope.machine, timeout: 30000, current: () => this.pinned() && this.preview === preview });
        if (r.code !== 0 || Buffer.from(r.stdout || "").length > 1048576) throw Error("Confirmation response unavailable");
        const value = status(JSON.parse(r.stdout), scope.project, preview.status.request_id);
        if (value.intent_sha256 !== preview.status.intent_sha256) throw Error("Different work receipt");
        if (this.current(scope) && this.generation === generation) { this.selected = value; this.dirty = true; }
      } catch (_) {
        if (!invoked) {
          // This is positive local evidence: nativeExec was never entered.
          // Never use this path once invocation has become ambiguous.
          this.attempts[key].state = "not_invoked";
          try {
            localStorage.setItem("work-attempts-v1", JSON.stringify(this.attempts));
            await localStorage.flush();
          } catch (_) { this.attempts[key].state = "uncertain"; this.storageError = true; }
        }
        if (this.current(scope) && this.generation === generation) this.error = invoked
          ? "Confirmation outcome uncertain or refused. Never resend. Read the exact server record/receipt."
          : "No confirmation transport was invoked. Review a fresh preview in the current context; unavailable durable evidence keeps actions blocked.";
      } finally {
        if (this.current(scope) && this.generation === generation) {
          this.preview = null; this.pages = null; this.working = false; cx.notify();
        }
      }
    }); cx.notify();
  }
  render(cx, theme, button) {
    this.sync();
    const box = v_flex().id("ocq-work-operator").gap(8);
    box.child(div().font_semibold().child("Project operator"));
    box.child(div().text_size(12).text_color(theme.muted).child("Main / Integrator authors requests on this server. Each confirmation admits or publishes one frozen request, not a background workflow or merge authorization."));
    if (!this.bound) return box.child(div().child("Choose an existing project in this selected queue."));
    box.child(this.host.mainClient.render(cx, theme, button));
    box.child(div().font_semibold().child("Prepared bounded assignments"));
    box.child(div().text_size(12).text_color(theme.muted).child(this.readAt ? `Journal read ${this.readAt} · filesystem_non_atomic · separate from the SQLite snapshot` : "Discovering this project's prepared requests automatically…"));
    if (this.storageError) box.child(div().text_color(theme.blocked).child("Private client attempt evidence unavailable. Confirmations disabled."));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (this.readAt && !this.rows.length) box.child(div().child("No server-prepared requests yet. No ID needs to be copied here."));
    for (const item of this.rows) box.child(button(`ocq-work-select-${item.request_id}`, `${item.kind} · ${item.request_id} · ${item.state}`, cx => this.choose(item, cx)));
    const item = this.selected;
    if (!item) return box;
    box.child(div().font_semibold().child(`${item.kind} · ${item.request_id}`));
    box.child(div().child(`State: ${item.state}\nBinding revision: ${item.binding_revision}\nIntent: ${item.intent_sha256}\nParent request: ${item.parent_request_id || "none"}`));
    if (item.state === "recorded_observed") box.child(div().child("Exact admission/publication observed. This is NOT proof of execution completion or correctness."));
    if (item.job) {
      box.child(div().child(`Job: ${item.job.job_id} · ${item.job.state} · ${item.job.outcome || "no terminal outcome"}\nTerminal evidence: ${item.job.terminal_proven ? "stored proof" : "not proven"}\nValidation: not_run`));
      box.child(button("ocq-work-job", "Inspect exact job / recorded session", cx => {
        this.suspend();
        this.host.workspaceView = "projects"; this.host.projectView = "work";
        if (this.host.detailJob !== item.job.job_id) this.host.inspect(item.job.job_id, cx);
        this.host.openTarget("job", item.job.job_id, cx);
      }));
      if (!this.working && ["implementation", "review"].includes(item.kind) && item.job.session_id)
        box.child(button("ocq-client-worker-new", "Prepare NEW exact-worker inspection client", cx => this.host.mainClient.prepareNavigation("worker", item.request_id, cx)));
    }
    if (item.result) box.child(button("ocq-work-result", "Read exact result manifest", cx => { this.suspend(); this.host.openTarget("manifest", item.result.manifest_digest, cx); }));
    if (this.attempted()) box.child(div().text_color(theme.blocked).child("A client confirmation attempt is retained. Use read-only exact recovery; no resend is offered, even if no server receipt exists."));
    if (!this.working) {
      box.child(button("ocq-work-status", "Read exact request status", cx => this.read("work-status", cx)));
      box.child(button("ocq-work-recover", "Recover exact server record / receipt · read only", cx => this.recover(cx)));
      if (item.job?.terminal_proven) box.child(button("ocq-work-output", "Read exact final assistant output", cx => this.read("work-output", cx)));
      if (item.kind === "review_result" && item.state === "recorded_observed") box.child(button("ocq-work-handoff", "Read exact integration handoff", cx => this.read("work-handoff", cx)));
      if (item.state === "prepared" && !this.attempted() && !this.storageError && !this.preview)
        box.child(button("ocq-work-preview", "Retain and review exact server preview", cx => this.previewRequest(cx)));
    } else box.child(div().child("Reading/confirming exact pointer…"));
    if (this.preview) {
      const valid = this.pinned();
      if (!valid) box.child(div().text_color(theme.blocked).child("Preview authority suspended. Read status, then review afresh."));
      this.seen.add(this.page);
      box.child(div().font_semibold().child(`Retained-byte review ${this.page + 1}/${this.pages.length}`));
      box.child(div().text_size(12).child("Full escaped JSON, including original input, exact payload, profile/ordered permissions, binding and caller context. Preview retains private evidence; it does not submit."));
      box.child(div().child(this.pages[this.page]));
      box.child(h_flex().gap(4)
        .when(this.page > 0, el => el.child(button("ocq-work-review-previous", "Previous page", cx => { this.page--; cx.notify(); })))
        .when(this.page < this.pages.length - 1, el => el.child(button("ocq-work-review-next", "Next retained page", cx => { this.page++; cx.notify(); }))));
      if (valid && !this.working && this.seen.size === this.pages.length) {
        if (!this.confirming) box.child(button("ocq-work-review-complete", "All pages seen · review confirmation", cx => { if (this.pinned()) this.confirming = true; cx.notify(); }));
        else box.child(div().text_color(theme.blocked).child(`Confirm ${this.preview.status.request_id} exactly once?\nPreview SHA-256: ${this.preview.preview_sha256}\nAn active dispatcher may execute an admitted job. A timeout is UNKNOWN, never permission to retry.`))
          .child(button("ocq-work-confirm-back", "Back without confirming", cx => { this.confirming = false; cx.notify(); }))
          .child(button("ocq-work-confirm-once", "Confirm exact pointer once", cx => this.confirm(cx)));
      }
    }
    if (this.output) {
      const out = this.output;
      box.child(div().font_semibold().child(out.title));
      box.child(div().child(`Retained output page ${out.page + 1}/${out.pages.length}`));
      box.child(div().child(out.pages[out.page]));
      box.child(h_flex().gap(4)
        .when(out.page > 0, el => el.child(button("ocq-work-output-previous", "Previous output page", cx => { out.page--; cx.notify(); })))
        .when(out.page < out.pages.length - 1, el => el.child(button("ocq-work-output-next", "Next output page", cx => { out.page++; cx.notify(); }))));
    }
    return box;
  }
}
