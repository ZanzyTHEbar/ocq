import { div } from "gpui-kit";
import { v_flex, h_flex, ScrollHandle } from "gpui-base";
import { Buffer } from "buffer";
import { createHash } from "crypto";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { decimal } from "./stored-updates.js";
import { technicalDetails } from "./action-review.js";

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
    this.compatibility = host.compatibility || compatibility;
    this.command = (args, options) => bridgeCommand(args, { ...options, compatibility: this.compatibility });
    this.scroll = ScrollHandle.new();
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
    return { project: h.selected, machine, space, folder: h.folder, queue: observation.queue_identity, ...(h.sourceKey ? { source: h.sourceKey } : {}),
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
  attemptKey(scope, item) { return JSON.stringify([scope.source ?? scope.machine, scope.space, scope.queue, scope.project, item.request_id, item.intent_sha256]); }
  attempted() {
    const marker = this.selected && this.bound && this.attempts[this.attemptKey(this.bound, this.selected)];
    return marker && marker.state !== "not_invoked";
  }
  choose(item, cx) {
    this.suspend(); this.selectedID = item.request_id; this.selected = item; this.page = 0; this.output = null;
    this.requestDetails = false; this.revealSelected = true; cx.notify();
  }
  async call(action, scope, extra = []) {
    const args = [action, ...(action === "work-preview" ? ["--work-observation-context", scope.bridge] : []),
      "--", scope.space, scope.queue, scope.project, ...(action === "work-list" ? [] : [this.selectedID]), ...extra];
    const r = await this.command(args, { on: scope.machine });
    if (r.code !== 0 || Buffer.from(r.stdout || "").length > 1048576) throw Error("Work read unavailable");
    return JSON.parse(r.stdout);
  }
  async check(scope, generation) {
    return this.compatibility.check(() => this.current(scope) && this.generation === generation, true, true);
  }
  pinned() {
    const p = this.reviewScope;
    const binding = this.host.board?.project?.current_binding;
    return p && this.visible() && this.current(p.scope) && this.generation === p.generation && this.compatibility.workReady() &&
      this.compatibility.epoch === p.epoch && this.compatibility.serial === p.serial && pins(this.compatibility.status) === p.pins &&
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
        const serial = this.compatibility.serial, epoch = this.compatibility.epoch;
        const value = await this.call("work-preview", scope, this.contextArgs(scope, bridge));
        if (!this.visible() || !this.current(scope) || this.generation !== generation || serial !== this.compatibility.serial || epoch !== this.compatibility.epoch) return;
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
        const input = JSON.parse(value.input_json);
        this.summary = { input, profile: input.profile ? JSON.parse(input.profile) : null,
          report: value.status.kind === "review_result" ? JSON.parse(JSON.parse(value.payload).manifest) : null };
        this.preview = value; this.pages = this.pagesFor(value);
        this.reviewDetails = { pages: this.pages, page: 0 };
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
          this.output = { kind: "record", title: "Exact server record / receipt · read only", value };
        } else if (action === "work-output") {
          status(value.status, scope.project, request);
          if (value.protocol !== protocol || typeof value.text !== "string" || Buffer.from(value.text).length > 16384 ||
            !hex(value.sha256) || hash(value.text) !== value.sha256 || typeof value.provenance !== "string") throw Error("Invalid exact output");
          this.output = { kind: "answer", title: "Final assistant output", value };
        } else {
          if (value.protocol !== protocol || value.integration_authorized !== false || value.validation !== "not_run" ||
            !["ready_for_integrator_review", "not_ready"].includes(value.readiness) || value.review_result?.request_id !== request ||
            typeof value.implementation_manifest !== "string" || typeof value.review_manifest !== "string") throw Error("Invalid integration handoff");
          for (const part of ["implementation", "capture", "review", "review_result"]) status(value[part], scope.project);
          this.output = { kind: "handoff", title: "Integration handoff", value };
        }
        this.output.pages = this.pagesFor(this.output.value); this.output.page = 0;
        this.revealOutput = true;
      } catch (_) {
        if (this.current(scope) && this.generation === generation) this.error = "Exact stored record/output unavailable. Missing evidence never permits resend, recapture, replacement, or integration.";
      } finally { if (this.current(scope) && this.generation === generation) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  confirm(cx) {
    const preview = this.preview, scope = this.bound;
    if (this.working || !preview || !this.confirming || !this.pinned() || this.attempted() || this.storageError) return;
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
        const r = await this.command(["work-confirm", scope.space, scope.queue, scope.project, preview.status.request_id,
          ...this.contextArgs(scope, this.compatibility.status), preview.preview_sha256],
          { on: scope.machine, current: () => this.pinned() && this.preview === preview });
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
    const items = [], add = element => items.push(element);
    // Native ScrollHandle indices name direct children, not DOM descendants.
    // Keep each task/action/output block a direct child of this viewport.
    const atItem = build => {
      const previous = this.host.actionReveal, index = items.length;
      this.host.actionReveal = () => this.scroll.scroll_to_item(index);
      try { return build(); } finally { this.host.actionReveal = previous; }
    };
    const control = (id, text, action, label = text) => atItem(() => button(id, text, action, label));
    const disclosure = (id, text, action) => atItem(() => this.host.panelControl(id, text, action, theme, this.host.renderTargets));
    const box = v_flex().id("ocq-work-operator").flex_1().min_h(0).overflow_y_scroll().track_scroll(this.scroll).gap(8).p(12);
    if (!this.bound) return box.child(div().text_color(theme.muted).child("Choose a project to see its tasks."));
    add(atItem(() => this.host.mainClient.render(cx, theme, control)));
    add(div().pt(10).font_semibold().child("Tasks"));
    if (!this.readAt) add(div().text_size(12).text_color(theme.muted).child("Reading prepared tasks…"));
    if (this.storageError) add(div().text_color(theme.blocked).child("Private client attempt evidence unavailable. Confirmations disabled."));
    if (this.error) add(div().text_color(theme.blocked).child(this.error));
    if (this.readAt && !this.rows.length) add(div().text_color(theme.muted).child("Tasks appear here when Main prepares work."));
    const kindLabel = { implementation: "Work", capture: "Snapshot", review: "Review", review_result: "Handoff" };
    const stateLabel = { preparing: "Preparing", prepared: "Needs review", armed: "Claimed", uncertain: "Unknown", recorded_observed: "Recorded", blocked: "Blocked" };
    for (const item of this.rows) {
      const label = `${kindLabel[item.kind]} · ${stateLabel[item.state]}`;
      add(control(`ocq-work-select-${item.request_id}`, label, cx => this.choose(item, cx), `${label} · ${item.request_id}`)
        .min_w(0).child(div().truncate().text_size(12).text_color(theme.muted).child(item.request_id))
        .when(item.request_id === this.selectedID, e => e.bg(theme.hover)));
      if (item.request_id === this.selectedID && this.selected)
        this.renderSelected(items, theme, control, disclosure);
    }
    return box.children(items);
  }
  renderSelected(items, theme, button, disclosure) {
    const add = element => items.push(element);
    const item = this.selected;
    add(div().font_semibold().child(item.request_id));
    if (item.state === "recorded_observed") add(div().text_size(12).text_color(theme.muted).child("Recorded. Execution and validation are separate."));
    if (item.job) {
      add(div().child(`${item.job.state} · ${item.job.outcome || "no terminal outcome"}`));
      add(div().text_size(12).text_color(theme.muted).child(`${item.job.terminal_proven ? "Terminal proof recorded" : "Completion not proven"} · Tests not run`));
    }
    if (this.attempted()) add(div().text_color(theme.blocked).child("A client confirmation attempt is retained. Use read-only exact recovery; no resend is offered, even if no server receipt exists."));
    if (this.revealSelected) { this.scroll.scroll_to_item(items.length); this.revealSelected = false; }
    if (!this.working) {
      add(h_flex().gap(4)
        .child(button("ocq-work-status", "Read status", cx => this.read("work-status", cx)))
        .child(button("ocq-work-recover", "Check receipt", cx => this.recover(cx))));
      if (item.job?.terminal_proven) add(button("ocq-work-output", "Read final output", cx => this.read("work-output", cx)));
      if (["implementation", "review"].includes(item.kind) && item.job?.session_id)
        add(button("ocq-client-worker-new", "Open worker / attention", cx => this.host.mainClient.openConversation("worker", item.request_id, cx)));
      if (item.kind === "review_result" && item.state === "recorded_observed") add(button("ocq-work-handoff", "View handoff", cx => this.read("work-handoff", cx)));
      if (item.state === "prepared" && !this.attempted() && !this.storageError && !this.preview)
        add(button("ocq-work-preview", "Review prepared task", cx => this.previewRequest(cx)));
    } else add(div().child("Reading/confirming exact pointer…"));
    if (this.output) this.renderOutput(items, theme, button, disclosure);
    add(disclosure("ocq-read-work-details", this.requestDetails ? "Hide request details" : "Request details",
      cx => { this.requestDetails = !this.requestDetails; cx.notify(); }));
    if (this.requestDetails) {
      add(div().text_size(12).text_color(theme.muted).child(`Request: ${item.request_id}\nState: ${item.state}\nBinding: ${item.binding_revision}\nIntent: ${item.intent_sha256}\nParent: ${item.parent_request_id || "none"}\nJournal: ${this.readAt || "not read"}\nFilesystem observation, separate from the SQLite snapshot.`));
      if (item.job) add(button("ocq-work-job", "Inspect job", cx => {
        this.suspend();
        this.host.workspaceView = "projects"; this.host.projectView = "work";
        if (this.host.detailJob !== item.job.job_id) this.host.inspect(item.job.job_id, cx);
        this.host.openTarget("job", item.job.job_id, cx);
      }));
      if (item.result) add(button("ocq-work-result", "View result", cx => { this.suspend(); this.host.openTarget("manifest", item.result.manifest_digest, cx); }));
    }
    if (this.preview) {
      const valid = this.pinned();
      if (!valid) add(div().text_color(theme.blocked).child("Preview authority suspended. Read status, then review afresh."));
      const { input, profile, report } = this.summary;
      const action = { implementation: "Start implementation", capture: "Capture snapshot", review: "Start independent review", review_result: "Record review result" }[item.kind];
      add(div().font_semibold().child(action));
      add(div().child(`Project: ${this.bound.project}\nMachine: ${this.bound.machine ?? "This computer"}\nRepository: ${this.bound.folder}\nRequest: ${item.request_id}${item.parent_request_id ? `\nBased on: ${item.parent_request_id}` : ""}`));
      if (input.brief) add(div().child(input.brief));
      if (input.criteria) add(div().child(`Review criteria\n${input.criteria}`));
      if (input.rules) add(div().child(`Review rules\n${input.rules}`));
      if (report) {
        add(div().child(`Verdict: ${report.verdict}`));
        for (const finding of report.findings || []) add(div().child(`${finding.severity} · ${finding.location}\n${finding.message}`));
        if (report.limitations) add(div().child(`Limitations: ${report.limitations}`));
      }
      add(div().text_size(12).child({
        implementation: "Run the supplied brief in an owned worktree. Snapshot, independent review, and handoff are separate steps.",
        capture: "Capture the completed implementation's exact checkout as a retained snapshot. This does not claim tests passed.",
        review: "Review the linked snapshot independently. This does not integrate the implementation.",
        review_result: "Record the linked review result for handoff. Integration remains a separate decision.",
      }[item.kind]));
      add(div().text_size(12).child("Permissions stay in OpenCode. An uncertain result must be checked, not resent."));
      if (profile) {
        add(button("ocq-work-review-roles", this.reviewDetails.roles ? "Hide Advanced" : "Advanced · agent", cx => {
          this.reviewDetails.roles = !this.reviewDetails.roles; cx.notify();
        }));
        if (this.reviewDetails.roles) add(div().text_size(12).child(`${profile.agent} · ${profile.provider_id}/${profile.model_id}${profile.variant ? ` · ${profile.variant}` : ""}`));
      }
      if (valid && !this.working) {
        add(button("ocq-work-confirm-once", action, cx => {
          if (this.pinned()) { this.confirming = true; this.confirm(cx); }
        }));
      }
      // Keep optional detail navigation on this exact preview object; it is
      // never an authorization flag or a substitute for the pin checks.
      add(technicalDetails(this.reviewDetails, button, "ocq-work-review"));
    }
  }
  renderOutput(items, theme, button, disclosure) {
    const add = element => items.push(element), out = this.output, value = out.value;
    add(div().font_semibold().child(`${out.title} · ${this.selectedID}`));
    if (out.kind === "answer") {
      add(div().text_size(12).text_color(theme.muted).child("Terminal-evidence answer · not test validation"));
      if (this.revealOutput) this.scroll.scroll_to_item(items.length);
      add(div().child(value.text));
    } else if (out.kind === "handoff") {
      add(div().text_color(theme.blocked).child(`${value.readiness} · integration NOT authorized`));
      add(div().text_size(12).text_color(theme.muted).child("Tests not run · linked historical evidence"));
      let report;
      try { report = JSON.parse(value.review_output?.text); } catch (_) {}
      if (!keys(report, ["verdict", "findings", "limitations"]) ||
          !["approved", "needs_revision", "unable_to_review"].includes(report.verdict) ||
          !Array.isArray(report.findings) || !report.findings.every(finding =>
            keys(finding, ["location", "severity", "message"]) &&
            ["location", "message"].every(key => typeof finding[key] === "string" && finding[key].trim() !== "") &&
            ["critical", "high", "medium", "low", "info"].includes(finding.severity)) ||
          typeof report.limitations !== "string" || report.limitations.trim() === "") report = null;
      if (report) {
        add(div().child(`Verdict: ${report.verdict}`));
        add(div().child(`Findings: ${report.findings.length ? report.findings.length : "none"}`));
        for (const finding of report.findings)
          add(div().child(`${finding.severity} · ${finding.location}\n${finding.message}`));
      } else add(div().text_color(theme.blocked).child("Review report unavailable. Inspect Exact evidence."));
      add(button("ocq-work-keep-separate", "Keep separate", cx => {
        out.keptSeparate = true; cx.notify();
      }));
      if (out.keptSeparate) add(div().text_color(theme.muted).child("Work remains in its owned checkout. No integration was requested or authorized."));
      if (report) add(div().child(`Limitations: ${report.limitations}`));
      for (const kind of ["implementation", "capture", "review", "review_result"]) {
        const linked = value[kind];
        add(div().child(`${kind}: ${linked.request_id}\n${linked.job?.job_id || linked.result?.association_id || linked.state}`));
      }
      if (this.revealOutput) this.scroll.scroll_to_item(items.length - 5);
    } else {
      if (this.revealOutput) this.scroll.scroll_to_item(items.length);
      add(div().child(value.receipt ? `Admission recorded · ${value.receipt.job_id}` : `Stored state: ${value.state} · no admission receipt`));
    }
    this.revealOutput = false;
    add(disclosure("ocq-read-work-exact-evidence", out.exact ? "Hide exact evidence" : "Exact evidence",
      cx => { out.exact = !out.exact; cx.notify(); }));
    if (out.exact) {
      add(div().text_size(12).child(`Exact evidence page ${out.page + 1}/${out.pages.length}`));
      add(div().child(out.pages[out.page]));
      add(h_flex().gap(4)
        .when(out.page > 0, el => el.child(button("ocq-work-output-previous", "Previous output page", cx => { out.page--; cx.notify(); })))
        .when(out.page < out.pages.length - 1, el => el.child(button("ocq-work-output-next", "Next output page", cx => { out.page++; cx.notify(); }))));
    }
  }
}
