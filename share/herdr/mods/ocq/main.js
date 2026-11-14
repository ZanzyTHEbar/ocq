import { View, div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { nextCommand, spaces, theme } from "herdr";
import { exec, compatibility, enabledButton } from "./compatibility.js";
import { connect } from "net";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { QueueInspection } from "./queue.js";
import { WorktreeVerification } from "./worktree-check.js";
import { ResultPublication } from "./result-publication.js";
import { RegistrationForm } from "./registration.js";
import { SubmissionForm } from "./submission.js";
import { ComposeForm } from "./compose-form.js";
import { composeProof, composeEqual, composeDecode, socketFrames } from "./private-frames.js";
import { StoredUpdates, acceptObservationVersion } from "./stored-updates.js";

function emptyCollection() {
  return { pages: [{ start: "0" }], items: [], history: [], recent: [], readTimes: {}, cursor: "0", readAt: null, loaded: false, loading: false, hasMore: false, error: null };
}

function readOrder(value) {
  return (value || "").replace(/(?:\.(\d+))?Z$/, (_match, fraction) => `.${(fraction || "").padEnd(9, "0")}Z`);
}

// These are the core observer's exact stored reasons, not a live request list.
function attention(job) {
  if (job?.state !== "unknown") return null;
  if (job.outcome === "blocked" && [
    "blocked: pending permission/form; never auto-answer; capacity held",
    "blocked: permission/form appeared during terminal inspection; capacity held",
  ].includes(job.reason)) return "Last recorded: permission or form request (type unknown)";
  if (job.outcome === "blocked" && [
    "session input pending in inbox; no completion, resend, or resume",
    "session input arrived across terminal inspection; wait for inbox to drain; capacity held",
  ].includes(job.reason)) return "Last recorded: session inbox input (not necessarily a human request)";
  return "Unknown job state; request status not established";
}

function attentionDetail(job) {
  if (!attention(job)) return null;
  return {
    status: attention(job),
    observation_time: "not recorded; current request status unknown",
    session_id: job.agent?.session_id || "not recorded",
    location_worktree: job.agent?.worktree || "not recorded",
    guidance: job.agent?.session_id && job.agent?.worktree
      ? "Find the exact recorded session and location in OpenCode and check its current state there. An ID alone does not establish session availability. OCQ does not answer requests or observe the runtime from this view."
      : "Session ID or location is not recorded. Investigate ownership before attempting to locate the session. OCQ does not answer requests or observe the runtime from this view.",
  };
}

// The bridge has already run the core's duplicate-key and schema validation on
// these retained bytes. The panel supports only these exact spellings and shapes;
// unsupported wrappers, aliases, roles or unsafe numbers are never summarized.
function intakeObject(value, allowed) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("unsupported object");
  for (const key of Object.keys(value)) if (!allowed.includes(key)) throw new Error("unsupported field");
  return value;
}

function intakeEmbedded(value) {
  if (typeof value === "string") value = JSON.parse(value);
  if (!value || typeof value !== "object" || Array.isArray(value) || Object.prototype.hasOwnProperty.call(value, "canonical")) throw new Error("unsupported embedded representation");
  return value;
}

function visibleString(value) {
  const invisible = /[\p{Cc}\p{Default_Ignorable_Code_Point}\p{Zl}\p{Zp}]/u;
  const s = JSON.stringify(value);
  return Array.from(s, (c) => invisible.test(c) ? Array.from({ length: c.length }, (_, n) => `\\u${c.charCodeAt(n).toString(16).padStart(4, "0")}`).join("") : c).join("");
}

function intakeStructured(kind, raw, exact = null) {
  const parsed = JSON.parse(raw), sections = [];
  const walk = (label, value) => {
    if (typeof value === "string") {
      if (composeDecode(Buffer.from(value, "utf8")) !== value) throw new Error("unsupported Unicode");
      sections.push({ title: label, text: visibleString(value) });
    } else if (value === null || typeof value === "boolean") sections.push({ title: label, text: String(value) });
    else if (typeof value === "number") {
      if (!Number.isSafeInteger(value) || Object.is(value, -0)) throw new Error("unsupported number");
      sections.push({ title: label, text: String(value) });
    } else if (Array.isArray(value)) {
      if (!value.length) sections.push({ title: label, text: "[]" });
      else value.forEach((item, n) => walk(`${label} [${n + 1}]`, item));
    } else if (value && typeof value === "object") {
      const keys = Object.keys(value);
      if (!keys.length) sections.push({ title: label, text: "{}" });
       else for (const key of keys) {
         if (composeDecode(Buffer.from(key, "utf8")) !== key) throw new Error("unsupported Unicode key");
         walk(`${label} · ${visibleString(key)}`, value[key]);
       }
    } else throw new Error("unsupported value");
  };
  const fieldList = (label, object, skip = []) => {
    for (const key of Object.keys(object)) if (!skip.includes(key)) walk(`${label} · ${key}`, object[key]);
  };
  const model = (value) => intakeObject(value, ["id", "providerID", "variant"]);
  const permissions = (value) => {
    if (!Array.isArray(value)) throw new Error("unsupported permissions");
    for (const rule of value) intakeObject(rule, ["action", "resource", "effect"]);
  };
  const manifest = (value) => {
    const common = ["version", "kind", "project_id", "source", "observed_at"];
    const fields = {
      handoff: ["intent", "decisions", "risks", "outstanding_job_ids", "outstanding_event_ids", "outstanding_manifest_digests", "notes"],
      brief: ["goal", "scope", "exclusions", "acceptance_criteria", "base", "agent", "model", "permissions", "allowed_actions", "context_references"],
      result: ["claim_label", "job_id", "request_digest", "base", "worktree", "staged_status", "unstaged_status", "untracked_paths", "before_status_digest", "after_status_digest", "before_content_digest", "after_content_digest", "ignored_exclusions", "changed_files", "diff", "inline_output", "evidence_digests", "claimed_validation"],
      review_brief: ["target_job_id", "target_result_digest", "base", "criteria", "rules", "diff", "changed_files"],
      review_result: ["claim_label", "review_job_id", "review_session_id", "target_job_id", "reviewed_result_digest", "verdict", "findings", "limitations"],
    };
    const m = intakeEmbedded(value);
    if (!fields[m.kind]) throw new Error("unsupported manifest kind");
    intakeObject(m, [...common, ...fields[m.kind]]);
    if (m.kind === "brief") { model(m.model); permissions(m.permissions); }
    if (m.changed_files !== undefined) { if (!Array.isArray(m.changed_files)) throw new Error("unsupported changed files"); for (const f of m.changed_files) intakeObject(f, ["path", "old_mode", "new_mode", "contents"]); }
    if (m.kind === "result") intakeObject(m.claimed_validation, ["status", "commands", "results", "provenance", "limitations"]);
    if (m.kind === "review_result") { if (!Array.isArray(m.findings)) throw new Error("unsupported findings"); for (const f of m.findings) intakeObject(f, ["location", "severity", "message"]); }
    return m;
  };
  const bytes = (label, part) => {
    if (!part || typeof part.raw !== "string" || createHash("sha256").update(part.raw).digest("hex") !== part.sha256) throw new Error("exact embedded bytes unavailable");
    walk(`${label} · SHA-256 of exact bytes`, part.sha256);
    walk(`${label} · exact embedded JSON bytes`, part.raw);
  };
  if (exact && (exact.raw !== raw || createHash("sha256").update(raw).digest("hex") !== exact.sha256)) throw new Error("retained envelope changed");
  if (kind === "registration") {
    const request = intakeObject(parsed, ["version", "key", "project_id", "label", "expected_revision", "repo", "commit_anchor", "elephant_session_id", "expected_opencode_project_id", "expected_opencode_directory", "auth_ref", "handoff_manifest_digest", "handoff_manifest"]);
    const handoff = intakeObject(intakeEmbedded(request.handoff_manifest), ["version", "kind", "project_id", "source", "observed_at", "intent", "decisions", "risks", "outstanding_job_ids", "outstanding_event_ids", "outstanding_manifest_digests", "notes"]);
    if (handoff.kind !== "handoff") throw new Error("unsupported manifest");
    fieldList("Registration and binding", request, ["handoff_manifest"]);
    fieldList("Handoff", handoff);
    if (exact?.handoff) bytes("Handoff", exact.handoff);
  } else if (kind === "submission") {
    const request = intakeObject(parsed, ["version", "key", "project_id", "binding_revision", "role", "lane_id", "fsfork_driver", "request", "brief_digest", "brief_manifest_digest", "brief_manifest", "target_job_id", "target_result_digest", "referenced_manifests"]);
    if (!["implementation", "review"].includes(request.role) || request.referenced_manifests !== undefined && !Array.isArray(request.referenced_manifests)) throw new Error("unsupported submission representation");
    const agent = intakeObject(intakeEmbedded(request.request), ["version", "kind", "repo", "base", "prompt", "agent", "model", "permissions", "provision_policy", "memory_bytes"]);
    model(agent.model); permissions(agent.permissions);
    const brief = manifest(request.brief_manifest);
    if (brief.kind !== (request.role === "review" ? "review_brief" : "brief") || brief.project_id !== request.project_id || brief.base !== agent.base) throw new Error("brief context mismatch");
    if (request.role === "review" && (brief.target_job_id !== request.target_job_id || brief.target_result_digest !== request.target_result_digest)) throw new Error("review target mismatch");
    const refs = request.referenced_manifests || [];
    if (exact && (!exact.request || !exact.brief || (exact.references || []).length !== refs.length)) throw new Error("embedded review incomplete");
    fieldList("Submission and binding", request, ["request", "brief_manifest", "referenced_manifests"]);
    if (!request.lane_id) walk("Submission and binding · effective lane_id (derived from key)", `lane-${createHash("sha256").update(request.key).digest("hex").slice(0, 16)}`);
    if (!request.fsfork_driver) walk("Submission and binding · effective fsfork_driver (default)", "git_worktree");
    fieldList("Agent request", agent);
    if (exact) bytes("Agent request", exact.request);
    fieldList(brief.kind === "review_brief" ? "Review brief (core deterministic prompt inputs)" : "Brief", brief);
    if (exact) bytes("Brief", exact.brief);
    walk("Referenced manifests · count", refs.length);
    refs.forEach((value, n) => {
      const ref = manifest(value), label = `Referenced manifest [${n + 1}]`;
      if (ref.project_id !== request.project_id) throw new Error("reference context mismatch");
      fieldList(label, ref);
      if (exact) bytes(label, exact.references[n]);
    });
  } else throw new Error("unsupported kind");
  return sections;
}

// The bridge is a short-lived Go process. OCQ's database remains authoritative;
// no Herdr status, pane state, or mod state is interpreted as job completion.
export default class OCQ extends View {
  init(_props, cx) {
    this.queue = new QueueInspection();
    this.updates = new StoredUpdates(this, cx);
    this.projectPages = [{ start: "0" }];
    this.lanePages = [{ start: "0" }];
    this.boardToken = {};
    this.worktreeCheck = new WorktreeVerification();
    this.worktreeCheck.reset();
    this.publication = new ResultPublication();
    this.queueMode = false;
    this.workspaceView = "overview";
    this.projectView = "work";
    this.contextDetails = false;
    this.diagnostics = false;
    this.updateHealth = { state: "stored", reason: null };
    this.projects = [];
    this.projectsReadAt = null;
    this.selected = null;
    this.board = null;
    this.error = null;
    this.loading = false;
    this.generation = 0;
    this.detailGeneration = 0;
    this.detail = null;
    this.detailJob = null;
    this.detailError = null;
    this.detailLoading = false;
    this.manifestGeneration = 0;
    this.manifestDigest = null;
    this.manifest = null;
    this.manifestError = null;
    this.manifestLoading = false;
    this.expandedMessage = null;
    this.collections = { inbox: emptyCollection(), results: emptyCollection() };
    this.mailActions = {};
    this.mailConfirmation = null;
    this.compose = { screen: false, state: "loading", id: null, preview: null, working: false, error: null };
    this.intake = { screen: false, state: "loading", id: null, kind: "registration", preview: null, page: 0, working: false, error: null, context: null, confirm: false, uncertainID: null, observation: null };
    this.intakeInput = TextareaState.new({ placeholder: "Paste an exact registration or submission JSON envelope", rows: 8 });
    this.intakeInputFocused = false;
    this.intakeInput.on("change", (_e, cx) => cx.notify());
    this.intakeInput.on("focus", (_e, cx) => { this.intakeInputFocused = true; cx.notify(); });
    this.intakeInput.on("blur", (_e, cx) => { this.intakeInputFocused = false; cx.notify(); });
    this.intakeInput.on("submit", () => this.handle.focus());
    this.spaceID = spaces().find((s) => s.active)?.id ?? null;
    this.machine = spaces().find((s) => s.active)?.machine ?? null;
    this.folder = spaces().find((s) => s.active)?.folder ?? null;
    this.handle = cx.focus_handle();
    this.keyboardTarget = { kind: "refresh" };
    this.keyboardMode = false;
    this.keyboardPage = 0;
    cx.spawn(async () => {
      for (;;) {
        if (await nextCommand() === "focus") this.handle.focus();
      }
    });
    this.updates.sync(cx);
  }

  close() { this.closed = true; this.generation++; this.queue.generation++; this.publication.generation++; this.worktreeCheck.generation++; this.updates.dispose(); }

