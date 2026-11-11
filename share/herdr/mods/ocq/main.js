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

function emptyCollection() {
  return { items: [], history: [], recent: [], readTimes: {}, cursor: "0", refreshCursor: "0", readAt: null, recentReadAt: null, loaded: false, loading: false, recentLoading: false, refreshingHistory: false, hasMore: false, error: null, recentError: null };
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
    this.worktreeCheck = new WorktreeVerification();
    this.worktreeCheck.reset();
    this.publication = new ResultPublication();
    this.queueMode = false;
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
    this.refresh(cx);
    this.refreshCompose(cx);
    cx.timer.every(15000, (cx) => { this.refresh(cx); this.refreshCompose(cx); });
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
    this.mailConfirmation = null;
    this.compose = { ...this.compose, screen: false, preview: null, context: null, form: null, options: null, confirm: false, working: false };
    this.intake = { ...this.intake, screen: false, preview: null, context: null, confirm: false, working: false };
  }

  refresh(cx, notify = true) {
    if (this.queueMode) return;
    if (this.loading) return;
    const active = spaces().find((s) => s.active);
    if (!active) {
      this.error = null;
      if (notify) cx.notify();
      return;
    }
    if (active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) { if (notify) cx.notify(); return; }
    const generation = ++this.generation;
    const machine = this.machine;
    const folder = this.folder;
    this.loading = true;
    this.error = null;
    cx.spawn(async (cx) => {
      try {
        const list = await exec("ocq-herdr", ["projects"], { on: machine });
        if (list.code !== 0) throw new Error(list.stderr || "Cannot read projects");
        const projectList = JSON.parse(list.stdout);
        const projects = projectList.projects;
        const matching = projects.filter((p) => p.repo_root === folder);
        const selected = projects.some((p) => p.project_id === this.selected)
          ? this.selected : matching.length === 1 ? matching[0].project_id
            : projects.length === 1 ? projects[0].project_id : null;
        let board = null;
        if (selected) {
          const result = await exec("ocq-herdr", ["board", selected], { on: machine });
          if (result.code !== 0) throw new Error(result.stderr || "Cannot read project board");
          board = JSON.parse(result.stdout);
        }
        if (generation !== this.generation) return;
        if (selected !== this.selected) this.clearInspection();
        this.projects = projects;
        this.projectsReadAt = projectList.read_at;
        this.selected = selected;
        this.board = board;
        if (selected) {
          for (const kind of ["inbox", "results"]) {
            const collection = this.collections[kind];
            if (!collection.recentLoading) this.loadRecent(kind, cx);
            if (!collection.loading && !collection.loaded) this.loadCollection(kind, false, cx);
            else if (!collection.loading && collection.loaded && collection.history.length) this.refreshHistory(kind, cx);
          }
        }
      } catch (e) {
        if (generation === this.generation) {
          this.error = String(e);
        }
      } finally {
        if (generation === this.generation) this.loading = false;
        cx.notify();
      }
    });
    if (notify) cx.notify();
  }

  itemID(kind, item) { return kind === "inbox" ? item.id : item.event.id; }

  combineCollection(kind, collection) {
    const latest = new Set(collection.recent.map((item) => this.itemID(kind, item)));
    const items = collection.recent.slice().reverse().concat(collection.history.filter((item) => !latest.has(this.itemID(kind, item))));
    // Keep an item being inspected reachable if it rolls out of the recent window.
    if (this.keyboardMode && this.keyboardTarget.id && !items.some((item) => this.itemID(kind, item) === this.keyboardTarget.id)) {
      const selected = collection.items.find((item) => this.itemID(kind, item) === this.keyboardTarget.id);
      if (selected) items.push(selected);
    }
    collection.items = items;
    const retained = new Set(items.map((item) => this.itemID(kind, item)));
    for (const id of Object.keys(collection.readTimes)) if (!retained.has(id)) delete collection.readTimes[id];
  }

  loadRecent(kind, cx) {
    const active = spaces().find((s) => s.active);
    if (!this.selected || !active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) return;
    const collection = this.collections[kind], projectID = this.selected, machine = this.machine;
    if (collection.recentLoading) return;
    collection.recentLoading = true;
    cx.spawn(async (cx) => {
      try {
        const response = await exec("ocq-herdr", [`recent-${kind}`, projectID], { on: machine });
        if (response.code !== 0) throw new Error(response.stderr || `Cannot read recent ${kind}`);
        const page = JSON.parse(response.stdout), items = kind === "inbox" ? page.messages : page.items;
        if (!Array.isArray(items) || !page.read_at) throw new Error("Invalid recent collection response");
        if (this.collections[kind] !== collection) return;
        const previous = new Map(collection.items.map((item) => [this.itemID(kind, item), item]));
        collection.recent = items.map((item) => {
          const id = this.itemID(kind, item);
          return readOrder(collection.readTimes[id]) > readOrder(page.read_at) && previous.has(id) ? previous.get(id) : item;
        });
        collection.recentReadAt = page.read_at;
        collection.recentError = null;
        const updates = new Map(items.map((item) => [this.itemID(kind, item), item]));
        collection.history = collection.history.map((item) => {
          const id = this.itemID(kind, item);
          return updates.has(id) && readOrder(collection.readTimes[id]) <= readOrder(page.read_at) ? updates.get(id) : item;
        });
        for (const item of items) {
          const id = this.itemID(kind, item);
          if (readOrder(collection.readTimes[id]) < readOrder(page.read_at)) collection.readTimes[id] = page.read_at;
        }
        this.combineCollection(kind, collection);
      } catch (e) {
        if (this.collections[kind] === collection) collection.recentError = String(e);
      } finally {
        if (this.collections[kind] === collection) collection.recentLoading = false;
        cx.notify();
      }
    });
  }

  // Refresh one already-loaded historical page per cycle; recent activity has
  // its own bounded read and never waits for this scan to reach the tail.
  refreshHistory(kind, cx) {
    const collection = this.collections[kind];
    if (collection.refreshingHistory || !collection.history.length) return;
    const projectID = this.selected, machine = this.machine, cursor = collection.refreshCursor;
    const lastID = this.itemID(kind, collection.history[collection.history.length - 1]);
    collection.refreshingHistory = true;
    cx.spawn(async (cx) => {
      try {
        const response = await exec("ocq-herdr", [kind, projectID, cursor], { on: machine });
        if (response.code !== 0) throw new Error(response.stderr || `Cannot refresh ${kind}`);
        const page = JSON.parse(response.stdout), items = kind === "inbox" ? page.messages : page.items;
        if (!Array.isArray(items) || typeof page.has_more !== "boolean" || typeof page.cursor !== "string" || !page.read_at) throw new Error("Invalid history refresh response");
        if (this.collections[kind] !== collection) return;
        const updates = new Map(items.map((item) => [this.itemID(kind, item), item]));
        collection.history = collection.history.map((item) => {
          const id = this.itemID(kind, item);
          return updates.has(id) && readOrder(collection.readTimes[id]) <= readOrder(page.read_at) ? updates.get(id) : item;
        });
        collection.recent = collection.recent.map((item) => {
          const id = this.itemID(kind, item);
          return updates.has(id) && readOrder(collection.readTimes[id]) <= readOrder(page.read_at) ? updates.get(id) : item;
        });
        for (const item of items) {
          const id = this.itemID(kind, item);
          if (readOrder(collection.readTimes[id]) < readOrder(page.read_at)) collection.readTimes[id] = page.read_at;
        }
        collection.refreshCursor = !page.has_more || page.cursor === cursor || updates.has(lastID) ? "0" : page.cursor;
        collection.error = null;
        this.combineCollection(kind, collection);
      } catch (e) {
        if (this.collections[kind] === collection) collection.error = String(e);
      } finally {
        if (this.collections[kind] === collection) collection.refreshingHistory = false;
        cx.notify();
      }
    });
  }

  loadCollection(kind, reset, cx) {
    const active = spaces().find((s) => s.active);
    if (!this.selected || !active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) return;
    if (reset) this.collections[kind] = emptyCollection();
    const collection = this.collections[kind];
    if (reset) this.loadRecent(kind, cx);
    if (collection.loading) return;
    const projectID = this.selected, machine = this.machine;
    collection.loading = true;
    collection.error = null;
    cx.spawn(async (cx) => {
      try {
        const response = await exec("ocq-herdr", [kind, projectID, collection.cursor], { on: machine });
        if (response.code !== 0) throw new Error(response.stderr || `Cannot read ${kind}`);
        const page = JSON.parse(response.stdout);
        if (typeof page.has_more !== "boolean") throw new Error("Update ocq-herdr on this machine to read paged data safely.");
        if (this.collections[kind] !== collection) return;
        const items = kind === "inbox" ? page.messages : page.items;
        const updates = new Map(items.map((item) => [this.itemID(kind, item), item]));
        collection.recent = collection.recent.map((item) => {
          const id = this.itemID(kind, item);
          return updates.has(id) && readOrder(collection.readTimes[id]) <= readOrder(page.read_at) ? updates.get(id) : item;
        });
        collection.history = collection.history.concat(items.map((item) => {
          const id = this.itemID(kind, item);
          return readOrder(collection.readTimes[id]) > readOrder(page.read_at) ? collection.items.find((existing) => this.itemID(kind, existing) === id) || item : item;
        }));
        this.combineCollection(kind, collection);
        for (const item of items) {
          const id = this.itemID(kind, item);
          if (readOrder(collection.readTimes[id]) < readOrder(page.read_at)) collection.readTimes[id] = page.read_at;
        }
        collection.cursor = page.cursor;
        collection.hasMore = page.has_more;
        collection.readAt = page.read_at;
        collection.loaded = true;
      } catch (e) {
        if (this.collections[kind] === collection) collection.error = String(e);
      } finally {
        if (this.collections[kind] === collection) collection.loading = false;
        cx.notify();
      }
    });
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
    return Boolean(active && active.machine === null && this.machine === null && active.id === this.spaceID && active.folder === this.folder);
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
    return this.compose === c && c.screen && c.context === context && this.composeContextCurrent(context);
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
    const current = () => this.compose === c && c.screen === scope.screen && this.composeLocal() && scope.space === this.spaceID && scope.folder === this.folder && scope.project === this.selected;
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
    const c = this.compose = { ...previous, working: false, preview: null, form: null, options: null, context: null };
    c.screen = true; c.confirm = false; c.error = null;
    if (lane && c.state === "idle") {
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
    const current = () => this.compose === c && c.screen && c.id === id && c.state === initialState && c.recoveryState === initialRecovery && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected;
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
    const current = () => this.compose === c && c.screen && this.composeLocal() && scope.space === this.spaceID && scope.folder === this.folder && scope.project === this.selected;
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
    return Boolean(context && this.intake.context === context && this.intake.screen && this.composeLocal() && context.machine === this.machine && context.space === this.spaceID && context.folder === this.folder && context.selection === this.selected);
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
    const i = this.intake = { ...this.intake, working: false };
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
    const projects = this.projects.length > 1 ? this.projects.map((p) => ({
      kind: "project", id: p.project_id, label: `Project: ${p.label} · ${p.project_id}`,
    })) : [];
    const jobs = (this.board?.project?.jobs ?? []).map((j) => ({
      kind: "job", id: j.job_id, label: `Job: ${j.role} · ${attention(j) || j.state} · ${j.job_id}`,
    }));
    const publications = (this.board?.project?.jobs ?? []).map((j) => ({ kind: "publication", id: j.job_id, label: `Import / publish manifest · ${j.role} · ${j.job_id}` }));
    const captures = (this.board?.project?.jobs ?? []).filter((j) => j.role === "implementation").map((j) => ({ kind: "capture", id: j.job_id, label: `Prepare / publish machine snapshot · ${j.job_id}` }));
    const messages = this.collections.inbox.items.flatMap((m) => {
      const targets = [{ kind: "message", id: m.id, label: `Message: ${m.subject} · ${m.id}` }];
      const options = this.mailOptions(m);
      if (options.read) targets.push({ kind: "mail-read", id: m.id, label: `Mark read: ${m.subject} · ${m.id}` });
      if (options.archive) targets.push({ kind: "mail-archive", id: m.id, label: `Archive: ${m.subject} · ${m.id}` });
      if (options.check) targets.push({ kind: "mail-check", id: m.id, label: `Check outcome: ${m.subject} · ${m.id}` });
      return targets;
    });
    const results = this.collections.results.items.map((r) => ({
      kind: "result", id: r.event.id, label: `Completion: ${r.role} · ${r.event.outcome} · ${r.job_id}`,
    }));
    const lanes = (this.board?.project?.lanes ?? []).map((lane) => ({
      kind: "lane", id: lane.lane_id, label: `Lane: ${lane.role} · ${lane.lane_id}`,
    }));
    const composeLanes = (this.board?.project?.lanes ?? []).map((lane) => ({ kind: "compose-lane", id: lane.lane_id, label: `Compose update from lane ${lane.lane_id}` }));
    const manifests = [];
    const addManifest = (id, label) => {
      if (id && !manifests.some((m) => m.id === id)) manifests.push({ kind: "manifest", id, label: `Manifest: ${label} · ${id}` });
    };
    addManifest(this.board?.project?.current_binding?.handoff_manifest_digest, "handoff");
    addManifest(this.detail?.brief_digest, "brief");
    for (const a of this.detail?.associations ?? []) addManifest(a.manifest_digest, `${a.kind} · ${a.verification}`);
    const checks = (this.detail?.associations ?? []).filter((a) => a.kind === "result").map((a) => ({ kind: "worktree-check", id: a.id, label: `Verify owned worktree · ${a.manifest_digest}`, association: a, worktree: this.detail?.job?.agent?.worktree }));
    const reviews = (this.detail?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => ({ kind: "review-submit", id: a.id, label: `Submit review job for exact result · ${a.manifest_digest}`, association: a }));
    for (const m of this.collections.inbox.items) addManifest(m.manifest_digest, `message attachment · ${m.subject}`);
    for (const r of this.collections.results.items) {
      for (const a of r.result_summary?.associations ?? []) addManifest(a.manifest_digest, `${a.kind} · ${a.verification}`);
    }
    if (this.manifest !== null && this.manifestDigest) manifests.push({
      kind: "manifest-text", id: this.manifestDigest, label: `Original manifest text · ${this.manifestDigest}`,
    });
    const controls = [];
    for (const kind of ["inbox", "results"]) {
      controls.push({ kind: "reload", id: kind, label: `Reload ${kind}` });
      if (this.collections[kind].hasMore) controls.push({ kind: "more", id: kind, label: `Load more ${kind}` });
    }
    const summary = this.board ? [{ kind: "summary", label: "Project overview and binding" }] : [];
    return [{ kind: "refresh", label: "Refresh board" }, { kind: "queue", label: "Read queue on selected machine" }, { kind: "compose", label: "Compose and recovery" }, { kind: "intake", label: "Prepare project intake" }, ...(this.composeLocal() && this.selected ? [{ kind: "implementation-submit", label: "Submit implementation job" }] : []), ...projects, ...summary, ...jobs, ...publications, ...captures, ...lanes, ...composeLanes, ...manifests, ...checks, ...reviews, ...controls, ...results, ...messages];
  }

  key(e, cx) {
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    if (this.intake.screen) {
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
    if (this.compose.screen) {
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
    if (this.mailConfirmation) {
      if (e.key === "escape" || e.key === "n") { this.mailConfirmation = null; cx.notify(); }
      else if (e.key === "y" && !e.is_held) this.confirmArchive(cx);
      cx.stop_propagation();
      return;
    }
    const targets = this.targets();
    let index = targets.findIndex((t) => t.kind === this.keyboardTarget.kind && t.id === this.keyboardTarget.id);
    if (index < 0) index = 0;
    if (e.key === "down" || e.key === "up") {
      index = (index + (e.key === "down" ? 1 : targets.length - 1)) % targets.length;
      this.keyboardTarget = { kind: targets[index].kind, id: targets[index].id };
      this.keyboardPage = 0;
      this.keyboardMode = true;
    } else if (e.key === "escape") this.keyboardMode = false;
    else if (e.key === "left") this.keyboardPage = Math.max(0, this.keyboardPage - 1);
    else if (e.key === "right") this.keyboardPage = Math.min(this.keyboardPages(targets[index]).length - 1, this.keyboardPage + 1);
    else if (e.key === "enter" || e.key === "space") {
      this.keyboardMode = true;
      const target = targets[index];
      if (target.kind === "refresh") this.refresh(cx);
      else if (target.kind === "queue") this.openQueue(cx);
      else if (target.kind === "compose") this.openCompose(null, cx);
      else if (target.kind === "intake") this.openIntake(cx);
      else if (target.kind === "implementation-submit") this.openImplementationIntake(cx);
      else if (target.kind === "compose-lane") this.openCompose((this.board?.project?.lanes ?? []).find((lane) => lane.lane_id === target.id), cx);
      else if (target.kind === "project") this.select(target.id, cx);
      else if (target.kind === "job") this.inspect(target.id, cx);
      else if (target.kind === "publication") this.publication.open(this, target.id, cx);
      else if (target.kind === "capture") this.publication.open(this, target.id, cx, true);
      else if (target.kind === "manifest") this.inspectManifest(target.id, cx);
      else if (target.kind === "worktree-check") this.worktreeCheck.open(this, target.association, target.worktree, cx);
      else if (target.kind === "review-submit") this.openReviewIntake(target.association, cx);
      else if (target.kind === "more" || target.kind === "reload") this.loadCollection(target.id, target.kind === "reload", cx);
      else if (target.kind === "mail-read" || target.kind === "mail-archive") {
        const message = this.collections.inbox.items.find((m) => m.id === target.id);
        if (message) this.requestMailAction(message, target.kind === "mail-archive", cx);
      } else if (target.kind === "mail-check") {
        const message = this.collections.inbox.items.find((m) => m.id === target.id);
        if (message) this.checkMailAction(message, cx);
      }
      this.keyboardPage = 0;
    } else return;
    cx.stop_propagation();
    cx.notify();
  }

  openTarget(kind, id, cx) {
    this.keyboardTarget = { kind, id };
    this.keyboardPage = 0;
    this.keyboardMode = true;
    this.handle.focus();
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
      return this.mailActions[key] = { status: "uncertain", mode: saved.mode, sequence: saved.sequence };
    } catch (_e) {
      return this.mailActions[key] = { status: "blocked", mode: "unknown" };
    }
  }

  mailOptions(message) {
    const state = this.mailState(message);
    if (state && state.status !== "observed") return { check: state.status === "uncertain", read: false, archive: false };
    const validSequence = typeof message.sequence_text === "string" && /^[0-9]+$/.test(message.sequence_text);
    const read = message.read_at || state?.message?.read_at;
    const archived = message.archived_at || state?.message?.archived_at;
    return { read: validSequence && !read, archive: validSequence && Boolean(read) && !archived, check: false };
  }

  mailStatus(message) {
    const state = this.mailState(message);
    if (!state) return "";
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
    return { space: active.id, machine: active.machine, folder: active.folder, project: this.selected,
      id: message.id, sequence: message.sequence_text, subject: message.subject,
      key: this.mailKey(active.machine, this.selected, message.id) };
  }

  mailContextCurrent(context) {
    const active = spaces().find((s) => s.active);
    return Boolean(active && active.id === context.space && active.machine === context.machine && active.folder === context.folder && this.spaceID === context.space && this.selected === context.project);
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
          localStorage.removeItem(context.key);
          delete this.mailActions[context.key];
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
    if (!context || !state || state.status !== "uncertain" || state.sequence !== context.sequence) return;
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
        if (this.mailContextCurrent(context)) {
          const inbox = this.collections.inbox;
          inbox.history = inbox.history.map((m) => m.id === item.id ? item : m);
          inbox.recent = inbox.recent.map((m) => m.id === item.id ? item : m);
          this.combineCollection("inbox", inbox);
          inbox.readTimes[item.id] = observed.read_at;
        }
      }
    } catch (_e) { state.status = "uncertain"; }
    cx.notify();
  }

  // A field-labelled document keeps each selected item reachable even when its
  // board row is offscreen. Long values continue at word/line boundaries.
  keyboardPages(target) {
    let value;
    if (target.kind === "project") value = this.projects.find((p) => p.project_id === target.id);
    else if (target.kind === "summary") value = this.board?.project;
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
      value = this.manifestDigest === target.id ? this.manifest : "Read the manifest first.";
    } else if (target.kind === "message") value = this.collections.inbox.items.find((m) => m.id === target.id);
    else if (target.kind === "mail-read") value = "Activate to mark this message read. Opening it does not mark it read. Its stored state is checked afterward.";
    else if (target.kind === "mail-archive") value = "Activate to review a separate archive confirmation. Archiving cannot be undone; the message remains retained.";
    else if (target.kind === "mail-check") value = "Activate to read back the exact message. An absent change does not prove a delayed request cannot still complete.";
    else if (target.kind === "result") value = this.collections.results.items.find((r) => r.event.id === target.id);
    else if (target.kind === "lane") value = this.board?.project?.lanes?.find((lane) => lane.lane_id === target.id);
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
    const generation = ++this.detailGeneration;
    const projectID = this.selected, machine = this.machine;
    this.detailJob = jobID;
    this.detail = null;
    this.detailError = null;
    this.detailLoading = true;
    cx.spawn(async (cx) => {
      try {
        const result = await exec("ocq-herdr", ["job", projectID, jobID], { on: machine });
        if (result.code !== 0) throw new Error(result.stderr || "Cannot inspect job");
        const detail = JSON.parse(result.stdout);
        if (generation === this.detailGeneration) this.detail = detail;
      } catch (e) {
        if (generation === this.detailGeneration) this.detailError = String(e);
      } finally {
        if (generation === this.detailGeneration) this.detailLoading = false;
        cx.notify();
      }
    });
    cx.notify();
  }

  inspectManifest(digest, cx) {
    const active = spaces().find((s) => s.active);
    if (!active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) { cx.notify(); return; }
    const generation = ++this.manifestGeneration;
    const projectID = this.selected, machine = this.machine;
    this.manifestDigest = digest;
    this.manifest = null;
    this.manifestError = null;
    this.manifestLoading = true;
    cx.spawn(async (cx) => {
      try {
        const result = await exec("ocq-herdr", ["manifest", projectID, digest], { on: machine });
        if (result.code !== 0) throw new Error(result.stderr || "Cannot read manifest");
        const manifest = JSON.parse(result.stdout);
        if (generation === this.manifestGeneration) this.manifest = manifest;
      } catch (e) {
        if (generation === this.manifestGeneration) this.manifestError = String(e);
      } finally {
        if (generation === this.manifestGeneration) this.manifestLoading = false;
        cx.notify();
      }
    });
    cx.notify();
  }

  render(cx) {
    compatibility.observe();
    const content = this.renderContent(cx);
    return v_flex().size_full().min_h(0).child(compatibility.notice(cx, theme())).child(div().flex_1().min_h(0).child(content));
  }

  renderContent(cx) {
    const active = spaces().find((s) => s.active);
    const machine = active?.machine ?? null, folder = active?.folder ?? null;
    if ((active?.id ?? null) !== this.spaceID || machine !== this.machine || folder !== this.folder) {
      this.spaceID = active?.id ?? null;
      this.machine = machine;
      this.folder = folder;
      this.generation++;
      this.loading = false;
      this.selected = null;
      this.projects = [];
      this.projectsReadAt = null;
      this.board = null;
      this.keyboardTarget = { kind: "refresh" };
      this.keyboardPage = 0;
      this.clearInspection();
      this.queue.reset();
      if (this.queueMode && active) this.queue.refresh(this, cx, false);
      this.refresh(cx, false);
    }
    const t = theme();
    const button = (id, text, action) => div().id(id).role("button").accessibility_label(text).px(8).py(4).rounded(5)
      .text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
      .when(!enabledButton(id), e => e.opacity(0.4))
      .on_click((_e, cx) => { if (enabledButton(id)) action(cx); }).child(text);
    if (this.queueMode && active) return this.queue.render(this, cx, t, button);
    if (this.worktreeCheck.screen) return this.worktreeCheck.render(this, cx, t, button);
    if (this.publication.screen) return this.publication.render(this, cx, t, button);
    if (this.intake.screen) {
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
    if (this.compose.screen) {
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
    if (this.mailConfirmation) {
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
    const targets = this.targets();
    const keyboardTarget = targets.find((target) => target.kind === this.keyboardTarget.kind && target.id === this.keyboardTarget.id) ?? targets[0];
    const pages = this.keyboardPages(keyboardTarget);
    const page = Math.min(this.keyboardPage, pages.length - 1);
    const observation = ["message", "mail-read", "mail-archive", "mail-check"].includes(keyboardTarget.kind) ? this.collections.inbox.readTimes[keyboardTarget.id]
      : keyboardTarget.kind === "result" ? this.collections.results.readTimes[keyboardTarget.id]
        : (keyboardTarget.kind === "manifest" || keyboardTarget.kind === "manifest-text") ? null
          : keyboardTarget.kind === "job" && this.detailJob === keyboardTarget.id ? this.detail?.read_at : project?.read_at;
    const selectedMessage = ["message", "mail-read", "mail-archive", "mail-check"].includes(keyboardTarget.kind)
      ? this.collections.inbox.items.find((m) => m.id === keyboardTarget.id) : null;
    return v_flex().id("ocq-panel").size_full().min_h(0).bg(t.background)
      .text_color(t.foreground).text_size(13).track_focus(this.handle)
      .on_key_down((e, cx) => this.key(e, cx))
      .child(h_flex().h(42).flex_none().px(14).gap(8).items_center().border_b(1).border_color(t.border)
        .child(div().font_semibold().child("OCQ"))
        .child(div().flex_1())
        .child(button("ocq-intake-open", "Project intake", (cx) => this.openIntake(cx)))
        .child(button("ocq-compose-open", "Compose", (cx) => this.openCompose(null, cx)))
        .child(button("ocq-refresh", this.loading ? "Refreshing…" : "Refresh", (cx) => this.refresh(cx))))
      .when(Boolean(active), (e) => e.child(button("ocq-open-queue", "Queue · jobs, logs and global notifications", (cx) => this.openQueue(cx))))
      .child(div().px(14).py(5).text_size(12).text_color(t.muted)
        .child(`↑/↓ select · Enter activate · ${keyboardTarget.label}`))
      .child(div().px(14).text_size(12).text_color(t.muted)
        .child(this.projectsReadAt ? `Project list read · ${this.projectsReadAt}` : this.loading ? "Loading stored projects…" : "No project snapshot available"))
      .when(this.keyboardMode, (e) => e.child(v_flex().id("ocq-keyboard-inspection").flex_1().min_h(0).p(12).gap(8)
        .child(div().text_size(12).text_color(t.muted).child(`Inspection · ←/→ field ${page + 1}/${pages.length} · Esc board`))
        .child(h_flex().gap(8)
          .child(button("ocq-page-previous", "Previous", (cx) => { this.keyboardPage = Math.max(0, page - 1); cx.notify(); }))
          .child(button("ocq-page-next", "Next", (cx) => { this.keyboardPage = Math.min(pages.length - 1, page + 1); cx.notify(); }))
          .child(button("ocq-return-board", "Back to board", (cx) => { this.keyboardMode = false; cx.notify(); })))
        .when(keyboardTarget.kind === "manifest" && this.manifestDigest === keyboardTarget.id && this.manifest !== null,
          (e) => e.child(button("ocq-manifest-original", "View original manifest text", (cx) => this.openTarget("manifest-text", this.manifestDigest, cx))))
        .when(keyboardTarget.kind === "manifest-text", (e) => e.child(button("ocq-manifest-fields", "View manifest fields", (cx) => this.openTarget("manifest", this.manifestDigest, cx))))
        .when(Boolean(this.error), (e) => e.child(div().text_color(t.ansi1).child(`Refresh failed; data may be stale: ${this.error}`)))
        .when(Boolean(selectedMessage && this.mailStatus(selectedMessage)), (e) => e.child(div().text_color(this.mailState(selectedMessage)?.status === "observed" ? t.muted : t.blocked).child(this.mailStatus(selectedMessage))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).read), (e) => e.child(button("ocq-inspection-mark-read", "Mark read", (cx) => this.requestMailAction(selectedMessage, false, cx))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).archive), (e) => e.child(button("ocq-inspection-archive", "Archive…", (cx) => this.requestMailAction(selectedMessage, true, cx))))
        .when(Boolean(selectedMessage && this.mailOptions(selectedMessage).check), (e) => e.child(button("ocq-inspection-check-mail", "Check stored message", (cx) => this.checkMailAction(selectedMessage, cx))))
        .when(Boolean(project), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Project read ${project?.read_at || "unknown"} · item read ${observation || "not recorded"} · not live reconciliation`)))
        .child(div().font_semibold().child(pages[page].title))
        .child(div().flex_1().min_h(0).overflow_y_scroll().child(pages[page].text))))
      .when(!this.keyboardMode, (e) => e.child(v_flex().id("ocq-content").flex_1().min_h(0).overflow_y_scroll().p(12).gap(8)
        .when(!active, (e) => e.child(div().text_color(t.muted).child("Select a Herdr space to view OCQ on its machine.")))
        .when(Boolean(this.error), (e) => e.child(div().text_color(t.ansi1).child(`Refresh failed; displayed data may be stale: ${this.error}`)))
        .when(Boolean(active) && !this.error && !this.loading && this.projects.length === 0, (e) => e.child(div().text_color(t.dim).child("No registered projects.")))
        .when(!project && this.projects.length > 1, (e) => e.child(div().child("Choose a project")))
        .when(this.projects.length > 1, (e) => e.child(v_flex().gap(3).children(this.projects.map((p) =>
          button(`ocq-project-${p.project_id}`, `${p.label} · ${p.project_id} · ${p.integrator_unread_mail_count} unread`, (cx) => this.select(p.project_id, cx))))))
        .when(Boolean(project), (e) => e
          .child(div().font_semibold().child(project?.label ?? ""))
          .child(div().text_size(12).text_color(t.muted).child(`Stored snapshot · ${project?.read_at ?? ""}`))
          .child(button("ocq-project-overview", "View project and binding", (cx) => this.openTarget("summary", undefined, cx)))
          .when(this.composeLocal(), (e) => e.child(button("ocq-implementation-submit", "Submit implementation job…", (cx) => this.openImplementationIntake(cx))))
          .when(Boolean(project?.current_binding?.handoff_manifest_digest), (e) => e.child(button("ocq-handoff", "Read handoff", (cx) => this.openTarget("manifest", project.current_binding.handoff_manifest_digest, cx))))
          .child(div().pt(8).font_semibold().child("Work"))
          .child(div().text_size(12).text_color(t.muted).child("Request observations may be stale; their observation time is not recorded. Inspect a job for its recorded session and location."))
          .when((project?.jobs ?? []).length === 0, (e) => e.child(div().text_color(t.dim).child("No jobs.")))
          .children((project?.jobs ?? []).map((j) => v_flex().id(`ocq-job-${j.job_id}`).py(5).gap(2).border_b(1).border_color(t.border)
            .cursor_pointer().hover((s) => s.bg(t.hover)).on_click((_e, cx) => this.inspect(j.job_id, cx))
            .child(div().child(`${j.role} · ${j.state} · ${j.outcome}`))
            .when(Boolean(attention(j)), (e) => e.child(div().text_size(12).text_color(t.blocked).child(attention(j))))
            .child(div().text_size(12).text_color(t.muted).child(j.job_id))
            .child(div().text_size(12).child(`Result: ${j.result_status} · Validation: ${j.validation?.status ?? "unknown"} · Review: ${j.review?.verdict ?? "not reviewed"}`))
            .when(Boolean(j.review?.stale_for_latest_result), (e) => e.child(div().text_color(t.blocked).child("Review is stale for the latest result")))
            .when(Boolean(j.reason), (e) => e.child(div().text_size(12).text_color(t.muted).child(j.reason)))
            .child(div().text_size(12).text_color(t.accent).child(this.detailJob === j.job_id ? "Close details" : "Inspect job"))
            .when(this.detailJob === j.job_id, (e) => e.child(v_flex().px(8).py(5).gap(3)
              .when(this.detailLoading, (e) => e.child(div().child("Loading job…")))
              .when(Boolean(this.detailError), (e) => e.child(div().text_color(t.ansi1).child(`Cannot inspect job: ${this.detailError}`)))
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
                .child(div().role("button").accessibility_label("Import or publish result manifest").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.publication.open(this, j.job_id, cx); }).child("Import / publish result manifest…"))
                .when(j.role === "implementation", (e) => e.child(div().role("button").accessibility_label("Prepare machine snapshot").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.publication.open(this, j.job_id, cx, true); }).child("Prepare / publish machine snapshot…")))
                .child(div().role("button").accessibility_label("View all job fields").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.openTarget("job", j.job_id, cx); }).child("View all job fields"))
                .when(Boolean(this.detail?.brief_digest), (e) => e.child(div().role("button").accessibility_label("Read job brief").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.openTarget("manifest", this.detail.brief_digest, cx); }).child("Read job brief")))
                .children((this.detail?.associations ?? []).map((a) => div().text_size(12).text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => {
                    cx.stop_propagation();
                    this.openTarget("manifest", a.manifest_digest, cx);
                  })
                  .child(`Read ${a.kind} · ${a.verification} · ${a.manifest_digest}`)))
                .children((this.detail?.associations ?? []).filter((a) => a.kind === "result").map((a) => div().role("button").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.worktreeCheck.open(this, a, this.detail?.job?.agent?.worktree, cx); })
                  .child(`Verify owned worktree… · ${a.manifest_digest}`)))
                .when(this.composeLocal(), (e) => e.children((this.detail?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-review-submit-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => this.openReviewIntake(a, cx))))))))))
          .child(div().pt(8).font_semibold().child("Lanes"))
          .when((project?.lanes ?? []).length === 0, (e) => e.child(div().text_color(t.dim).child("No lanes.")))
          .children((project?.lanes ?? []).map((lane) => button(`ocq-lane-${lane.lane_id}`, `${lane.role} · ${lane.job_state} · ${lane.lane_id}`, (cx) => this.openTarget("lane", lane.lane_id, cx))))
          .when(this.composeLocal() && this.compose.state === "idle", (e) => e.children((project?.lanes ?? []).map((lane) => button(`ocq-compose-lane-${lane.lane_id}`, `Compose update · ${lane.lane_id}`, (cx) => this.openCompose(lane, cx)))))
          .child(div().pt(8).font_semibold().child("Completion history"))
          .child(div().text_size(12).text_color(t.muted).child(`Recent 20 · ${this.collections.results.recentReadAt || "not yet read"}; historical pages refresh separately`))
          .when(Boolean(this.collections.results.recentError), (e) => e.child(div().text_color(t.ansi1).child(`Recent results read failed: ${this.collections.results.recentError}`)))
          .when(Boolean(this.collections.results.error), (e) => e.child(div().text_color(t.ansi1).child(`Results read failed: ${this.collections.results.error}`)))
          .when(this.collections.results.loading, (e) => e.child(div().child("Loading results…")))
          .when(this.collections.results.loaded && !this.collections.results.items.length, (e) => e.child(div().text_color(t.dim).child("No completion events.")))
          .children(this.collections.results.items.map((r) => v_flex().gap(2)
            .child(button(`ocq-result-${r.event.id}`, `${r.role} · historical ${r.event.outcome} · current ${r.current_job.state} · ${r.job_id}`, (cx) => this.openTarget("result", r.event.id, cx)))
            .children((r.result_summary?.associations ?? []).map((a) => button(`ocq-result-manifest-${a.id}`, `Read ${a.kind} · ${a.verification}`, (cx) => this.openTarget("manifest", a.manifest_digest, cx))))
            .children((r.result_summary?.associations ?? []).filter((a) => a.kind === "result").map((a) => button(`ocq-result-check-${a.id}`, `Verify owned worktree… · ${a.manifest_digest}`, (cx) => this.worktreeCheck.open(this, a, r.current_job.agent?.worktree, cx))))
            .when(this.composeLocal(), (e) => e.children((r.result_summary?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-result-review-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => this.openReviewIntake(a, cx)))))))
          .when(this.collections.results.hasMore, (e) => e.child(button("ocq-results-more", "Load more results", (cx) => this.loadCollection("results", false, cx))))
          .child(button("ocq-results-reload", "Reload results", (cx) => this.loadCollection("results", true, cx)))
          .child(div().pt(8).font_semibold().child("Inbox · all recipients"))
          .child(div().text_size(12).text_color(t.muted).child(`Recent 20 · ${this.collections.inbox.recentReadAt || "not yet read"}; includes read and archived mail; historical pages refresh separately`))
          .when(Boolean(this.collections.inbox.recentError), (e) => e.child(div().text_color(t.ansi1).child(`Recent inbox read failed: ${this.collections.inbox.recentError}`)))
          .when(Boolean(this.collections.inbox.error), (e) => e.child(div().text_color(t.ansi1).child(`Inbox read failed: ${this.collections.inbox.error}`)))
          .when(this.collections.inbox.loading, (e) => e.child(div().child("Loading inbox…")))
          .when(this.collections.inbox.loaded && !this.collections.inbox.items.length, (e) => e.child(div().text_color(t.dim).child("No messages.")))
          .children(this.collections.inbox.items.map((m) => v_flex().id(`ocq-message-${m.id}`).py(4).gap(2).border_b(1).border_color(t.border)
            .child(button(`ocq-read-${m.id}`, `${m.subject} · ${m.archived_at || this.mailState(m)?.message?.archived_at ? "archived" : m.read_at || this.mailState(m)?.message?.read_at ? "read" : "unread"}`, (cx) => this.openTarget("message", m.id, cx)))
            .child(div().text_size(12).text_color(t.muted).child(`${m.kind} · ${m.sender} → ${m.recipient}`))
            .when(Boolean(this.mailStatus(m)), (e) => e.child(div().text_size(12).text_color(this.mailState(m)?.status === "observed" ? t.muted : t.blocked).child(this.mailStatus(m))))
            .when(this.mailOptions(m).read, (e) => e.child(button(`ocq-mark-read-${m.id}`, "Mark read", (cx) => this.requestMailAction(m, false, cx))))
            .when(this.mailOptions(m).archive, (e) => e.child(button(`ocq-archive-${m.id}`, "Archive…", (cx) => this.requestMailAction(m, true, cx))))
            .when(this.mailOptions(m).check, (e) => e.child(button(`ocq-check-mail-${m.id}`, "Check stored message", (cx) => this.checkMailAction(m, cx))))
            .when(Boolean(m.manifest_digest), (e) => e.child(button(`ocq-attachment-${m.id}`, "Read attached manifest", (cx) => this.openTarget("manifest", m.manifest_digest, cx))))))
          .when(this.collections.inbox.hasMore, (e) => e.child(button("ocq-inbox-more", "Load more mail", (cx) => this.loadCollection("inbox", false, cx))))
          .child(button("ocq-inbox-reload", "Reload inbox", (cx) => this.loadCollection("inbox", true, cx)))
          .child(div().pt(8).text_size(12).text_color(t.muted).child("Stored observations only. Imported claims are unverified; worktree drift is checked only by explicit Verify filesystem now. Opening mail does not mark it read.")))));
  }
}