  observationSelectors() {
    const descriptors = [], scope = { space: this.spaceID, machine: this.machine, folder: this.folder, generation: this.generation, project: this.selected };
    const add = (selector, target, apply, dirty = false) => descriptors.push({ selector, target, apply, dirty });
    this.projectPages.forEach((page, index) => add({ slot: `projects-${index}`, kind: "projects", cursor: page.start }, page, (slot, meta, readAt) => {
      if (!this.projectPages.includes(page) || !acceptObservationVersion(page.observationVersion, meta)) return false;
      page.observationVersion = meta; page.items = slot.status === "ready" ? slot.data : []; page.readAt = readAt;
      page.error = slot.status === "ready" ? null : `Project page ${page.start}: ${slot.status}`;
      page.next = slot.cursor || page.start; page.hasMore = slot.has_more;
      this.projects = this.projectPages.flatMap(item => item.items || []);
      this.projectsHasMore = this.projectPages[this.projectPages.length - 1].hasMore !== false;
      this.projectsReadAt = readAt; this.error = this.projectPages.map(item => item.error).filter(Boolean).join("\n") || null;
      if (!this.selected && !this.projectsHasMore) {
        const matching = this.projects.filter(item => item.repo_root === this.folder);
        const selected = matching.length === 1 ? matching[0].project_id : this.projects.length === 1 ? this.projects[0].project_id : null;
        if (selected) { this.selected = selected; this.boardToken = {}; this.projectDetailToken = {}; this.manifestToken = {}; }
      }
      return true;
    }));
    if (this.queueMode) return descriptors.concat(this.queue.observationSelectors(this));
    if (!this.selected) return descriptors;
    const boardToken = this.boardToken;
    add({ slot: "project-board", kind: "board", project_id: this.selected }, boardToken, (slot, meta, _readAt, cx) => {
      if (this.boardToken !== boardToken || scope.project !== this.selected) return false;
      if (slot.status !== "ready") { this.board = null; this.boardError = `Selected project: ${slot.status}${slot.status === "limit_exceeded" ? " (legacy project dimensions exceed the bounded view; use paged Lanes or Queue)" : ""}`; boardToken.observationVersion = meta; return true; }
      this.boardError = null;
      return this.applyProjectObservation(scope, slot.data, cx, meta);
    });
    if (this.workspaceView === "projects" && this.projectView === "lanes") this.lanePages.forEach((page, index) => add({ slot: `lanes-${index}`, kind: "lanes", project_id: this.selected, cursor: page.start }, page, (slot, meta, readAt) => {
      if (!this.lanePages.includes(page) || scope.project !== this.selected || !acceptObservationVersion(page.observationVersion, meta)) return false;
      page.observationVersion = meta; page.items = slot.status === "ready" ? slot.data : []; page.error = slot.status === "ready" ? null : slot.status;
      page.next = slot.cursor || page.start; page.hasMore = slot.has_more; page.readAt = readAt;
      this.lanes = this.lanePages.flatMap(item => item.items || []); return true;
    }));
    for (const kind of ["inbox", "results"]) {
      const visible = kind === "inbox" ? this.workspaceView === "inbox" : this.workspaceView === "projects" && this.projectView === "results";
      if (!visible) continue;
      const collection = this.collections[kind];
      collection.pages.forEach((page, index) => add({ slot: `${kind}-${index}`, kind, project_id: this.selected, cursor: page.start }, page, (slot, meta, readAt) => {
        if (this.collections[kind] !== collection || !collection.pages.includes(page) || !acceptObservationVersion(page.observationVersion, meta)) return false;
        page.observationVersion = meta; page.items = slot.status === "ready" ? (kind === "inbox" ? slot.data.messages : slot.data.items) : [];
        page.error = slot.status === "ready" ? null : `${kind} page ${page.start}: ${slot.status}`;
        page.next = slot.cursor || page.start; page.hasMore = slot.has_more; page.readAt = readAt;
        collection.history = collection.pages.flatMap(item => item.items || []); collection.recent = [];
        this.combineCollection(kind, collection);
        for (const item of page.items) collection.readTimes[this.itemID(kind, item)] = readAt;
        const last = collection.pages[collection.pages.length - 1]; collection.cursor = last.next || last.start; collection.hasMore = last.hasMore;
        collection.readAt = readAt; collection.loaded = true; collection.loading = false;
        collection.error = collection.pages.map(item => item.error).filter(Boolean).join("\n") || null; return true;
      }));
    }
    if (this.detailJob && ((this.workspaceView === "projects" && this.projectView === "work") || this.keyboardMode)) {
      const token = this.projectDetailToken, job = this.detailJob;
      add({ slot: "project-job", kind: "job", project_id: this.selected, job_id: job }, token, (slot, meta) => {
        if (this.projectDetailToken !== token || this.detailJob !== job || scope.project !== this.selected || !acceptObservationVersion(token.observationVersion, meta)) return false;
        token.observationVersion = meta; this.detail = slot.status === "ready" ? slot.data : null;
        this.detailError = slot.status === "ready" ? null : `Selected job: ${slot.status}`; this.detailLoading = false; return true;
      });
    }
    if (this.keyboardMode && ["manifest", "manifest-text"].includes(this.keyboardTarget.kind) && this.manifestDigest) {
      const token = this.manifestToken, digest = this.manifestDigest;
      add({ slot: "manifest", kind: "manifest", project_id: this.selected, digest }, token, (slot, meta) => {
        if (this.manifestToken !== token || this.manifestDigest !== digest || !acceptObservationVersion(token.observationVersion, meta)) return false;
        token.observationVersion = meta; this.manifest = slot.status === "ready" ? slot.data : null;
        this.manifestError = slot.status === "ready" ? null : `Manifest: ${slot.status}`; this.manifestLoading = false; return true;
      });
    }
    const mail = this.mailObservation;
    if (this.keyboardMode && mail && this.keyboardTarget.id === mail.id) add({ slot: "selected-mail", kind: "mail", project_id: this.selected, message_id: mail.id, sequence: mail.sequence }, mail, (slot, meta, readAt) => {
      if (this.mailObservation !== mail || !acceptObservationVersion(mail.observationVersion, meta)) return false;
      mail.observationVersion = meta; mail.status = slot.status; mail.data = slot.status === "ready" ? slot.data : null; mail.readAt = readAt; return true;
    });
    return descriptors;
  }

  resetObservationVersions() {
    // New binding means new projection ownership, not just new version labels.
    // Keep private journals/reviews separate; never reuse their old authority.
    this.observationReset = "Stored observation context changed; prior authority suspended.";
    this.generation++; this.detailGeneration++; this.manifestGeneration++;
    this.retainedPrivate = this.retainedPrivate || [];
    if (!this.compose.authoritySuspended || !this.intake.authoritySuspended) this.retainedPrivate.push({ compose: this.compose, intake: this.intake, mailConfirmation: this.mailConfirmation, worktreeCheck: { ...this.worktreeCheck }, publication: { ...this.publication } });
    this.compose.authoritySuspended = true; this.intake.authoritySuspended = true;
    for (const state of Object.values(this.mailActions)) state.authoritySuspended = true;
    this.worktreeCheck.generation++; this.worktreeCheck.screen = false;
    this.publication.generation++; this.publication.screen = false;
    this.projectPages = [{ start: "0" }]; this.projects = []; this.projectsReadAt = null; this.projectsHasMore = false;
    this.board = null; this.boardVersion = null; this.boardError = null; this.boardToken = {};
    this.lanePages = [{ start: "0" }]; this.lanes = [];
    this.collections = { inbox: emptyCollection(), results: emptyCollection() };
    this.detail = null; this.detailJob = null; this.detailError = null; this.detailLoading = false; this.projectDetailToken = {};
    this.manifest = null; this.manifestDigest = null; this.manifestError = null; this.manifestLoading = false; this.manifestToken = {};
    this.mailObservation = null; this.expandedMessage = null;
    this.error = null; this.loading = false;
    this.keyboardTarget = { kind: "refresh" }; this.keyboardPage = 0;
    this.queue.resetObservationVersions();
  }

  clearInspection() {
    this.worktreeCheck.reset();
    this.publication.reset();
    this.detailGeneration++;
    this.detail = null;
    this.detailJob = null;
    this.detailError = null;
    this.detailLoading = false;
    this.manifestGeneration++;
    this.manifestDigest = null;
    this.manifest = null;
    this.manifestError = null;
    this.manifestLoading = false;
    this.expandedMessage = null;
    this.collections = { inbox: emptyCollection(), results: emptyCollection() };
    this.boardToken = {}; this.projectDetailToken = {}; this.manifestToken = {}; this.mailObservation = null;
    this.boardVersion = null; this.boardError = null; this.lanes = [];
    this.lanePages = [{ start: "0" }];
    this.mailConfirmation = null;
    this.compose = { ...this.compose, screen: false, preview: null, context: null, form: null, options: null, confirm: false, working: false };
    this.intake = { ...this.intake, screen: false, preview: null, context: null, confirm: false, working: false };
  }

  refresh(cx, notify = true) {
    this.updates.readAgain(cx);
    if (notify) cx.notify();
  }

  itemID(kind, item) { return kind === "inbox" ? item.id : item.event.id; }

  combineCollection(kind, collection) {
    const latest = new Set(collection.recent.map((item) => this.itemID(kind, item)));
    const items = collection.recent.slice().reverse().concat(collection.history.filter((item) => !latest.has(this.itemID(kind, item))));
    // Inspection identity remains stable, but missing/limited pages must not
    // reinsert old objects as current data. Exact mail has its own selector.
    collection.items = items;
    const retained = new Set(items.map((item) => this.itemID(kind, item)));
    for (const id of Object.keys(collection.readTimes)) if (!retained.has(id)) delete collection.readTimes[id];
  }

  loadRecent(kind, cx) {
    this.updates.readAgain(cx);
  }

  loadCollection(kind, reset, cx) {
    const collection = this.collections[kind];
    if (!reset && collection.hasMore && !collection.pages.some(page => page.start === collection.cursor)) collection.pages.push({ start: collection.cursor });
    this.updates.readAgain(cx);
    cx.notify();
  }

  select(projectID, cx) {
    this.generation++;
    this.loading = false;
    this.selected = projectID;
    this.board = null;
    this.keyboardTarget = { kind: "refresh" };
    this.keyboardPage = 0;
    this.clearInspection();
    this.refresh(cx);
  }

  composeLocal() {
    const active = spaces().find((s) => s.active);
    return Boolean(!this.closed && active && active.machine === null && this.machine === null && active.id === this.spaceID && active.folder === this.folder);
  }

  composeContext(lane) {
    if (!this.composeLocal() || !this.selected || !lane?.lane_id || !lane?.owner_job_id) return null;
    return Object.freeze({ space: this.spaceID, folder: this.folder, project: this.selected, lane: lane.lane_id, job: lane.owner_job_id });
  }

  composeContextCurrent(context) {
    return Boolean(context && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected &&
      (this.board?.project?.lanes ?? []).some((lane) => lane.lane_id === context.lane && lane.owner_job_id === context.job));
  }

  composeCurrent(c, context) {
    return !c.authoritySuspended && this.compose === c && c.screen && c.context === context && this.composeContextCurrent(context);
  }

  composeQuoted(value) { return visibleString(value); }

  async composeStoredContext(context, manifest = "", reply = "") {
    const result = await exec("ocq-herdr", ["compose-context", context.project, context.lane, manifest, reply], { on: null });
    if (result.code !== 0) throw new Error("stored compose links unavailable");
    const info = JSON.parse(result.stdout);
    if (info.project_id !== context.project || info.lane_id !== context.lane || info.job_id !== context.job || info.manifest !== manifest || info.reply_to !== reply || typeof info.lane_path !== "string" || !Array.isArray(info.integrator_senders) || !Array.isArray(info.recipient_lanes)) throw new Error("stored owner changed");
    return info;
  }

  composeRoute(request, info = null, recovered = true) {
    if (request.Kind === "lane.blocked") return [`lane:${request.LaneID}`, `job:${request.JobID}`].includes(request.From) && request.To === `integrator:${request.ProjectID}`;
    return request.Kind === "lane.assigned" && typeof request.From === "string" && request.From.startsWith("integrator:") && typeof request.To === "string" && request.To.startsWith("lane:") &&
      (!info || info.recipient_lanes.some((lane) => request.To === `lane:${lane}`)) && (recovered || info?.integrator_senders.includes(request.From));
  }

  composePreviewPages(request, info = null, raw = "") {
    const pages = [];
    const path = request.FsforkPath ?? "", implicit = path === "";
    for (const [label, value] of [
      ["Machine", "this computer"], ["Project", request.ProjectID], ["Lane", request.LaneID], ["Job", request.JobID],
      ["From", request.From], ["To", request.To], ["Kind", request.Kind],
      ["Identity meaning", "Stored routing metadata only; not OS identity, authentication, permission or execution approval."],
      [`Manifest (${request.Manifest === undefined ? "omitted → none" : request.Manifest === "" ? "explicit empty → none" : "retained attachment"})`, request.Manifest ?? ""],
      [`Reply to (${request.ReplyTo === undefined ? "omitted → no reply" : request.ReplyTo === "" ? "explicit empty → no reply" : "retained same-project message"})`, request.ReplyTo ?? ""],
      [`Requested FsforkPath (${request.FsforkPath === undefined ? "omitted → empty/default" : implicit ? "explicit empty → lane default" : "nonempty literal override"})`, path],
      ["Reviewed stored lane default (empty remains empty)", info ? info.lane_path : "UNAVAILABLE — cannot confirm"],
      ["Effective path to recheck/pin at claim", implicit ? info ? info.lane_path : "UNAVAILABLE — cannot confirm" : path],
      ["Path meaning", implicit ? "Use the stored lane default, even if that default is empty. A changed default requires a new review." : "Exact literal metadata override, even if its text says lane default. No path is opened or created."],
      ["Retained link lookup", info ? "Manifest/reply IDs checked in this project's stored SQLite snapshot; send rechecks in its transaction." : "Unavailable; raw fields may be read/cancelled, never confirmed."],
      ["Subject", request.Subject], [request.Body === undefined ? "Message (omitted → empty)" : "Message", request.Body ?? ""],
      ["Exact retained request SHA-256", createHash("sha256").update(raw).digest("hex")],
    ]) {
      const tokens = visibleString(value).match(/\\(?:u[0-9a-fA-F]{4}|["\\/bfnrt])|[\s\S]/gu) || [];
      for (let start = 0; start < tokens.length;) {
        let end = start, length = 0;
        while (end < tokens.length && length + tokens[end].length <= 160) length += tokens[end++].length;
        pages.push(`${label}${start ? " (continued)" : ""}:\n${tokens.slice(start, end).join("")}`);
        start = end;
      }
    }
    return pages;
  }

  advanceComposePage(delta, cx) {
    const c = this.compose, p = c.preview;
    if (!p || c.working || c.confirm || !this.composeCurrent(c, c.context)) return;
    const next = Math.max(0, Math.min(p.pages.length - 1, c.page + delta));
    if (delta === 1 && c.page === p.openedThrough && next === c.page + 1) p.openedThrough = next;
    c.page = next; cx.notify();
  }

  canReviewCompose() {
    const c = this.compose, p = c.preview;
    return Boolean(!c.working && c.state === "prepared" && p?.state === "prepared" && p.id === c.id && p.info && this.composeCurrent(c, c.context) && c.page === p.pages.length - 1 && p.openedThrough === p.pages.length - 1);
  }

  refreshCompose(cx) {
    if (this.queueMode) return;
    const c = this.compose;
    if (!this.composeLocal() || c.working) return;
    const scope = { space: this.spaceID, folder: this.folder, project: this.selected, screen: c.screen };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen === scope.screen && this.composeLocal() && scope.space === this.spaceID && scope.folder === this.folder && scope.project === this.selected;
    c.working = true;
    cx.spawn(async (cx) => {
      try {
        const result = await exec("ocq-herdr", ["compose-status"], { on: null });
        if (result.code !== 0 || !current()) throw new Error("status unavailable");
        const status = JSON.parse(result.stdout);
        if (!["idle", "busy", "prepared", "armed", "dispatch_intent", "completed", "cancelled", "blocked"].includes(status.state)) throw new Error("invalid status");
        if (c.state === "uncertain") {
          if (status.id === c.id && ["prepared", "armed"].includes(status.state)) {
            if (c.recoveryState !== status.state) c.preview = null;
            c.recoveryState = status.state;
            c.error = "The exact intent is unattempted now. A delayed claim or send may still race; preview and explicitly cancel under the journal lock. Do not resend.";
            return;
          }
          c.recoveryState = null; c.preview = null;
          if (status.id !== c.id || !["dispatch_intent", "completed", "cancelled"].includes(status.state)) {
            c.error = "Send outcome remains uncertain. A busy, unavailable or different journal does not authorize a retry.";
            return;
          }
        }
        if (c.id !== (status.id ?? null) || c.state !== status.state) c.observation = null;
        c.state = status.state; c.id = status.id ?? null; c.observation = status.observation ?? null; c.recoveryState = null;
        if (c.preview && (!["prepared", "armed"].includes(status.state) || c.preview.id !== status.id || c.preview.state !== status.state)) c.preview = null;
        c.error = null;
        if (c.state === "idle" && this.composeCurrent(c, c.context)) {
          const context = c.context;
          const info = await this.composeStoredContext(context);
          if (!current() || !this.composeCurrent(c, context)) throw new Error("context changed");
          c.options = info;
        }
      } catch (_e) { if (current()) { if (c.state !== "uncertain") c.state = "blocked"; c.preview = null; c.error = "Compose status/options unavailable. Do not send; check the local store."; } }
      finally { if (current()) { c.working = false; cx.notify(); } }
    });
  }

  openCompose(lane, cx) {
    const previous = this.compose;
    if (previous.working && previous.screen && this.composeContextCurrent(previous.context)) return;
    const c = this.compose = { ...previous, authoritySuspended: false, working: false, preview: null, form: null, options: null, context: null };
    c.screen = true; c.confirm = false; c.error = null;
    if (lane && ["idle", "loading"].includes(c.state)) {
      c.context = this.composeContext(lane);
      if (c.context) c.form = new ComposeForm(this, c.context);
    }
    this.handle.focus();
    this.keyboardMode = false;
    this.refreshCompose(cx);
    cx.notify();
  }

  composeMode(kind, cx) {
    const c = this.compose;
    if (c.working || c.state !== "idle" || !this.composeCurrent(c, c.context) || !["lane.blocked", "lane.assigned"].includes(kind)) return;
    const context = Object.freeze({ ...c.context });
    this.compose = { ...c, context, form: new ComposeForm(this, context, kind), preview: null, confirm: false, error: null };
    this.handle.focus(); cx.notify();
  }

  closeCompose(cx) {
    // Keep recovery identity/state, not the form, confirmation or callback scope.
    this.compose = { ...this.compose, screen: false, working: false, context: null, form: null, options: null, preview: null, confirm: false };
    cx.notify();
  }

  async composeExchange(command, id, payload, current, cx) {
    current = compatibility.transfer(current);
    if (!current()) throw new Error("context changed");
    const launch = randomBytes(32).toString("hex");
    const args = command === "compose-preview" ? [command, id, launch] : [command, launch];
    const worker = exec("ocq-herdr", args, { on: null, timeout: 25000, current }).then((value) => value, () => null);
    let socket = null;
    try {
      let rendezvous = null;
      for (let i = 0; i < 100; i++) {
        if (!current()) throw new Error("context changed");
        const result = await exec("ocq-herdr", ["compose-rendezvous", launch], { on: null });
        if (result.code === 0) { rendezvous = JSON.parse(result.stdout); break; }
        await cx.sleep(50);
      }
      if (!rendezvous || rendezvous.launch !== launch || !/^[0-9a-f]{64}$/.test(rendezvous.capability) || !Number.isInteger(rendezvous.port) || rendezvous.port < 1 || rendezvous.port > 65535) throw new Error("rendezvous unavailable");
      socket = await connect("127.0.0.1", rendezvous.port);
      const frames = socketFrames(socket), line = () => frames.line(), nonce = randomBytes(32).toString("hex");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(nonce + "\n");
      if (!composeEqual(await line(), composeProof(rendezvous.capability, nonce, "server"))) throw new Error("server authentication failed");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(composeProof(rendezvous.capability, nonce, "client") + "\n");
      if (await line() !== "READY" || !current()) throw new Error("context or client authentication failed");
      let response;
      if (command === "compose-receive") {
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(payload + "\n");
        response = await line();
        if (!/^PREPARED [0-9a-f]{64}$/.test(response)) throw new Error("preparation uncertain");
      } else response = await line();
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !current()) throw new Error("compose exchange uncertain");
      if (command === "compose-preview") {
        const status = JSON.parse(done.stdout);
        if (status.id !== id || !["prepared", "armed"].includes(status.state)) throw new Error("preview status mismatch");
        return { response, status };
      }
      return response;
    } finally { if (socket) socket.close(); await worker; }
  }

  prepareCompose(cx) {
    const c = this.compose, context = c.context, form = c.form;
    if (c.working || c.state !== "idle" || !form || !c.options || !this.composeCurrent(c, context)) return;
    const snapshot = form.snapshot(), current = () => this.composeCurrent(c, context);
    let request, payload;
    try {
      request = form.request(context, c.options);
      for (const value of Object.values(request)) if (typeof value !== "string" || composeDecode(Buffer.from(value, "utf8")) !== value) throw new Error("invalid text");
      if (!request.Subject || Buffer.from(request.Subject).length > 512 || Buffer.from(request.Body).length > 16384 || request.Subject.includes("\0") || request.Body.includes("\0")) throw new Error("invalid subject/body");
      payload = JSON.stringify(request);
      if (Buffer.from(payload).length + 1 > 131072) throw new Error("message frame too large");
    } catch (_e) { c.error = "Select a legal stored route and valid retained link IDs; enter subject/body within 512/16384 UTF-8 bytes. A literal path override must be nonempty. Nothing was prepared."; cx.notify(); return; }
    c.working = true; c.error = null; c.observation = null;
    cx.spawn(async (cx) => {
      let launched = false;
      try {
        const options = await this.composeStoredContext(context, request.Manifest, request.ReplyTo);
        if (!current() || c.form !== form || form.snapshot() !== snapshot || !this.composeRoute(request, options, false)) throw new Error("form/route changed");
        launched = true;
        const reply = await this.composeExchange("compose-receive", null, payload, current, cx);
        if (current()) { c.id = reply.slice(9); c.state = "prepared"; c.preview = null; }
      } catch (_e) { if (current()) { if (launched) c.state = "blocked"; c.error = launched ? "Preparation outcome unknown. Refresh journal status before doing anything else." : "Stored route, attachment or reply unavailable/changed. Nothing was prepared."; } }
      finally { if (current()) { c.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  previewCompose(cx) {
    const c = this.compose, id = c.id, initialState = c.state, initialRecovery = c.recoveryState;
    if (c.working || !(["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))) || !id || !this.composeLocal() || !this.selected) return;
    // The selected project must be explicit; the returned lane/job is checked
    // against the current stored board before it can be confirmed.
    const context = { space: this.spaceID, folder: this.folder, project: this.selected };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen && c.id === id && c.state === initialState && c.recoveryState === initialRecovery && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected;
    c.working = true; c.error = null; c.preview = null; c.confirm = false;
    cx.spawn(async (cx) => {
      try {
        const { response, status } = await this.composeExchange("compose-preview", id, null, current, cx);
        if (!current() || ((initialState === "armed" || initialRecovery === "armed") && status.state !== "armed")) throw new Error("preview state changed");
        const request = JSON.parse(response);
        const allowed = ["ProjectID", "From", "To", "LaneID", "JobID", "Kind", "Subject", "Body", "Manifest", "ReplyTo", "FsforkPath"];
        if (!request || Array.isArray(request) || Object.keys(request).some((key) => !allowed.includes(key)) || Object.values(request).some((value) => typeof value !== "string" || composeDecode(Buffer.from(value)) !== value)) throw new Error("unsupported fields");
        const actual = Object.freeze({ ...context, lane: request.LaneID, job: request.JobID });
        if (!current() || !this.composeContextCurrent(actual) || request.ProjectID !== context.project || !this.composeRoute(request)) throw new Error("context mismatch");
        let info = null;
        try { info = await this.composeStoredContext(actual, request.Manifest ?? "", request.ReplyTo ?? ""); } catch (_e) { /* Allow exact recovery/cancel, never confirmation with unavailable links. */ }
        if (!current() || (info && !this.composeRoute(request, info))) throw new Error("route/context changed");
        if (initialState === "uncertain") c.recoveryState = status.state;
        else c.state = status.state;
        c.context = actual; c.preview = { id, state: status.state, request, raw: response, info, pages: this.composePreviewPages(request, info, response), openedThrough: 0 }; c.page = 0; c.confirm = false;
        if (!info) c.error = "Retained links/default unavailable. Exact fields remain readable and cancellable, but cannot be confirmed.";
      } catch (_e) {
        if (current()) { if (c.state !== "uncertain") c.state = "blocked"; c.error = "Preview unavailable or belongs to another project/lane. Refresh journal status; nothing was sent."; }
      }
      finally { if (this.compose === c && c.screen && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected) { c.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  composeAction(action, cx) {
    const c = this.compose, id = c.id, preview = c.preview, wasUncertain = c.state === "uncertain", context = c.context;
    if (c.working || !id || !this.composeLocal()) return;
    if (action === "dispatch" && (!c.confirm || !this.canReviewCompose())) return;
    if (action === "cancel" && !((["prepared", "armed"].includes(c.state) || (wasUncertain && ["prepared", "armed"].includes(c.recoveryState))) && preview?.id === id && this.composeCurrent(c, context))) return;
    if (action === "reconcile" && !["dispatch_intent", "uncertain", "completed"].includes(c.state)) return;
    if (action === "retire" && !["completed", "cancelled"].includes(c.state)) return;
    if (!["dispatch", "cancel", "reconcile", "retire"].includes(action)) return;
    const scope = { space: this.spaceID, folder: this.folder, project: this.selected };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen && this.composeLocal() && scope.space === this.spaceID && scope.folder === this.folder && scope.project === this.selected;
    c.working = true; c.confirm = false; c.error = null;
    cx.spawn(async (cx) => {
      let claimLaunched = false, claimKnown = false, sendLaunched = false;
      try {
        if (action === "dispatch") {
          const exact = await this.composeExchange("compose-preview", id, null, () => current() && this.composeCurrent(c, context) && c.preview === preview, cx);
          const info = await this.composeStoredContext(context, preview.request.Manifest ?? "", preview.request.ReplyTo ?? "");
          if (!current() || !this.composeCurrent(c, context) || c.preview !== preview || exact.response !== preview.raw || exact.status.state !== "prepared" || info.lane_path !== preview.info.lane_path || !this.composeRoute(preview.request, info)) throw new Error("retained fields/default changed");
          // The send process is never launched until this separate, durable
          // claim has been acknowledged. A delayed claim cannot send by itself.
          c.state = "uncertain"; c.recoveryState = null; c.preview = null; claimLaunched = true;
          const claim = await exec("ocq-herdr", ["compose-arm", id], { on: null, timeout: 30000, current: () => current() && c.id === id });
          if (claim.code !== 0) throw new Error("claim outcome unavailable");
          const armed = JSON.parse(claim.stdout);
          if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
          claimKnown = true;
          if (!current() || !this.composeCurrent(c, context)) return;
          const afterClaim = await this.composeStoredContext(context, preview.request.Manifest ?? "", preview.request.ReplyTo ?? "");
          if (!current() || !this.composeCurrent(c, context)) return;
          if (afterClaim.lane_path !== preview.info.lane_path || !this.composeRoute(preview.request, afterClaim)) throw new Error("claimed context changed");
        }
        if (!current() || c.id !== id || action === "cancel" && (c.preview !== preview || !this.composeCurrent(c, context))) throw new Error("context changed");
        if (action === "dispatch") sendLaunched = true;
        const result = await exec("ocq-herdr", [`compose-${action}`, id], { on: null, timeout: 30000, current: () => current() && c.id === id });
        if (result.code !== 0 || !current()) throw new Error("action unavailable");
        const status = JSON.parse(result.stdout);
        if (status.id !== id) throw new Error("identity mismatch");
        if (action === "retire" && status.state === "retired") { c.state = "idle"; c.id = null; c.preview = null; c.context = null; c.form = null; c.options = null; c.observation = null; }
        else if (["dispatch_intent", "completed", "cancelled"].includes(status.state)) { c.state = status.state; c.observation = status.observation; c.preview = null; c.recoveryState = null; }
        else throw new Error("unexpected state");
      } catch (_e) { if (current()) {
        if (action === "dispatch" && !claimLaunched) { c.preview = null; c.error = "Exact retained fields, links or lane default changed/unavailable. Nothing was claimed. Preview again."; }
        else if (action === "dispatch" && claimKnown && !sendLaunched) { c.state = "armed"; c.preview = null; c.error = "Claim retained, but route/default changed or became unavailable. No send launched; refresh, preview and cancel."; }
        else { c.error = action === "dispatch" || action === "reconcile" || wasUncertain ? "Outcome uncertain. Refresh and check the stored intent; do not resend." : "Action outcome unavailable. Refresh journal status."; if (wasUncertain || action === "dispatch") { c.state = "uncertain"; c.recoveryState = null; c.preview = null; } else if (action !== "reconcile") c.state = "blocked"; }
      } }
      finally { if (current()) { c.working = false; cx.notify(); } }
    });
    cx.notify();
  }

  intakeCurrent(context) {
    return Boolean(!this.intake.authoritySuspended && context && this.intake.context === context && this.intake.screen && this.composeLocal() && context.machine === this.machine && context.space === this.spaceID && context.folder === this.folder && context.selection === this.selected);
  }

  intakeSubmission(raw) {
    const r = JSON.parse(raw);
    return Object.freeze({ project: r.project_id, revision: r.binding_revision, role: r.role, lane: r.lane_id || `lane-${createHash("sha256").update(r.key).digest("hex").slice(0, 16)}`, driver: r.fsfork_driver || "git_worktree", target: r.target_job_id || null, digest: r.target_result_digest || null });
  }

  intakeMatches(kind, raw, context, admission = false) {
    const request = JSON.parse(raw);
    if (!request || typeof request !== "object" || Array.isArray(request) || typeof request.project_id !== "string" || !/^proj-[A-Za-z0-9_-]+$/.test(request.project_id)) return false;
    if (context.reviewTarget && kind !== "submission") return false;
    if (context.generatedSubmission && createHash("sha256").update(raw).digest("hex") !== context.generatedSubmission) return false;
    if (kind === "registration") {
      if (context.registration && createHash("sha256").update(raw).digest("hex") !== context.registration.digest) return false;
      if (admission && context.registration && (context.newProject ? this.projects.some((p) => p.project_id === request.project_id) : this.board?.project?.current_binding?.revision !== context.revision)) return false;
      return request.repo === context.folder && request.expected_opencode_directory === context.folder && (context.newProject ? request.expected_revision === 0 : (!context.project || request.project_id === context.project));
    }
    if (kind !== "submission" || !context.project || request.project_id !== context.project) return false;
    if (admission && (this.board?.project?.current_binding?.repo_root !== context.folder || this.board?.project?.current_binding?.revision !== context.revision || request.binding_revision !== context.revision)) return false;
    const scope = this.intakeSubmission(raw);
    if (context.submission && Object.keys(scope).some((k) => scope[k] !== context.submission[k])) return false;
    if (context.reviewTarget && (scope.role !== "review" || scope.target !== context.reviewTarget.job || scope.digest !== context.reviewTarget.digest)) return false;
    let embedded = request.request;
    for (let n = 0; n < 4; n++) {
      if (typeof embedded === "string") embedded = JSON.parse(embedded);
      else if (embedded && typeof embedded === "object" && !Array.isArray(embedded) && Object.prototype.hasOwnProperty.call(embedded, "canonical") && !Object.prototype.hasOwnProperty.call(embedded, "version")) embedded = embedded.canonical;
      else break;
    }
    return Boolean(embedded && typeof embedded === "object" && !Array.isArray(embedded) && embedded.repo === context.folder);
  }

  // Reversible ASCII rendering: each page is a slice of one JSON string
  // literal, so whitespace, control characters and Unicode remain explicit.
  intakePages(raw) {
    const escaped = JSON.stringify(raw).replace(/[ \u007f-\uffff]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
    const pages = [];
    for (let i = 0; i < escaped.length; i += 120) pages.push(escaped.slice(i, i + 120).match(/.{1,40}/g).join("\n"));
    return pages;
  }

  intakeReviewPages(kind, raw, exact = null) {
    const pages = [];
    for (const section of intakeStructured(kind, raw, exact)) {
      const chars = Array.from(section.text);
      const count = Math.max(1, Math.ceil(chars.length / 160));
      for (let n = 0; n < count; n++) pages.push({ title: section.title, text: chars.slice(n * 160, (n + 1) * 160).join(""), part: n + 1, parts: count });
    }
    if (!pages.length) throw new Error("empty review");
    return pages;
  }

  openIntake(cx, newProject = false, reviewTarget = null) {
    if (this.intake.working && this.intakeCurrent(this.intake.context)) return;
    const i = this.intake = { ...this.intake, authoritySuspended: false, working: false };
    i.screen = true; i.context = Object.freeze({ machine: this.machine, space: this.spaceID, folder: this.folder, selection: this.selected, project: newProject ? null : this.selected, revision: this.board?.project?.current_binding?.revision ?? null, newProject, reviewTarget: reviewTarget ? Object.freeze({ association: reviewTarget.association, job: reviewTarget.job, digest: reviewTarget.digest, source: reviewTarget.source, verification: reviewTarget.verification }) : null, submission: null, registration: null, generatedSubmission: null });
    if (newProject) i.kind = "registration";
    else if (reviewTarget) i.kind = "submission";
    i.entry = "form";
    this.registrationForm = new RegistrationForm(this, cx, i.context);
    this.submissionForm = new SubmissionForm(this, i.context);
    i.preview = null; i.error = null; i.state = "loading"; i.id = null; i.confirm = false; i.observation = null;
    this.keyboardMode = false; this.handle.focus();
    this.refreshIntake(cx); cx.notify();
  }

  refreshIntake(cx) {
    const i = this.intake, context = i.context;
    if (i.working || !this.intakeCurrent(context)) return;
    i.working = true;
    cx.spawn(async (cx) => {
      try {
        const result = await exec("ocq-herdr", ["intake-status"], { on: null });
        if (!this.intakeCurrent(context) || result.code !== 0) throw new Error("status unavailable");
        const status = JSON.parse(result.stdout);
        if (!["idle", "prepared", "armed", "attempted", "completed", "cancelled", "blocked"].includes(status.state)) throw new Error("invalid status");
        if (i.uncertainID && status.id !== i.uncertainID) throw new Error("uncertain intent changed");
        if (i.id !== (status.id ?? null) || i.state !== status.state) i.preview = null;
        i.state = status.state; i.id = status.id ?? null; i.confirm = false; i.observation = status.observation ?? null;
        i.error = i.uncertainID && status.state === "prepared" ? "Claim response was lost. Do not confirm again; preview and cancel this unattempted intent." : null;
      } catch (_e) { if (this.intakeCurrent(context)) { i.state = "blocked"; i.preview = null; i.error = "Status unavailable. Refresh before preparing; do not retry an uncertain operation."; } }
      finally { if (this.intakeCurrent(context)) { i.working = false; cx.notify(); } }
    });
  }

  openReviewIntake(association, cx) {
    if (!this.composeLocal() || association?.project_id !== this.selected || association.kind !== "result" || association.availability !== "available" || !/^[0-9a-f]{64}$/.test(association.manifest_digest) || !association.job_id) return;
    if (this.intake.working && this.intakeCurrent(this.intake.context)) return;
    this.intakeInput.set_value("");
    this.openIntake(cx, false, { association: association.id, job: association.job_id, digest: association.manifest_digest, source: association.source, verification: association.verification });
  }

  openImplementationIntake(cx) {
    if (!this.composeLocal() || !this.selected || (this.intake.working && this.intakeCurrent(this.intake.context))) return;
    this.intake.kind = "submission";
    this.openIntake(cx);
  }

  async intakeExchange(mode, kind, id, raw, context, cx) {
    if (!this.intakeCurrent(context)) throw new Error("context changed");
    const current = compatibility.transfer(() => this.intakeCurrent(context));
    const launch = randomBytes(32).toString("hex");
    const command = mode === "receive" ? ["intake-receive-text", kind, launch] : mode === "draft" ? [`intake-${kind}-draft`, launch] : ["intake-review-preview", id, launch];
    const worker = exec("ocq-herdr", command, { on: null, timeout: 25000, current: () => this.intakeCurrent(context) }).then((value) => value, () => null);
    let socket = null;
    try {
      let rendezvous = null;
      for (let n = 0; n < 100; n++) {
        if (!this.intakeCurrent(context)) throw new Error("context changed");
        const result = await exec("ocq-herdr", ["intake-rendezvous", launch], { on: null });
        if (result.code === 0) { rendezvous = JSON.parse(result.stdout); break; }
        await cx.sleep(50);
      }
      if (!rendezvous || rendezvous.launch !== launch || !/^[0-9a-f]{64}$/.test(rendezvous.capability) || !Number.isInteger(rendezvous.port) || rendezvous.port < 1 || rendezvous.port > 65535) throw new Error("rendezvous unavailable");
      socket = await connect("127.0.0.1", rendezvous.port);
      const frames = socketFrames(socket, mode === "receive" ? 131072 : 1048576), nonce = randomBytes(32).toString("hex");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(nonce + "\n");
      if (!composeEqual(await frames.line(64), composeProof(rendezvous.capability, nonce, "server"))) throw new Error("server authentication failed");
      if (!current()) throw new Error("Transfer authorization changed");
      await socket.write(composeProof(rendezvous.capability, nonce, "client") + "\n");
      if (await frames.line(64) !== "READY" || !current()) throw new Error("context or authentication changed");
      let response;
      if (mode === "receive" || mode === "draft") {
        const bytes = Buffer.from(raw, "utf8");
        if (!bytes.length || bytes.length > (mode === "draft" && kind === "registration" ? 16384 : 131072) || composeDecode(bytes) !== raw) throw new Error("invalid request");
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(`${bytes.length}\n`);
        if (!current()) throw new Error("Transfer authorization changed");
        await socket.write(raw);
        if (mode === "receive") {
          response = await frames.line(80);
          if (!/^PREPARED [0-9a-f]{64}$/.test(response)) throw new Error("preparation uncertain");
        }
      }
      if (mode !== "receive") {
        const header = await frames.bytes(5);
        const length = header[1] * 16777216 + header[2] * 65536 + header[3] * 256 + header[4];
        if ((header[0] !== 82 && header[0] !== 83) || !length || length > 1048576) throw new Error("invalid preview frame");
        const exact = JSON.parse(composeDecode(await frames.bytes(length)));
        if (typeof exact.raw !== "string" || Buffer.from(exact.raw).length > 131072 || createHash("sha256").update(exact.raw).digest("hex") !== exact.sha256) throw new Error("exact preview changed");
        response = { kind: header[0] === 82 ? "registration" : "submission", raw: exact.raw, exact };
      }
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !current()) throw new Error("exchange uncertain");
      if (mode === "draft" && JSON.parse(done.stdout).state !== "draft") throw new Error("draft unavailable");
      if (mode === "preview") {
        const status = JSON.parse(done.stdout);
        if (status.id !== id || !["prepared", "armed", "attempted", "completed", "cancelled"].includes(status.state)) throw new Error("preview state changed");
        response.state = status.state;
      }
      return response;
    } finally { if (socket) socket.close(); await worker; }
  }

  buildRegistration(cx) {
    const i = this.intake, form = this.registrationForm;
    let context = i.context;
    if (i.working || i.state !== "idle" || i.kind !== "registration" || i.entry !== "form" || !this.intakeCurrent(context) || context.reviewTarget) return;
    const draft = form.values(), input = JSON.stringify(draft);
    try {
      if (!Number.isSafeInteger(draft.expected_revision) || (context.newProject ? draft.expected_revision !== 0 : !context.project || draft.expected_revision !== context.revision || draft.expected_revision < 1)) throw new Error("revision unavailable");
      for (const value of Object.values(draft).flat()) if (typeof value === "string" && composeDecode(Buffer.from(value)) !== value) throw new Error("unsupported Unicode");
      if (draft.repo !== context.folder || draft.expected_opencode_directory !== context.folder) throw new Error("directory mismatch");
    } catch (_e) { i.error = "Explicit project context, exact current revision and matching directories are required. Nothing was built."; cx.notify(); return; }
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("draft", "registration", null, input, context, cx);
        if (!this.intakeCurrent(context) || i.kind !== "registration" || i.entry !== "form" || this.registrationForm !== form || JSON.stringify(form.values()) !== input || !this.intakeMatches("registration", result.raw, context)) throw new Error("draft context changed");
        const request = JSON.parse(result.raw);
        if (request.expected_revision !== draft.expected_revision || request.project_id !== draft.project_id) throw new Error("draft identity changed");
        const review = this.intakeReviewPages("registration", result.raw, result.exact);
        context = Object.freeze({ ...context, registration: Object.freeze({ digest: result.exact.sha256 }) }); i.context = context;
        i.preview = { draft: true, kind: "registration", raw: result.raw, exact: result.exact, review, rawPages: this.intakePages(result.raw).map((text) => ({ title: "Generated exact JSON string", text })), pages: review, view: "review", openedThrough: 0 };
        i.page = 0; i.confirm = false;
      } catch (_e) { if (this.intakeCurrent(context)) i.error = "Draft unavailable. Check all required fields, exact identities, sizes and context. Nothing was prepared or registered."; }
      finally { if (this.intakeCurrent(context)) { i.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  intakeEntry(entry, cx) {
    const i = this.intake;
    if (i.working || i.state !== "idle" || !this.intakeCurrent(i.context)) return;
    i.context = Object.freeze({ ...i.context, registration: null, submission: null, generatedSubmission: null });
    i.preview = null; i.entry = entry; i.error = null; i.confirm = false;
    cx.notify();
  }

  buildSubmission(cx) {
    const i = this.intake, form = this.submissionForm, snapshot = form.snapshot();
    let context = i.context, input;
    if (i.working || i.state !== "idle" || i.kind !== "submission" || i.entry !== "form" || !this.intakeCurrent(context) || context.newProject) return;
    try {
      const draft = form.values(context);
      if (!context.project || !Number.isSafeInteger(context.revision) || context.revision < 1 || draft.repo !== context.folder || this.board?.project?.current_binding?.revision !== context.revision) throw new Error("binding changed");
      const check = (value) => {
        if (typeof value === "string" && composeDecode(Buffer.from(value)) !== value) throw new Error("unsupported Unicode");
        if (typeof value === "number" && (!Number.isSafeInteger(value) || Object.is(value, -0))) throw new Error("unsafe number");
        if (Array.isArray(value)) value.forEach(check);
        else if (value && typeof value === "object") for (const [key, item] of Object.entries(value)) { check(key); check(item); }
      };
      check(draft);
      check(JSON.parse(draft.permissions));
      input = JSON.stringify(draft);
    } catch (_e) { i.error = "Check required fields, JSON lists, explicit safe memory count and exact current project binding. Nothing was built."; cx.notify(); return; }
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("draft", form.role, null, input, context, cx);
        if (!this.intakeCurrent(context) || i.kind !== "submission" || i.entry !== "form" || this.submissionForm !== form || form.snapshot() !== snapshot || result.kind !== "submission" || !this.intakeMatches("submission", result.raw, context, true)) throw new Error("draft context changed");
        const scope = this.intakeSubmission(result.raw);
        if (scope.role !== form.role || scope.revision !== context.revision) throw new Error("draft scope changed");
        const review = this.intakeReviewPages("submission", result.raw, result.exact);
        context = Object.freeze({ ...context, submission: scope, generatedSubmission: result.exact.sha256 }); i.context = context;
        i.preview = { draft: true, kind: "submission", raw: result.raw, review, rawPages: this.intakePages(result.raw).map((text) => ({ title: "Generated exact JSON string", text })), pages: review, view: "review", openedThrough: 0 };
        i.page = 0; i.confirm = false;
      } catch (_e) { if (this.intakeCurrent(context)) i.error = "Draft unavailable. Check exact binding/target, full fields, permissions and size (review prompt ≤16 KiB). Nothing was prepared or submitted; no target content is truncated."; }
      finally { if (this.intakeCurrent(context)) { i.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  prepareIntake(cx, generated = null) {
    const i = this.intake, raw = generated ? generated.raw : this.intakeInput.value(), kind = i.kind;
    let context = i.context;
    if (i.working || i.state !== "idle" || !this.intakeCurrent(context)) return;
    if (generated ? i.preview !== generated || !generated.draft || generated.view !== "review" || generated.openedThrough !== generated.review.length - 1 || i.page !== generated.review.length - 1 : i.entry === "form") return;
    try {
      const bytes = Buffer.from(raw, "utf8");
      if (!bytes.length || bytes.length > (kind === "registration" ? 16384 : 131072) || composeDecode(bytes) !== raw || !this.intakeMatches(kind, raw, context, true)) throw new Error("invalid request or context");
      if (context.newProject && (kind !== "registration" || this.projects.some((p) => p.project_id === JSON.parse(raw).project_id))) throw new Error("new project already exists");
      this.intakeReviewPages(kind, raw);
    } catch (_e) { i.error = "Request is invalid, belongs to another context, or uses an unsupported review representation. Nothing was prepared."; cx.notify(); return; }
    if (kind === "submission") { context = Object.freeze({ ...context, submission: this.intakeSubmission(raw) }); i.context = context; }
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("receive", kind, null, raw, context, cx);
        if (this.intakeCurrent(context)) { i.id = result.slice(9); i.state = "prepared"; i.preview = null; }
      } catch (_e) { if (this.intakeCurrent(context)) { i.state = "blocked"; i.error = "Preparation outcome unknown. Refresh the journal; do not prepare again until it is resolved."; } }
      finally { if (this.intakeCurrent(context)) { i.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  previewIntake(cx) {
    const i = this.intake, id = i.id;
    let context = i.context;
    if (i.working || !id || !this.intakeCurrent(context) || !["prepared", "armed", "attempted", "completed", "cancelled"].includes(i.state)) return;
    i.working = true; i.preview = null; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("preview", null, id, null, context, cx);
        if (!this.intakeCurrent(context) || !this.intakeMatches(result.kind, result.raw, context)) throw new Error("context mismatch");
        const rawPages = this.intakePages(result.raw).map((text) => ({ title: "Exact raw JSON string", text }));
        let review = null;
        try { review = this.intakeReviewPages(result.kind, result.raw, result.exact); } catch (_e) { /* Keep exact raw view, never invent a summary. */ }
        if (result.kind === "submission") { context = Object.freeze({ ...context, submission: this.intakeSubmission(result.raw) }); i.context = context; }
        i.state = result.state; i.preview = { id, kind: result.kind, raw: result.raw, state: result.state, review, rawPages, pages: review || rawPages, view: review ? "review" : "raw", openedThrough: 0 }; i.page = 0; i.confirm = false;
        if (!review) i.error = "This envelope representation is not supported for structured review. Exact raw view only; dispatch remains unavailable.";
      } catch (_e) { if (this.intakeCurrent(context)) i.error = "Preview unavailable or belongs to another project or folder. Nothing was dispatched."; }
      finally { if (this.intakeCurrent(context)) { i.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  advanceIntakePage(delta, cx) {
    const i = this.intake, preview = i.preview;
    if (!preview || i.working || !this.intakeCurrent(i.context)) return;
    const next = Math.max(0, Math.min(preview.pages.length - 1, i.page + delta));
    if (preview.view === "review" && delta === 1 && i.page === preview.openedThrough && next === i.page + 1) preview.openedThrough = next;
    i.page = next;
    cx.notify();
  }

  canConfirmIntake() {
    const i = this.intake, p = i.preview;
    return Boolean(!i.working && !i.uncertainID && this.intakeCurrent(i.context) && i.state === "prepared" && p?.id === i.id && p.state === "prepared" && p.review && p.view === "review" && i.page === p.review.length - 1 && p.openedThrough === p.review.length - 1 && this.intakeMatches(p.kind, p.raw, i.context, true));
  }

  confirmIntake(cx) {
    const i = this.intake, context = i.context, preview = i.preview, id = i.id;
    if (!i.confirm || !this.canConfirmIntake()) return;
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      let armLaunched = false;
      try {
        const exact = await this.intakeExchange("preview", null, id, null, context, cx);
        if (!this.intakeCurrent(context) || !i.confirm || i.preview !== preview || i.id !== id || exact.state !== "prepared" || exact.kind !== preview.kind || exact.raw !== preview.raw || !this.intakeMatches(exact.kind, exact.raw, context, true)) throw new Error("preview changed");
        i.uncertainID = id; armLaunched = true;
        const claim = await exec("ocq-herdr", ["intake-arm", id], { on: null, timeout: 30000, current: () => this.intakeCurrent(context) && i.preview === preview && i.id === id });
        if (!claim || claim.code !== 0) throw new Error("claim outcome unknown");
        const armed = JSON.parse(claim.stdout);
        if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
        if (!this.intakeCurrent(context) || !i.confirm || i.preview !== preview || i.id !== id || !this.intakeMatches(preview.kind, preview.raw, context, true)) return;
        i.confirm = false; i.state = "armed";
        const result = await exec("ocq-herdr", ["intake-dispatch", id], { on: null, timeout: 30000, current: () => this.intakeCurrent(context) && i.preview === preview && i.id === id });
        if (!result || result.code !== 0 || !this.intakeCurrent(context) || i.preview !== preview || i.id !== id) throw new Error("dispatch outcome unknown");
        const status = JSON.parse(result.stdout);
        if (status.id !== id || !["attempted", "completed", "blocked"].includes(status.state)) throw new Error("dispatch identity mismatch");
        i.state = status.state; i.observation = status.observation ?? null; i.preview = null;
        if (status.state === "completed") i.uncertainID = null;
        else i.error = "Outcome uncertain. Check the exact stored receipt; do not retry.";
      } catch (_e) {
        if (this.intakeCurrent(context) && i.id === id) {
          i.confirm = false; i.preview = null; i.state = "blocked";
          i.error = armLaunched ? "Claim or dispatch outcome unknown. Refresh and check the retained intent; do not retry." : "Exact preview or context could not be rechecked. Nothing was claimed.";
        }
      } finally { if (this.intakeCurrent(context)) { i.working = false; cx.notify(); } }
    });
    cx.notify();
  }

  intakeAction(action, cx) {
    const i = this.intake, id = i.id;
    let context = i.context;
    if (i.working || !this.intakeCurrent(context) || i.preview?.id !== id || i.preview?.state !== i.state) return;
    if (action === "cancel" && !["prepared", "armed"].includes(i.state)) return;
    if (action === "reconcile" && !["attempted", "completed"].includes(i.state)) return;
    if (action === "retire" && !["cancelled", "completed"].includes(i.state)) return;
    if (!["cancel", "reconcile", "retire"].includes(action)) return;
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.intakeCurrent(context) || i.id !== id || i.preview?.id !== id || i.preview?.state !== i.state) throw new Error("context changed");
        const result = await exec("ocq-herdr", [`intake-${action}`, id], { on: null, timeout: 30000, current: () => this.intakeCurrent(context) && i.id === id });
        if (result.code !== 0 || !this.intakeCurrent(context)) throw new Error("action unavailable");
        const status = JSON.parse(result.stdout);
        const expected = action === "cancel" ? ["cancelled"] : action === "retire" ? ["retired"] : ["attempted", "completed", "blocked"];
        if (status.id !== id || !expected.includes(status.state)) throw new Error("identity mismatch");
        if (action === "retire") {
          // The retired envelope's scope must not constrain the next intent.
          // Keep the selected-result authorization, but invalidate old callbacks.
          context = Object.freeze({ ...context, submission: null, registration: null, generatedSubmission: null }); i.context = context;
          i.state = "idle"; i.id = null; i.preview = null; i.uncertainID = null; i.observation = null;
        }
        else { i.state = status.state; i.preview.state = status.state; i.observation = status.observation ?? null; if (["completed", "cancelled"].includes(status.state)) i.uncertainID = null; }
      } catch (_e) { if (this.intakeCurrent(context)) { i.state = "blocked"; i.preview = null; i.error = "Action outcome unavailable. Refresh the journal before proceeding."; } }
      finally { if (this.intakeCurrent(context)) { i.working = false; cx.notify(); } }
    });
    cx.notify();
  }

  targets() {
    return [...(this.headerTargets || []), ...(this.compatibilityTargets || []), ...(this.renderTargets || [])];
  }

  // Register alongside the rendered control, including its original callback
  // guard. Hidden sections never contribute keyboard actions. Inspection identity
  // is separate from navigation identity so data updates cannot redirect it.
  registerAction(id, label, action, list = this.renderTargets) {
    const run = cx => { this.handle.focus(); this.navigationTarget = id; this.navigationLabel = label; action(cx); };
    list.push({ id, label, run });
    if (this.navigationTarget === id) this.navigationLabel = label;
    return (_e, cx) => { cx.stop_propagation(); run(cx); };
  }

  key(e, cx) {
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    if (this.intake.screen && !this.intake.authoritySuspended) {
      if (e.key === "f6") { this.handle.focus(); this.intakeInputFocused = false; if (this.registrationForm) this.registrationForm.focused = false; if (this.submissionForm) this.submissionForm.focused = false; cx.stop_propagation(); cx.notify(); return; }
      if (this.intakeInputFocused || (this.intake.entry === "form" && (this.intake.kind === "registration" ? this.registrationForm?.focused : this.submissionForm?.focused))) {
        if (e.key === "escape") { this.handle.focus(); cx.stop_propagation(); }
        return;
      }
      const i = this.intake;
      if (i.confirm) {
        if (e.key === "escape" && !i.working) { i.confirm = false; cx.stop_propagation(); cx.notify(); }
        return;
      }
      if (e.key === "escape") i.screen = false;
      else if (!e.is_held && e.key === "g" && i.entry === "form") { if (i.kind === "registration") this.buildRegistration(cx); else this.buildSubmission(cx); }
      else if (!e.is_held && ["left", "right"].includes(e.key) && !i.preview && i.state === "idle" && i.entry === "form" && !i.working) (i.kind === "registration" ? this.registrationForm : this.submissionForm).advance(e.key === "right" ? 1 : -1, this, cx);
      else if (!e.is_held && e.key === "r") this.refreshIntake(cx);
      else if (!e.is_held && e.key === "p") this.prepareIntake(cx, i.preview?.draft ? i.preview : null);
      else if (!e.is_held && e.key === "v") this.previewIntake(cx);
      else if (!e.is_held && e.key === "c") this.intakeAction("cancel", cx);
      else if (!e.is_held && e.key === "t") this.intakeAction("retire", cx);
      else if (!e.is_held && e.key === "left" && i.preview) this.advanceIntakePage(-1, cx);
      else if (!e.is_held && e.key === "right" && i.preview) this.advanceIntakePage(1, cx);
      else return;
      cx.stop_propagation(); cx.notify(); return;
    }
    if (this.compose.screen && !this.compose.authoritySuspended) {
      const c = this.compose;
      if (e.key === "f6") { this.handle.focus(); if (c.form) c.form.focused = false; cx.stop_propagation(); cx.notify(); return; }
      if (c.form?.focused) {
        if (e.key === "escape") { this.handle.focus(); cx.stop_propagation(); }
        return;
      }
      if (e.key === "escape") { if (c.confirm) c.confirm = false; else this.closeCompose(cx); }
      else if (!e.is_held && e.key === "r") this.refreshCompose(cx);
      else if (!e.is_held && e.key === "p" && c.state === "idle") this.prepareCompose(cx);
      else if (!e.is_held && e.key === "v") this.previewCompose(cx);
      else if (!e.is_held && e.key === "c") this.composeAction("cancel", cx);
      else if (!e.is_held && e.key === "k") this.composeAction("reconcile", cx);
      else if (!e.is_held && e.key === "t") this.composeAction("retire", cx);
      else if (!e.is_held && ["left", "right"].includes(e.key) && c.state === "idle" && c.form && !c.working) c.form.advance(e.key === "right" ? 1 : -1, this, cx);
      else if (!e.is_held && e.key === "left" && c.preview && !c.confirm) this.advanceComposePage(-1, cx);
      else if (!e.is_held && e.key === "right" && c.preview && !c.confirm) this.advanceComposePage(1, cx);
      else if (!e.is_held && e.key === "s" && !c.confirm && this.canReviewCompose()) c.confirm = true;
      else if (!e.is_held && e.key === "y" && c.confirm) this.composeAction("dispatch", cx);
      else return;
      cx.stop_propagation(); cx.notify(); return;
    }
    if (this.mailConfirmation && this.mailContextCurrent(this.mailConfirmation)) {
      if (e.key === "escape" || e.key === "n") { this.mailConfirmation = null; cx.notify(); }
      else if (e.key === "y" && !e.is_held) this.confirmArchive(cx);
      cx.stop_propagation();
      return;
    }
    const targets = this.targets();
    if (!targets.length) return;
    let index = targets.findIndex(t => t.id === this.navigationTarget);
    if (index < 0) index = 0;
    if (e.key === "down" || e.key === "up") {
      index = (index + (e.key === "down" ? 1 : targets.length - 1)) % targets.length;
      this.navigationTarget = targets[index].id;
      this.navigationLabel = targets[index].label;
    } else if (e.key === "escape") this.keyboardMode = false;
    else if (e.key === "left") this.keyboardPage = Math.max(0, this.keyboardPage - 1);
    else if (e.key === "right") this.keyboardPage = Math.min(this.keyboardPages(this.keyboardTarget).length - 1, this.keyboardPage + 1);
    else if (e.key === "enter" || e.key === "space") {
      if (e.is_held) return;
      targets[index].run(cx);
    } else return;
    cx.stop_propagation();
    cx.notify();
  }

  openTarget(kind, id, cx) {
    this.keyboardTarget = { kind, id };
    this.keyboardPage = 0;
    this.keyboardMode = true;
    this.handle.focus();
    if (kind === "message") {
      const message = this.collections.inbox.items.find(item => item.id === id);
      this.mailObservation = message ? { id, sequence: message.sequence_text, status: "loading", data: null } : null;
    }
    if (kind === "manifest") this.inspectManifest(id, cx);
    else cx.notify();
  }

  openQueue(cx) {
    if (!spaces().some((s) => s.active && s.id === this.spaceID && s.machine === this.machine && s.folder === this.folder)) return;
    this.queueMode = true;
    this.queue.reset();
    this.handle.focus();
    this.queue.refresh(this, cx);
  }

  leaveQueue(cx) {
    this.queueMode = false;
    this.queue.reset();
    // Invalidate any pre-Queue project read and observe the current context
    // immediately. refresh/render retain their existing machine/space guards.
    this.generation++;
    this.loading = false;
    this.refresh(cx);
  }

  mailKey(machine, project, id) {
    return `ocq.mail-action.v1:${JSON.stringify([machine, project, id])}`;
  }

  mailState(message) {
    const key = this.mailKey(this.machine, this.selected, message.id);
    if (this.mailActions[key]) return this.mailActions[key];
    try {
      const raw = localStorage.getItem(key);
      if (!raw) return null;
      const saved = JSON.parse(raw);
      if (saved.version !== 1 || !["read", "archive"].includes(saved.mode) || typeof saved.sequence !== "string" || !/^[0-9]+$/.test(saved.sequence)) throw new Error("invalid journal");
      return this.mailActions[key] = { status: "uncertain", mode: saved.mode, sequence: saved.sequence, authoritySuspended: Boolean(this.observationReset) };
    } catch (_e) {
      return this.mailActions[key] = { status: "blocked", mode: "unknown" };
    }
  }

  mailOptions(message) {
    const state = this.mailState(message);
    if (state?.authoritySuspended) return { check: false, read: false, archive: false };
    if (state && state.status !== "observed") return { check: state.status === "uncertain", read: false, archive: false };
    const validSequence = typeof message.sequence_text === "string" && /^[0-9]+$/.test(message.sequence_text);
    const read = message.read_at || state?.message?.read_at;
    const archived = message.archived_at || state?.message?.archived_at;
    return { read: validSequence && !read, archive: validSequence && Boolean(read) && !archived, check: false };
  }

  mailStatus(message) {
    const state = this.mailState(message);
    if (!state) return "";
    if (state.authoritySuspended) return "Prior-context private action evidence retained; not a current message observation or authorization.";
    if (state.status === "journaling") return "Saving the action record; no request has been sent yet.";
    if (state.status === "pending") return `${state.mode} request in progress; outcome not yet known.`;
    if (state.status === "checking") return "Reading back the exact stored message…";
    if (state.status === "blocked") return "Local action record unavailable or invalid. Actions are blocked; investigate before proceeding.";
    if (state.status === "uncertain") return `${state.mode} outcome uncertain${state.readAt ? `; last checked ${state.readAt}` : ""}. Check the stored message; do not retry blindly.`;
    return `Confirmed stored state at ${state.readAt}: read ${state.message?.read_at || "not recorded"}; archived ${state.message?.archived_at || "not recorded"}.`;
  }

  mailContext(message) {
    const active = spaces().find((s) => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder || !this.selected || message.project_id !== this.selected) return null;
    return { generation: this.generation, space: active.id, machine: active.machine, folder: active.folder, project: this.selected,
      id: message.id, sequence: message.sequence_text, subject: message.subject,
      key: this.mailKey(active.machine, this.selected, message.id) };
  }

  mailContextCurrent(context) {
    const active = spaces().find((s) => s.active);
    return Boolean(!this.closed && context.generation === this.generation && active && active.id === context.space && active.machine === context.machine && active.folder === context.folder && this.spaceID === context.space && this.selected === context.project);
  }

  requestMailAction(message, archive, cx) {
    if (!compatibility.ready()) return;
    const context = this.mailContext(message), options = this.mailOptions(message);
    if (!context || !(archive ? options.archive : options.read)) return;
    if (archive) {
      this.mailConfirmation = context;
      this.handle.focus();
      cx.notify();
    } else this.sendMailAction(context, "read", cx);
  }

  confirmArchive(cx) {
    const context = this.mailConfirmation;
    this.mailConfirmation = null;
    if (!context || !this.mailContextCurrent(context)) { cx.notify(); return; }
    const message = this.collections.inbox.items.find((m) => m.id === context.id && m.sequence_text === context.sequence);
    if (message && this.mailOptions(message).archive) this.sendMailAction(context, "archive", cx);
    else cx.notify();
  }

  sendMailAction(context, mode, cx) {
    if (!compatibility.ready() || !this.mailContextCurrent(context)) return;
    const previous = this.mailActions[context.key];
    if (previous && previous.status !== "observed") return;
    // The actionable target disappears while its request is in flight. Keep
    // the message selected so keyboard users can see the outcome/read-back.
    if (this.keyboardMode && this.keyboardTarget.id === context.id) {
      this.keyboardTarget = { kind: "message", id: context.id };
      this.keyboardPage = 0;
    }
    const state = { status: "journaling", mode, sequence: context.sequence };
    this.mailActions[context.key] = state;
    cx.spawn(async (cx) => {
      try {
        if (!compatibility.ready() || !this.mailContextCurrent(context)) return;
        localStorage.setItem(context.key, JSON.stringify({ version: 1, mode, sequence: context.sequence }));
        await localStorage.flush();
        if (!this.mailContextCurrent(context)) {
          state.status = "blocked";
          state.error = "Context changed after journal persistence; retained evidence only, no dispatch.";
          cx.notify();
          return;
        }
      } catch (_e) {
        // A failed journal write or flush must not dispatch.
        state.status = "blocked";
        cx.notify();
        return;
      }
      state.status = "pending";
      cx.notify();
      // Neither a response nor a transport failure establishes the outcome.
      try {
        await exec("ocq-herdr", ["mail-ack", context.project, context.id, context.sequence, mode], { on: context.machine, current: () => this.mailContextCurrent(context) });
      } catch (_e) { /* The one dispatched attempt may have committed. */ }
      await this.readBackMail(context, state, cx);
    });
    cx.notify();
  }

  checkMailAction(message, cx) {
    const context = this.mailContext(message), state = this.mailState(message);
    if (!context || !state || state.authoritySuspended || state.status !== "uncertain" || state.sequence !== context.sequence) return;
    if (this.keyboardMode && this.keyboardTarget.id === context.id) {
      this.keyboardTarget = { kind: "message", id: context.id };
      this.keyboardPage = 0;
    }
    state.status = "checking";
    cx.spawn(async (cx) => { await this.readBackMail(context, state, cx); });
    cx.notify();
  }

  async readBackMail(context, state, cx) {
    // Both automatic and manual checks use the captured machine and exact IDs.
    state.status = "checking";
    cx.notify();
    try {
      const result = await exec("ocq-herdr", ["mail", context.project, context.id, state.sequence], { on: context.machine });
      if (!this.mailContextCurrent(context)) { state.status = "uncertain"; state.authoritySuspended = true; return; }
      if (result.code !== 0) throw new Error("read-back failed");
      const observed = JSON.parse(result.stdout), item = observed.message;
      if (!item || item.id !== context.id || item.project_id !== context.project || item.sequence_text !== state.sequence || state.sequence !== context.sequence || !observed.read_at) throw new Error("read-back identity mismatch");
      if (!item.read_at || (state.mode === "archive" && !item.archived_at)) {
        state.status = "uncertain";
        state.readAt = observed.read_at;
      } else {
        localStorage.removeItem(context.key);
        await localStorage.flush();
        state.status = "observed";
        state.message = item;
        state.readAt = observed.read_at;
        // Retain explicit action read-back separately. Only revision-bound
        // observation slots update the visible inbox/exact-mail projection.
      }
    } catch (_e) { state.status = "uncertain"; }
    cx.notify();
  }

  // A field-labelled document keeps each selected item reachable even when its
  // board row is offscreen. Long values continue at word/line boundaries.
  keyboardPages(target) {
    let value;
    if (target.kind === "project") value = this.projects.find((p) => p.project_id === target.id);
    else if (target.kind === "summary") value = this.boardError || this.board?.project;
    else if (target.kind === "job") {
      const stored = this.board?.project?.jobs?.find((j) => j.job_id === target.id);
      const detail = this.detailJob === target.id ? this.detail?.job : null;
      value = { attention: detail ? attentionDetail(detail) : attention(stored) ? {
        status: attention(stored), observation_time: "not recorded; current request status unknown",
        guidance: "Activate to load the recorded session ID and location; this does not observe the runtime.",
      } : null, stored_job: stored,
        job_detail: this.detailJob === target.id ? (this.detailLoading ? "Loading job detail…" : this.detailError || this.detail) : "Activate to inspect job details" };
    } else if (target.kind === "manifest") {
      value = this.manifestDigest === target.id ? (this.manifestLoading ? "Loading manifest…" : this.manifestError || this.manifest) : "Activate to read the retained manifest bytes.";
      if (this.manifestDigest === target.id && this.manifest && !this.manifestLoading && !this.manifestError) {
        try { value = JSON.parse(this.manifest); } catch (_e) { value = this.manifest; }
      }
    } else if (target.kind === "manifest-text") {
      value = this.manifestDigest === target.id ? (this.manifestLoading ? "Loading manifest…" : this.manifestError || this.manifest) : "Read the manifest first.";
    } else if (target.kind === "message") value = this.mailObservation?.id === target.id ? (this.mailObservation.status === "ready" ? this.mailObservation.data : `Selected message: ${this.mailObservation.status}`) : this.collections.inbox.items.find((m) => m.id === target.id);
    else if (target.kind === "mail-read") value = "Activate to mark this message read. Opening it does not mark it read. Its stored state is checked afterward.";
    else if (target.kind === "mail-archive") value = "Activate to review a separate archive confirmation. Archiving cannot be undone; the message remains retained.";
    else if (target.kind === "mail-check") value = "Activate to read back the exact message. An absent change does not prove a delayed request cannot still complete.";
    else if (target.kind === "result") value = this.collections.results.items.find((r) => r.event.id === target.id) || this.collections.results.error || "Selected completion is not present in the loaded pages at this read.";
    else if (target.kind === "lane") value = (this.lanes || this.board?.project?.lanes || []).find((lane) => lane.lane_id === target.id) || this.lanePages.map(page => page.error).filter(Boolean).join("\n") || "Selected lane is not present in the loaded pages at this read.";
    else value = target.kind === "refresh" ? "Activate to refresh the stored project board." : target.kind === "compose" ? "Activate to open local compose and recovery. Choose a lane to start a new update." : `Activate to ${target.label.toLowerCase()}.`;
    const pages = [];
    const addText = (title, text) => {
      const chars = Array.from(text);
      if (!chars.length) { pages.push({ title, text: "Empty" }); return; }
      let start = 0;
      while (start < chars.length) {
        let end = Math.min(start + 240, chars.length), lines = 0;
        for (let i = start; i < end; i++) {
          if (chars[i] === "\n" && ++lines === 4) { end = i + 1; break; }
        }
        if (end < chars.length && chars[end - 1] !== "\n") {
          for (let i = end - 1; i > start + 100; i--) {
            if (/\s/.test(chars[i])) { end = i + 1; break; }
          }
        }
        pages.push({ title, text: chars.slice(start, end).join("") });
        start = end;
      }
    };
    const walk = (item, title) => {
      if (item === null || item === undefined) { addText(title, "Not recorded"); return; }
      if (Array.isArray(item)) {
        if (!item.length) addText(title, "None");
        item.forEach((entry, i) => walk(entry, `${title} · ${i + 1}`));
      } else if (typeof item === "object") {
        const entries = Object.entries(item);
        if (!entries.length) addText(title, "None");
        for (const [key, entry] of entries) walk(entry, `${title} · ${key.replace(/_/g, " ")}`);
      } else if (typeof item === "number" && Number.isInteger(item) && !Number.isSafeInteger(item)) {
        addText(title, "Number cannot be represented exactly by this view.");
      } else addText(title, typeof item === "boolean" ? (item ? "Yes" : "No") : String(item));
    };
    walk(value, "Details");
    return pages;
  }

  inspect(jobID, cx) {
    const active = spaces().find((s) => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) { cx.notify(); return; }
    if (this.detailJob === jobID) {
      this.detailGeneration++;
      this.detailJob = null;
      this.detail = null;
      this.detailError = null;
      this.detailLoading = false;
      cx.notify();
      return;
    }
    ++this.detailGeneration;
    this.projectDetailToken = {};
    this.detailJob = jobID;
    this.detail = null;
    this.detailError = null;
    this.detailLoading = true;
    cx.notify();
  }

  inspectManifest(digest, cx) {
    const active = spaces().find((s) => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) { cx.notify(); return; }
    ++this.manifestGeneration;
    this.manifestToken = {};
    this.manifestDigest = digest;
    this.manifest = null;
    this.manifestError = null;
    this.manifestLoading = true;
    cx.notify();
  }

  render(cx) {
    compatibility.observe();
    this.renderTargets = []; this.headerTargets = []; this.compatibilityTargets = [];
    const content = this.renderContent(cx);
    const t = theme();
    const workspace = !(this.intake.screen && !this.intake.authoritySuspended) && !(this.compose.screen && !this.compose.authoritySuspended) && !this.publication.screen && !this.worktreeCheck.screen && !(this.mailConfirmation && this.mailContextCurrent(this.mailConfirmation)) &&
      ![this.queue.dispatcher, this.queue.submission, this.queue.agentSubmission].some(helper => helper.screen && helper.current(this)) &&
      !(this.queue.ack.confirm && this.queue.ackCurrent(this, this.queue.ack)) &&
      ![this.queue.cancel, this.queue.resolution, this.queue.reconciliation].some(helper => helper.confirm && helper.current(this));
    return v_flex().size_full().min_h(0).bg(t.background).text_color(t.foreground)
      .when(workspace, e => e.child(this.workspaceHeader(cx, t)))
      .when(Boolean(this.observationReset) && !workspace, e => e.child(div().p(8).text_size(12).text_color(t.blocked).child("Prior context suspended · private journals/reviews retained as evidence only. Reopen a fresh selected context for a new review.")))
      .child(compatibility.notice(cx, t, (id, label, action) => this.registerAction(id, label, action, this.compatibilityTargets)))
      .child(div().flex_1().min_h(0).child(content));
  }

  // Controller seam: observation health only, never action authorization or a
  // synthesized connection claim. The transport owns context/revision ordering.
  applyUpdateHealth(scope, health, cx) {
    const active = spaces().find(s => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder || scope.generation !== this.generation || scope.space !== this.spaceID || scope.machine !== this.machine || scope.folder !== this.folder) return false;
    if (!["stored", "reading", "stale", "unavailable"].includes(health.state)) return false;
    this.updateHealth = { state: health.state, reason: health.reason || null };
    cx.notify(); return true;
  }

  applyProjectObservation(scope, board, cx, meta = null) {
    const active = spaces().find(s => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder || this.loading || scope.generation !== this.generation || scope.space !== this.spaceID || scope.machine !== this.machine || scope.folder !== this.folder || scope.project !== this.selected || board?.project?.project_id !== this.selected) return false;
    if (meta ? !acceptObservationVersion(this.boardVersion, meta) : (!board.project.read_at || readOrder(board.project.read_at) < readOrder(this.board?.project?.read_at))) return false;
    // Only stored board data changes: retain inspection, review pages, mail
    // intents, collection cursors and forms. Existing action guards still apply.
    const binding = board.project.current_binding;
    // Guided forms retain their exact safe-integer binding revision contract.
    // The database observation revision itself is NEVER converted to Number.
    if (typeof binding?.revision === "string" && /^(0|[1-9][0-9]*)$/.test(binding.revision) && (binding.revision.length < 16 || (binding.revision.length === 16 && binding.revision <= "9007199254740991"))) board = { ...board, project: { ...board.project, current_binding: { ...binding, revision: Number(binding.revision) } } };
    this.board = board; if (meta) { this.boardVersion = meta; this.boardToken.observationVersion = meta; }
    cx.notify(); return true;
  }

  navigateWorkspace(view, cx) {
    if (view === "queue") { if (!this.queueMode) this.openQueue(cx); }
    else { if (this.queueMode) this.leaveQueue(cx); this.workspaceView = view; this.keyboardMode = false; }
    cx.notify();
  }

  workspaceHeader(cx, t) {
    const control = (id, label, action, selected = false) => div().id(id).role("button").accessibility_label(label)
      .px(8).py(5).rounded(5).cursor_pointer().when(selected, e => e.bg(t.hover).font_semibold())
      .when(this.navigationTarget === id, e => e.bg(t.hover))
      .hover(e => e.bg(t.hover)).on_click(this.registerAction(id, label, action, this.headerTargets)).child(label);
    const queueID = this.queue.jobs.data?.queue_identity || compatibility.status?.queue_identity;
    const view = this.queueMode ? "queue" : this.workspaceView;
    return v_flex().id("ocq-workspace-header").flex_none().p(10).gap(5).border_b(1).border_color(t.border)
      .child(div().font_semibold().child(`OCQ · ${this.machine ?? "This computer"}${this.machine !== null ? " · Remote read-only" : ""}`))
      .child(h_flex().gap(2).children(["overview", "projects", "queue", "inbox"].map(name => control(`ocq-workspace-${name}`, name[0].toUpperCase() + name.slice(1), cx => this.navigateWorkspace(name, cx), view === name))))
      .child(div().text_size(12).text_color(["waiting", "reading"].includes(this.updateHealth.state) ? t.muted : t.blocked).child(this.updateHealth.state === "waiting" ? "Automatic change waiting · stored observations" : this.updateHealth.state === "missing" ? "No existing queue · automatic creation waiting" : `${this.updateHealth.state.replace(/_/g, " ")} · stored observations may be stale`))
      .when(Boolean(this.observationReset), e => e.child(div().text_size(12).text_color(t.blocked).child("Prior action authority suspended. Private reviews/journals retained separately; only freshly read projections are shown. Quiesce and reopen a fresh selected context before a new review.")))
      .child(control("ocq-context-details", this.contextDetails ? "Hide context details" : "Context details", cx => { this.contextDetails = !this.contextDetails; cx.notify(); }))
      .when(this.contextDetails, e => e.child(v_flex().gap(4)
        .child(div().text_size(12).text_color(t.muted).child(`Space: ${this.spaceID || "not selected"}\nFolder: ${this.folder || "not selected"}\nQueue: ${queueID || "identity not observed"}\nSnapshot revision: ${this.updates.revision || "not observed"}\nLatest snapshot read: ${this.updateHealth.readAt || "not yet read"}\nLatest project page read: ${this.projectsReadAt || "not yet read"}\n${this.updateHealth.reason || "No current read diagnostic."}`))
        .child(control("ocq-copy-update-diagnostics", "Copy update diagnostics", cx => cx.write_to_clipboard(JSON.stringify({ machine: this.machine, health: this.updateHealth, revision: this.updates.revision }))))
        .child(control("ocq-workspace-read-again", "Read again", cx => this.queueMode ? this.queue.refresh(this, cx) : this.refresh(cx)))))
      .when(["stale", "unavailable"].includes(this.updateHealth.state), e => e.child(div().text_size(12).text_color(t.blocked).child("Updates unavailable; displayed observations may be stale.")));
  }

  readFault(error, label, t) {
    const control = (id, text, action) => div().id(id).role("button").accessibility_label(text).px(8).py(4).cursor_pointer()
      .when(this.navigationTarget === id, e => e.bg(t.hover))
      .hover(e => e.bg(t.hover)).on_click(this.registerAction(id, text, action)).child(text);
    return v_flex().gap(4)
      .child(div().text_color(t.ansi1).child(`Cannot read ${label} on ${this.machine ?? "this computer"}. Displayed observations may be stale.`))
      .child(control(`ocq-diagnostics-${label}`, "Diagnostics", cx => { this.diagnostics = !this.diagnostics; cx.notify(); }))
      .when(this.diagnostics, e => e.child(div().child(error)).child(control(`ocq-copy-diagnostics-${label}`, "Copy diagnostics", cx => cx.write_to_clipboard(error))));
  }

  renderContent(cx) {
    const active = spaces().find((s) => s.active);
    const machine = active?.machine ?? null, folder = active?.folder ?? null;
    if ((active?.id ?? null) !== this.spaceID || machine !== this.machine || folder !== this.folder) {
      this.spaceID = active?.id ?? null;
      this.machine = machine;
      this.folder = folder;
      this.resetObservationVersions();
      this.selected = null;
      this.updateHealth = { state: "stored", reason: null };
      if (this.queueMode && active) this.queue.refresh(this, cx, false);
      this.refresh(cx, false);
    }
    const t = theme();
    this.updates.sync(cx);
    const button = (id, text, action) => div().id(id).role("button").accessibility_label(text).px(8).py(4).rounded(5)
      .text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
      .when(!enabledButton(id) || (this.queueMode && !this.queue.detailControlAllowed(id)), e => e.opacity(0.4))
      .when(this.navigationTarget === id, e => e.bg(t.hover))
      .on_click(!this.queueMode && enabledButton(id)
        ? this.registerAction(id, text, cx => { if (enabledButton(id)) action(cx); })
        : (_e, cx) => { cx.stop_propagation(); if (enabledButton(id) && (!this.queueMode || this.queue.detailControlAllowed(id))) { this.handle.focus(); action(cx); } }).child(text);
    if (this.queueMode && active) return this.queue.render(this, cx, t, button);
    if (this.worktreeCheck.screen) return this.worktreeCheck.render(this, cx, t, button);
    if (this.publication.screen) return this.publication.render(this, cx, t, button);
    if (this.intake.screen && !this.intake.authoritySuspended) {
      const i = this.intake, local = this.intakeCurrent(i.context), preview = i.preview;
      const stateText = { loading: "Checking journal…", idle: "No retained intake.", prepared: "Prepared, not dispatched. Preview the retained request.", armed: "Already claimed. A delayed dispatch may still start; do not send again.", attempted: "Outcome uncertain. A missing receipt does not authorize retry.", completed: "Exact receipt was matched. The request remains retained.", cancelled: "Cancelled before attempt. The request remains retained.", blocked: "Journal unavailable or blocked. Do not prepare another request." };
      if (local && preview && i.confirm) {
        return v_flex().id("ocq-intake-confirmation").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
          .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
          .child(div().flex_none().font_semibold().child("Confirm project intake"))
          .child(v_flex().id("ocq-intake-confirmation-content").flex_1().min_h(0).overflow_y_scroll().gap(10)
            .child(div().text_color(t.blocked).child(preview.kind === "submission" ? "This submits the exact retained request. An already-running dispatcher may start the queued work. A lost response is uncertain; do not retry." : "This registers the exact retained project request and can create or change a binding. A lost response is uncertain; do not retry."))
            .child(div().child(`Project: ${JSON.parse(preview.raw).project_id}\nFolder: ${i.context.folder}\nIntent: ${i.id}`))
            .when(preview.kind === "submission", (e) => e.child(div().text_size(12).child(`Binding revision: ${i.context.submission?.revision}\nRole: ${i.context.submission?.role}\nLane: ${i.context.submission?.lane}\nTarget job: ${i.context.submission?.target || "none"}\nTarget result SHA-256: ${i.context.submission?.digest || "none"}\nEnvelope SHA-256: ${createHash("sha256").update(preview.raw).digest("hex")}`)))
            .when(i.context.submission?.role === "review", (e) => e.child(div().child("Core checks the exact reviewer, supported OpenCode version, ordered read/glob/grep policy, deterministic prompt and target linkage at fresh intake and again before prompting. This UI does not substitute an agent, run a reviewer or authorize integration.")))
            .child(div().text_size(12).text_color(t.muted).child("Opening pages records navigation, not comprehension. The request is rechecked before a durable claim and one dispatch. Final confirmation requires a pointer press."))
            .when(i.working, (e) => e.child(div().child("Checking and claiming the retained request…")))
            .when(!i.working && this.canConfirmIntake(), (e) => e.child(div().id("ocq-intake-confirm-once").px(8).py(8).rounded(5).text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
              .on_mouse_down("left", (_e, cx) => this.confirmIntake(cx)).child("Press to confirm once")))
            .when(!i.working, (e) => e.child(button("ocq-intake-confirm-back", "Back to fields", (cx) => { i.confirm = false; cx.notify(); }))));
      }
      if (local && preview) {
        const page = preview.pages[i.page];
        return v_flex().id("ocq-intake-review").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
          .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
          .child(h_flex().flex_none().items_center().gap(8)
            .child(div().font_semibold().child(`${preview.draft ? "Draft" : preview.kind} · ${preview.view === "review" ? "field" : "raw"} ${i.page + 1}/${preview.pages.length}`))
            .child(div().flex_1()).child(button("ocq-intake-review-back", preview.draft ? "Edit form" : "Back", (cx) => { if (preview.draft) this.intakeEntry("form", cx); else { i.preview = null; cx.notify(); } })))
          .child(v_flex().id(`ocq-intake-page-${preview.view}-${i.page}`).flex_1().min_h(0).overflow_y_scroll().gap(8)
            .when(Boolean(preview.draft), (e) => e.child(div().text_size(12).child(`Generated by the core from this form; not retained or ${preview.kind === "submission" ? "submitted" : "registered"}. P prepares only after all fields have been opened. The retained request then needs its own review and confirmation.`)))
            .child(div().text_size(12).text_color(t.muted).child(preview.view === "raw" ? "Exact JSON string: join lines and pages to recover the retained text." : "Scroll to see the entire field. Controls and invisible characters are escaped. Opening pages is not confirmation."))
            .child(div().font_semibold().child(page.title + (page.parts > 1 ? ` · ${page.part}/${page.parts}` : "")))
            .child(div().child(page.text))
            .when(preview.draft && !i.working && preview.view === "review" && i.page === preview.review.length - 1 && preview.openedThrough === preview.review.length - 1, (e) => e.child(button("ocq-intake-draft-prepare", preview.kind === "submission" ? "Prepare these generated bytes (no submission yet)" : "Prepare these generated bytes (no registration yet)", (cx) => this.prepareIntake(cx, preview))))
            .when(Boolean(i.error), (e) => e.child(div().text_color(t.ansi1).child(i.error)))
            .when(Boolean(i.observation), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Last observation: ${i.observation}`)))
            .when(this.canConfirmIntake(), (e) => e.child(button("ocq-intake-review-confirm", "Review confirmation…", (cx) => { if (this.canConfirmIntake()) i.confirm = true; cx.notify(); })))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["prepared", "armed"].includes(i.state), (e) => e.child(button("ocq-intake-cancel", "Cancel before attempt", (cx) => this.intakeAction("cancel", cx))))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["attempted", "completed"].includes(i.state), (e) => e.child(button("ocq-intake-reconcile", "Check exact stored receipt", (cx) => this.intakeAction("reconcile", cx))))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["cancelled", "completed"].includes(i.state), (e) => e.child(button("ocq-intake-retire", "Retire retained intent", (cx) => this.intakeAction("retire", cx)))))
          .child(h_flex().flex_none().gap(8)
            .child(button("ocq-intake-previous", "Previous", (cx) => this.advanceIntakePage(-1, cx)))
            .child(button("ocq-intake-next", "Next", (cx) => this.advanceIntakePage(1, cx)))
            .child(div().flex_1())
            .when(Boolean(preview.review), (e) => e.child(button("ocq-intake-switch-view", preview.view === "review" ? "Exact raw" : "Fields", (cx) => {
              if (preview.view === "review") { preview.view = "raw"; preview.pages = preview.rawPages; }
              else if (preview.review) { preview.view = "review"; preview.pages = preview.review; }
              i.page = 0; cx.notify();
            }))))
          .child(div().flex_none().text_size(12).text_color(t.muted).child("←/→ pages · Esc close"));
      }
      return v_flex().id("ocq-intake").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
        .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .child(h_flex().items_center().gap(8).child(div().font_semibold().child("Project intake · prepare and preview"))
          .child(div().flex_1()).child(button("ocq-intake-close", "Close", (cx) => { i.screen = false; cx.notify(); })))
        .child(div().text_color(local ? t.muted : t.blocked).child(local ? `This computer · ${i.context.folder} · ${i.context.newProject ? "new project registration only" : (i.context.project || "no selected project")}` : "Local context unavailable or changed. Remote intake is unavailable."))
        .when(local && Boolean(i.context.reviewTarget), (e) => e.child(div().text_size(12).child(`Review submission only · binding revision ${i.context.revision}\nExact association: ${i.context.reviewTarget.association}\nExact target job: ${i.context.reviewTarget.job}\nExact result SHA-256: ${i.context.reviewTarget.digest}\nTarget source/verification: ${i.context.reviewTarget.source} / ${i.context.reviewTarget.verification}\nGuided form generates the core deterministic prompt from this stored result. Advanced raw must match the same exact target. Imported claims are not execution or test proof.`)))
        .when(local && !i.working, (e) => e.child(button("ocq-intake-context", i.context.newProject ? "Use selected project context" : "Register a new project…", (cx) => this.openIntake(cx, !i.context.newProject))))
        .when(local, (e) => e.child(div().child(stateText[i.state] || "Journal status unavailable.")))
        .when(Boolean(i.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${i.id}`)))
        .when(Boolean(i.error), (e) => e.child(div().text_color(t.ansi1).child(i.error)))
        .when(local && !i.working, (e) => e.child(button("ocq-intake-refresh", "Refresh journal", (cx) => this.refreshIntake(cx))))
        .when(local && i.state === "idle", (e) => e.child(v_flex().gap(6)
          .child(h_flex().gap(8)
            .when(!i.context.reviewTarget, (e) => e.child(button("ocq-intake-registration", i.kind === "registration" ? "Registration ✓" : "Registration", (cx) => { i.kind = "registration"; cx.notify(); })))
            .when(!i.context.newProject, (e) => e.child(button("ocq-intake-submission", i.kind === "submission" ? "Submission ✓" : "Submission", (cx) => { i.kind = "submission"; cx.notify(); }))))
          .child(h_flex().gap(8)
            .when(!i.working, (e) => e.child(button("ocq-intake-guided", i.entry === "form" ? "Guided form ✓" : "Guided form", (cx) => this.intakeEntry("form", cx))))
            .when(!i.working, (e) => e.child(button("ocq-intake-advanced", i.entry === "raw" ? "Advanced raw ✓" : "Advanced raw", (cx) => this.intakeEntry("raw", cx)))))
          .when(i.kind === "registration" && i.entry === "form", (e) => e.child(this.registrationForm.render(this, cx, t, button)))
          .when(i.kind === "submission" && i.entry === "form", (e) => e.child(this.submissionForm.render(this, cx, t, button)))
          .when(i.entry === "raw", (e) => e.child(v_flex().gap(6)
            .child(div().child("Advanced raw import: exact original JSON bytes are retained, never rebuilt by the form. Typing does not prepare or dispatch."))
            .child(Textarea.new(this.intakeInput).id("ocq-intake-input").accessibility_label("Exact intake JSON envelope").h(150).px(8).border_1().border_color(t.border).text_color(t.foreground))
            .when(!i.working, (e) => e.child(button("ocq-intake-prepare", "Prepare retained request", (cx) => this.prepareIntake(cx))))))))
        .when(local && !i.working && Boolean(i.id), (e) => e.child(button("ocq-intake-preview", "Preview exact retained request", (cx) => this.previewIntake(cx))))
        .child(div().text_size(12).text_color(t.muted).child("R refresh · P prepare · V preview · C cancel · T retire · ←/→ pages · Esc close"));
    }
    if (this.compose.screen && !this.compose.authoritySuspended) {
      const c = this.compose, local = this.composeLocal(), request = c.preview?.request;
      const previewPages = c.preview?.pages || [];
      const stateText = { loading: "Checking local journal…", idle: "No retained compose intent.", busy: "Another compose operation holds the journal. Refresh later.", prepared: "Prepared locally. A confirmation from another view may still be pending. Preview before confirming.", armed: "Send confirmed and durably claimed. A send process may be delayed; do not confirm again. You can revoke only before an attempt begins.", dispatch_intent: "Send outcome uncertain. Check the stored receipt; never resend automatically.", uncertain: "Send outcome uncertain. Check the stored receipt; never resend automatically.", completed: "Stored receipt matched. Retire to prepare another message.", cancelled: c.observation === "revoked_before_attempt" ? "Claim revoked before any attempt. A delayed send process will be refused. Retire to prepare another message." : "Prepared message cancelled. A delayed confirmation will be refused. Retire to prepare another message.", blocked: "Journal blocked or unverifiable. Do not send; investigate the local store." };
      return v_flex().id("ocq-compose").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
        .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .child(h_flex().items_center().gap(8).child(div().font_semibold().child("Compose · local only")).child(div().flex_1())
          .child(button("ocq-compose-close", "Close", (cx) => this.closeCompose(cx))))
        .child(div().text_color(local ? t.muted : t.blocked).child(local ? "This computer only · journal survives panel copies" : "Remote compose unavailable. Switch to a local space; nothing will be redirected."))
        .when(local, (e) => e.child(div().text_color(["blocked", "uncertain", "armed", "dispatch_intent"].includes(c.state) ? t.blocked : t.foreground).child(stateText[c.state] || "Compose state unavailable.")))
        .when(Boolean(c.error), (e) => e.child(div().text_color(t.ansi1).child(c.error)))
        .when(Boolean(c.observation), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Last observation: ${c.observation}`)))
        .when(Boolean(c.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${c.id}`)))
        .when(local && !c.working, (e) => e.child(button("ocq-compose-refresh", "Refresh journal status", (cx) => this.refreshCompose(cx))))
        .when(local && c.working, (e) => e.child(div().child("Checking the exact local context/journal…")))
        .when(local && !c.working && c.state === "idle" && Boolean(c.context && c.form && c.options), (e) => e.child(c.form.render(this, cx, t, button, c.options)))
        .when(local && c.state === "idle" && !c.context, (e) => e.child(div().text_color(t.muted).child("Close and choose a stored owner lane to compose lane.blocked or lane.assigned mail.")))
        .when(local && !c.working && !request && (["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))), (e) => e.child(button("ocq-compose-preview", "Preview exact retained message", (cx) => this.previewCompose(cx))))
        .when(Boolean(local && request), (e) => e.child(v_flex().flex_1().min_h(0).gap(6)
          .child(div().font_semibold().child(`Retained preview · ${c.page + 1}/${previewPages.length}`))
          .child(div().text_size(12).text_color(t.muted).child("Quoted fields escape controls and invisible characters; continuations preserve their order."))
          .child(div().flex_1().min_h(0).overflow_y_scroll().child(previewPages[c.page]))
          .when(!c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-previous", "Previous", (cx) => this.advanceComposePage(-1, cx)))
            .child(button("ocq-compose-next", "Next", (cx) => this.advanceComposePage(1, cx)))))
          .when(!c.working && !c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-cancel", "Cancel before attempt", (cx) => this.composeAction("cancel", cx)))
            .when(this.canReviewCompose(), (e) => e.child(button("ocq-compose-review", "Review send…", (cx) => { if (this.canReviewCompose()) c.confirm = true; cx.notify(); })))))
          .when(!c.working && c.state === "prepared" && c.preview?.state === "prepared" && c.confirm, (e) => e.child(v_flex().gap(6)
            .child(div().text_color(t.blocked).child("Send this exact retained message once? Every field was opened sequentially; opening is not proof of comprehension. Exact fields, links and the reviewed lane default are rechecked before the claim. A lost response remains uncertain."))
            .child(h_flex().gap(8).child(button("ocq-compose-back", "Back", (cx) => { c.confirm = false; cx.notify(); }))
              .child(button("ocq-compose-confirm", "Confirm send once", (cx) => this.composeAction("dispatch", cx))))))))
        .when(local && !c.working && ["dispatch_intent", "uncertain", "completed"].includes(c.state), (e) => e.child(button("ocq-compose-reconcile", "Check exact stored receipt", (cx) => this.composeAction("reconcile", cx))))
        .when(local && !c.working && ["completed", "cancelled"].includes(c.state), (e) => e.child(button("ocq-compose-retire", "Retire retained intent", (cx) => this.composeAction("retire", cx))))
        .when(local, (e) => e.child(div().text_size(12).text_color(t.muted).child("←/→ preview pages · R refresh · P prepare · V preview · C cancel · S review · Y confirm · K check · T retire")));
    }
    if (this.mailConfirmation && this.mailContextCurrent(this.mailConfirmation)) {
      const confirmation = this.mailConfirmation;
      return v_flex().id("ocq-archive-confirmation").size_full().min_h(0).p(14).gap(10).bg(t.background)
        .text_color(t.foreground).track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .child(div().font_semibold().child("Archive this message?"))
        .child(div().flex_1().min_h(0).overflow_y_scroll().child(`Archiving cannot be undone. The message remains retained and this does not release job capacity.\n\n${confirmation.subject}\nMessage: ${confirmation.id}\nProject: ${confirmation.project}\nMachine: ${confirmation.machine ?? "this computer"}\n\nConfirm only if this is the intended message. The stored state will be checked afterward; a client response alone is not confirmation.`))
        .child(div().text_size(12).text_color(t.muted).child("Y confirms archive · Esc or N cancels"))
        .child(h_flex().gap(8)
          .child(button("ocq-archive-cancel", "Cancel", (cx) => { this.mailConfirmation = null; cx.notify(); }))
          .child(button("ocq-archive-confirm", "Confirm archive", (cx) => this.confirmArchive(cx))));
    }
    const project = this.board?.project;
    const keyboardTarget = this.keyboardTarget;
    const pages = this.keyboardPages(keyboardTarget);
    const page = Math.min(this.keyboardPage, pages.length - 1);
    const observation = ["message", "mail-read", "mail-archive", "mail-check"].includes(keyboardTarget.kind) ? this.collections.inbox.readTimes[keyboardTarget.id]
      : keyboardTarget.kind === "result" ? this.collections.results.readTimes[keyboardTarget.id]
        : (keyboardTarget.kind === "manifest" || keyboardTarget.kind === "manifest-text") ? null
          : keyboardTarget.kind === "job" && this.detailJob === keyboardTarget.id ? this.detail?.read_at : project?.read_at;
    const selectedMessage = ["message", "mail-read", "mail-archive", "mail-check"].includes(keyboardTarget.kind)
      ? this.mailObservation?.id === keyboardTarget.id ? this.mailObservation.data : this.collections.inbox.items.find((m) => m.id === keyboardTarget.id) : null;
    return v_flex().id("ocq-panel").size_full().min_h(0).bg(t.background)
      .text_color(t.foreground).text_size(13).track_focus(this.handle)
      .on_key_down((e, cx) => this.key(e, cx))
      .child(div().px(14).py(5).text_size(12).text_color(t.muted)
        .child(`↑/↓ select · Enter activate · ${this.navigationLabel || "Workspace navigation"}`))
      .child(div().px(14).text_size(12).text_color(t.muted)
        .child(this.projectsReadAt ? `Latest project page read · ${this.projectsReadAt}` : "No project snapshot available yet"))
      .when(this.keyboardMode, (e) => e.child(v_flex().id("ocq-keyboard-inspection").flex_1().min_h(0).p(12).gap(8)
        .child(div().text_size(12).text_color(t.muted).child(`Inspection · ←/→ field ${page + 1}/${pages.length} · Esc board`))
        .child(h_flex().gap(8)
          .child(button("ocq-page-previous", "Previous", (cx) => { this.keyboardPage = Math.max(0, page - 1); cx.notify(); }))
          .child(button("ocq-page-next", "Next", (cx) => { this.keyboardPage = Math.min(pages.length - 1, page + 1); cx.notify(); }))
          .child(button("ocq-return-board", "Back to board", (cx) => { this.keyboardMode = false; cx.notify(); })))
        .when(keyboardTarget.kind === "manifest" && this.manifestDigest === keyboardTarget.id && this.manifest !== null,
          (e) => e.child(button("ocq-manifest-original", "View original manifest text", (cx) => this.openTarget("manifest-text", this.manifestDigest, cx))))
        .when(keyboardTarget.kind === "manifest-text", (e) => e.child(button("ocq-manifest-fields", "View manifest fields", (cx) => this.openTarget("manifest", this.manifestDigest, cx))))
         .when(Boolean(this.error), (e) => e.child(div().text_color(t.ansi1).child(`Cannot read stored projects on ${this.machine ?? "this computer"}. Displayed data may be stale.`)))
        .when(Boolean(selectedMessage && this.mailStatus(selectedMessage)), (e) => e.child(div().text_color(this.mailState(selectedMessage)?.status === "observed" ? t.muted : t.blocked).child(this.mailStatus(selectedMessage))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).read), (e) => e.child(button("ocq-inspection-mark-read", "Mark read", (cx) => this.requestMailAction(selectedMessage, false, cx))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).archive), (e) => e.child(button("ocq-inspection-archive", "Archive…", (cx) => this.requestMailAction(selectedMessage, true, cx))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).check), (e) => e.child(button("ocq-inspection-check-mail", "Check stored message", (cx) => this.checkMailAction(selectedMessage, cx))))
        .when(Boolean(project), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Project read ${project?.read_at || "unknown"} · item read ${observation || "not recorded"} · not live reconciliation`)))
        .child(div().font_semibold().child(pages[page].title))
        .child(div().flex_1().min_h(0).overflow_y_scroll().child(pages[page].text))))
      .when(!this.keyboardMode, (e) => e.child(v_flex().id("ocq-content").flex_1().min_h(0).overflow_y_scroll().p(12).gap(8)
        .when(!active, (e) => e.child(div().text_color(t.muted).child("Select a Herdr space to view OCQ on its machine.")))
         .when(Boolean(this.error), (e) => e
           .child(div().text_color(t.ansi1).child(`Cannot read stored projects on ${this.machine ?? "this computer"}. Check the selected machine and bridge. Displayed data may be stale.`))
           .child(button("ocq-project-diagnostics", this.diagnostics ? "Hide diagnostics" : "Diagnostics", cx => { this.diagnostics = !this.diagnostics; cx.notify(); }))
           .when(this.diagnostics, e => e.child(div().child(this.error)).child(button("ocq-project-diagnostics-copy", "Copy diagnostics", cx => cx.write_to_clipboard(this.error)))))
          .when(Boolean(active) && Boolean(this.projectPages[0].readAt) && !this.error && !this.projectsHasMore && this.projects.length === 0, (e) => e.child(div().text_color(t.muted).child("No projects registered in this queue. Queue contains standalone jobs separately.")))
          .when(Boolean(this.boardError), e => e.child(this.readFault(this.boardError, "selected project", t)))
         .when(this.workspaceView === "overview", e => e
           .child(div().font_semibold().child("Overview"))
           .child(div().child(this.selected ? `Selected project: ${project?.label || this.selected}` : "Choose a project in Projects to inspect work and inbox mail."))
           .when(Boolean(project), e => e.child(div().child(`Selected project · ${(project.jobs || []).length} loaded jobs · ${(project.jobs || []).filter(j => attention(j)).length} need inspection\nLoaded inbox items: ${this.collections.inbox.items.length} · loaded completion items: ${this.collections.results.items.length}`))))
         .when(this.workspaceView === "projects", e => e
           .when(this.composeLocal(), e => e.child(button("ocq-intake-open", "Project intake…", cx => this.openIntake(cx)))))
        .when(!project && this.projects.length > 1, (e) => e.child(div().child("Choose a project")))
         .when(this.workspaceView !== "overview" && this.projects.length > 0, (e) => e.child(v_flex().gap(3).child(div().font_semibold().child("Project")).children(this.projects.map((p) =>
          button(`ocq-project-${p.project_id}`, `${p.label} · ${p.project_id} · ${p.integrator_unread_mail_count} unread`, (cx) => this.select(p.project_id, cx))))))
          .when(this.workspaceView !== "overview" && this.projectsHasMore, e => e.child(button("ocq-project-pages-more", "Load next 20 projects", cx => { const last = this.projectPages[this.projectPages.length - 1]; if (last.next && !this.projectPages.some(page => page.start === last.next)) this.projectPages.push({ start: last.next }); cx.notify(); })))
          .when(Boolean(this.selected) && this.workspaceView !== "overview", (e) => e
           .child(div().font_semibold().child(project?.label ?? this.selected))
           .child(div().text_size(12).text_color(t.muted).child(`Project snapshot read · ${project?.read_at ?? "not available"}`))
           .when(this.workspaceView === "projects", e => e.child(h_flex().gap(2).children(["work", "results", "lanes", "binding"].map(view => button(`ocq-project-tab-${view}`, view[0].toUpperCase() + view.slice(1), cx => { this.projectView = view; cx.notify(); }).when(this.projectView === view, e => e.bg(t.hover).font_semibold())))))
           .when(this.workspaceView === "projects" && this.projectView === "binding", e => e
           .child(button("ocq-project-overview", "View project and binding", (cx) => this.openTarget("summary", undefined, cx)))
           .when(this.composeLocal(), e => e.child(button("ocq-intake-open", "Project intake / binding…", cx => this.openIntake(cx))))
           .when(Boolean(project?.current_binding?.handoff_manifest_digest), (e) => e.child(button("ocq-handoff", "Read handoff", (cx) => this.openTarget("manifest", project.current_binding.handoff_manifest_digest, cx)))))
            .when(Boolean(project) && this.workspaceView === "projects" && this.projectView === "work", e => e
          .when(this.composeLocal(), (e) => e.child(button("ocq-implementation-submit", "Submit implementation job…", (cx) => this.openImplementationIntake(cx))))
          .when(Boolean(project?.current_binding?.handoff_manifest_digest), (e) => e.child(button("ocq-handoff", "Read handoff", (cx) => this.openTarget("manifest", project.current_binding.handoff_manifest_digest, cx))))
          .child(div().pt(8).font_semibold().child("Work"))
          .child(div().text_size(12).text_color(t.muted).child("Request observations may be stale; their observation time is not recorded. Inspect a job for its recorded session and location."))
          .when((project?.jobs ?? []).length === 0, (e) => e.child(div().text_color(t.dim).child("No jobs.")))
          .children((project?.jobs ?? []).map((j) => v_flex().id(`ocq-job-${j.job_id}`).py(5).gap(2).border_b(1).border_color(t.border)
            .role("button").accessibility_label(`Inspect ${j.role} job ${j.job_id}, ${j.state}`).cursor_pointer().hover((s) => s.bg(t.hover))
            .when(this.navigationTarget === `ocq-job-${j.job_id}`, e => e.bg(t.hover))
            .on_click(this.registerAction(`ocq-job-${j.job_id}`, `Inspect ${j.role} job ${j.job_id}, ${j.state}`, cx => this.inspect(j.job_id, cx)))
            .child(div().child(`${j.role} · ${j.state} · ${j.outcome}`))
            .when(Boolean(attention(j)), (e) => e.child(div().text_size(12).text_color(t.blocked).child(attention(j))))
            .child(div().text_size(12).text_color(t.muted).child(j.job_id))
            .child(div().text_size(12).child(`Result: ${j.result_status} · Validation: ${j.validation?.status ?? "unknown"} · Review: ${j.review?.verdict ?? "not reviewed"}`))
            .when(Boolean(j.review?.stale_for_latest_result), (e) => e.child(div().text_color(t.blocked).child("Review is stale for the latest result")))
            .when(Boolean(j.reason), (e) => e.child(div().text_size(12).text_color(t.muted).child(j.reason)))
            .child(div().text_size(12).text_color(t.accent).child(this.detailJob === j.job_id ? "Close details" : "Inspect job"))
            .when(this.detailJob === j.job_id, (e) => e.child(v_flex().px(8).py(5).gap(3)
              .when(this.detailLoading, (e) => e.child(div().child("Loading job…")))
              .when(Boolean(this.detailError), (e) => e.child(this.readFault(this.detailError, "job detail", t)))
              .when(Boolean(this.detail), (e) => e
                .child(div().text_size(12).text_color(t.muted).child(`Job detail read · ${this.detail?.read_at ?? ""}`))
                .when(Boolean(attentionDetail(this.detail?.job)), (e) => e.child(v_flex().gap(2)
                  .child(div().text_color(t.blocked).child(attention(this.detail.job)))
                  .child(div().text_size(12).child("Observation time not recorded · current request status unknown"))
                  .child(div().text_size(12).child(`Recorded session ID: ${this.detail.job.agent?.session_id || "not recorded"}`))
                  .child(div().text_size(12).child(`Recorded location (worktree): ${this.detail.job.agent?.worktree || "not recorded"}`))
                  .child(div().text_size(12).text_color(t.muted).child(attentionDetail(this.detail.job).guidance))))
                .child(div().child(`Lane: ${this.detail?.lane_id || "not assigned"}`))
                .child(div().child(`Worktree: ${this.detail?.fsfork_path || "not recorded"}`))
                .child(div().child(`Integration: ${this.detail?.integration || "unknown"}`))
                .child(div().child(`Validation verified: ${this.detail?.validation?.verified ? "yes" : "no"}`))
                .child(button(`ocq-publication-${j.job_id}`, "Import / publish result manifest…", cx => this.publication.open(this, j.job_id, cx)))
                .when(j.role === "implementation", (e) => e.child(button(`ocq-capture-${j.job_id}`, "Prepare / publish machine snapshot…", cx => this.publication.open(this, j.job_id, cx, true))))
                .child(button(`ocq-project-job-fields-${j.job_id}`, "View all job fields", cx => this.openTarget("job", j.job_id, cx)))
                .when(Boolean(this.detail?.brief_digest), (e) => e.child(button(`ocq-project-job-brief-${j.job_id}`, "Read job brief", cx => this.openTarget("manifest", this.detail.brief_digest, cx))))
                .children((this.detail?.associations ?? []).map((a) => button(`ocq-project-association-${a.id}`, `Read ${a.kind} · ${a.verification} · ${a.manifest_digest}`, cx => this.openTarget("manifest", a.manifest_digest, cx))))
                .children((this.detail?.associations ?? []).filter((a) => a.kind === "result").map((a) => button(`ocq-worktree-associated-${a.id}`, `Verify owned worktree… · ${a.manifest_digest}`, cx => this.worktreeCheck.open(this, a, this.detail?.job?.agent?.worktree, cx))))
                .when(this.composeLocal(), (e) => e.children((this.detail?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-review-submit-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => this.openReviewIntake(a, cx))))))))))
           )
           .when(this.workspaceView === "projects" && this.projectView === "lanes", e => e
           .child(button("ocq-compose-open", "Compose / recover retained message…", cx => this.openCompose(null, cx)))
           .child(div().pt(8).font_semibold().child("Lanes"))
           .children(this.lanePages.filter(page => page.error).map(page => this.readFault(page.error, `lanes page ${page.start}`, t)))
           .when(Boolean(this.lanePages[0].readAt) && !this.lanePages[0].error && !(this.lanes || []).length, (e) => e.child(div().text_color(t.dim).child("No lanes.")))
           .children((this.lanes || []).map((lane) => button(`ocq-lane-${lane.lane_id}`, `${lane.role} · ${lane.job_state} · ${lane.lane_id}`, (cx) => this.openTarget("lane", lane.lane_id, cx))))
           .when(this.composeLocal() && this.compose.state === "idle", (e) => e.children((this.lanes || []).map((lane) => button(`ocq-compose-lane-${lane.lane_id}`, `Compose update · ${lane.lane_id}`, (cx) => this.openCompose(lane, cx)))))
           .when(Boolean(this.lanePages[this.lanePages.length - 1].hasMore), e => e.child(button("ocq-project-lanes-more", "Load next 20 lanes", cx => { const last = this.lanePages[this.lanePages.length - 1]; if (!this.lanePages.some(page => page.start === last.next)) this.lanePages.push({ start: last.next }); cx.notify(); })))
           )
           .when(this.workspaceView === "projects" && this.projectView === "results", e => e
           .child(div().pt(8).font_semibold().child("Completion history"))
           .child(div().text_size(12).text_color(t.muted).child(`Loaded pages of 20 · last page read ${this.collections.results.readAt || "not yet read"}; each snapshot batch is coherent, not one global total`))
          .when(Boolean(this.collections.results.recentError), (e) => e.child(this.readFault(this.collections.results.recentError, "recent results", t)))
          .when(Boolean(this.collections.results.error), (e) => e.child(this.readFault(this.collections.results.error, "results history", t)))
          .when(this.collections.results.loading, (e) => e.child(div().child("Loading results…")))
           .when(this.collections.results.loaded && !this.collections.results.error && !this.collections.results.items.length, (e) => e.child(div().text_color(t.dim).child("No completion events on loaded pages.")))
          .children(this.collections.results.items.map((r) => v_flex().gap(2)
            .child(button(`ocq-result-${r.event.id}`, `${r.role} · historical ${r.event.outcome} · current ${r.current_job.state} · ${r.job_id}`, (cx) => this.openTarget("result", r.event.id, cx)))
            .children((r.result_summary?.associations ?? []).map((a) => button(`ocq-result-manifest-${a.id}`, `Read ${a.kind} · ${a.verification}`, (cx) => this.openTarget("manifest", a.manifest_digest, cx))))
            .children((r.result_summary?.associations ?? []).filter((a) => a.kind === "result").map((a) => button(`ocq-result-check-${a.id}`, `Verify owned worktree… · ${a.manifest_digest}`, (cx) => this.worktreeCheck.open(this, a, r.current_job.agent?.worktree, cx))))
            .when(this.composeLocal(), (e) => e.children((r.result_summary?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-result-review-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => this.openReviewIntake(a, cx)))))))
          .when(this.collections.results.hasMore, (e) => e.child(button("ocq-results-more", "Load more results", (cx) => this.loadCollection("results", false, cx))))
            )
           .when(this.workspaceView === "inbox", e => e
          .child(div().pt(8).font_semibold().child("Inbox · all recipients"))
           .child(div().text_size(12).text_color(t.muted).child(`Loaded pages of 20 · last page read ${this.collections.inbox.readAt || "not yet read"}; includes read and archived mail, selected project only`))
          .when(Boolean(this.collections.inbox.recentError), (e) => e.child(this.readFault(this.collections.inbox.recentError, "recent inbox mail", t)))
          .when(Boolean(this.collections.inbox.error), (e) => e.child(this.readFault(this.collections.inbox.error, "inbox history", t)))
          .when(this.collections.inbox.loading, (e) => e.child(div().child("Loading inbox…")))
           .when(this.collections.inbox.loaded && !this.collections.inbox.error && !this.collections.inbox.items.length, (e) => e.child(div().text_color(t.dim).child("No messages on loaded pages.")))
          .children(this.collections.inbox.items.map((m) => v_flex().id(`ocq-message-${m.id}`).py(4).gap(2).border_b(1).border_color(t.border)
             .child(button(`ocq-read-${m.id}`, `${m.subject} · ${m.archived_at ? "archived" : m.read_at ? "read" : "unread"}`, (cx) => this.openTarget("message", m.id, cx)))
            .child(div().text_size(12).text_color(t.muted).child(`${m.kind} · ${m.sender} → ${m.recipient}`))
            .when(Boolean(this.mailStatus(m)), (e) => e.child(div().text_size(12).text_color(this.mailState(m)?.status === "observed" ? t.muted : t.blocked).child(this.mailStatus(m))))
            .when(this.mailOptions(m).read, (e) => e.child(button(`ocq-mark-read-${m.id}`, "Mark read", (cx) => this.requestMailAction(m, false, cx))))
            .when(this.mailOptions(m).archive, (e) => e.child(button(`ocq-archive-${m.id}`, "Archive…", (cx) => this.requestMailAction(m, true, cx))))
            .when(this.mailOptions(m).check, (e) => e.child(button(`ocq-check-mail-${m.id}`, "Check stored message", (cx) => this.checkMailAction(m, cx))))
            .when(Boolean(m.manifest_digest), (e) => e.child(button(`ocq-attachment-${m.id}`, "Read attached manifest", (cx) => this.openTarget("manifest", m.manifest_digest, cx))))))
          .when(this.collections.inbox.hasMore, (e) => e.child(button("ocq-inbox-more", "Load more mail", (cx) => this.loadCollection("inbox", false, cx))))
            )
          .child(div().pt(8).text_size(12).text_color(t.muted).child("Stored observations only. Imported claims are unverified; worktree drift is checked only by explicit Verify filesystem now. Opening mail does not mark it read.")))));
  }
}
