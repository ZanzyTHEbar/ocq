import { TextareaState } from "gpui-base";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { RegistrationForm } from "./registration.js";
import { SubmissionForm } from "./submission.js";
import { composeDecode } from "./private-frames.js";

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

export class IntakeController {
  constructor(owner) {
    this.owner = owner;
    this.intake = { screen: false, state: "loading", id: null, kind: "registration", preview: null, page: 0, working: false, error: null, context: null, confirm: false, uncertainID: null, observation: null };
    this.intakeInput = TextareaState.new({ placeholder: "Paste an exact registration or submission JSON envelope", rows: 8 });
    this.intakeInputFocused = false;
    this.intakeInput.on("change", (_e, cx) => cx.notify());
    this.intakeInput.on("focus", (_e, cx) => { this.intakeInputFocused = true; cx.notify(); });
    this.intakeInput.on("blur", (_e, cx) => { this.intakeInputFocused = false; cx.notify(); });
    this.intakeInput.on("submit", () => this.handle.focus());
  }

  get handle() { return this.owner.handle; }

  clearInspection() {
    this.intake = { ...this.intake, screen: false, preview: null, context: null, confirm: false, working: false };
  }

  intakeCurrent(context) {
    const owner = this.owner;
    return Boolean(!this.intake.authoritySuspended && context && this.intake.context === context && this.intake.screen && owner.composeLocal() && context.machine === owner.machine && context.space === owner.spaceID && context.folder === owner.folder && context.selection === owner.selected);
  }

  intakeSubmission(raw) {
    const r = JSON.parse(raw);
    return Object.freeze({ project: r.project_id, revision: r.binding_revision, role: r.role, lane: r.lane_id || `lane-${createHash("sha256").update(r.key).digest("hex").slice(0, 16)}`, driver: r.fsfork_driver || "git_worktree", target: r.target_job_id || null, digest: r.target_result_digest || null });
  }

  intakeMatches(kind, raw, context, admission = false) {
    const owner = this.owner, request = JSON.parse(raw);
    if (!request || typeof request !== "object" || Array.isArray(request) || typeof request.project_id !== "string" || !/^proj-[A-Za-z0-9_-]+$/.test(request.project_id)) return false;
    if (context.reviewTarget && kind !== "submission") return false;
    if (context.generatedSubmission && createHash("sha256").update(raw).digest("hex") !== context.generatedSubmission) return false;
    if (kind === "registration") {
      if (context.registration && createHash("sha256").update(raw).digest("hex") !== context.registration.digest) return false;
      if (admission && context.registration && (context.newProject ? owner.projects.some((p) => p.project_id === request.project_id) : owner.board?.project?.current_binding?.revision !== context.revision)) return false;
      return request.repo === context.folder && request.expected_opencode_directory === context.folder && (context.newProject ? request.expected_revision === 0 : (!context.project || request.project_id === context.project));
    }
    if (kind !== "submission" || !context.project || request.project_id !== context.project) return false;
    if (admission && (owner.board?.project?.current_binding?.repo_root !== context.folder || owner.board?.project?.current_binding?.revision !== context.revision || request.binding_revision !== context.revision)) return false;
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
    const owner = this.owner;
    if (this.intake.working && this.intakeCurrent(this.intake.context)) return;
    const i = this.intake = { ...this.intake, authoritySuspended: false, working: false };
    i.screen = true; i.context = Object.freeze({ machine: owner.machine, space: owner.spaceID, folder: owner.folder, selection: owner.selected, project: newProject ? null : owner.selected, revision: owner.board?.project?.current_binding?.revision ?? null, newProject, reviewTarget: reviewTarget ? Object.freeze({ association: reviewTarget.association, job: reviewTarget.job, digest: reviewTarget.digest, source: reviewTarget.source, verification: reviewTarget.verification }) : null, submission: null, registration: null, generatedSubmission: null });
    if (newProject) i.kind = "registration";
    else if (reviewTarget) i.kind = "submission";
    i.entry = "form";
    this.registrationForm = new RegistrationForm(owner, cx, i.context);
    this.submissionForm = new SubmissionForm(owner, i.context);
    i.preview = null; i.error = null; i.state = "loading"; i.id = null; i.confirm = false; i.observation = null;
    owner.keyboardMode = false; this.handle.focus();
    this.refreshIntake(cx); cx.notify();
  }

  refreshIntake(cx) {
    const i = this.intake, context = i.context;
    if (i.working || !this.intakeCurrent(context)) return;
    i.working = true;
    cx.spawn(async (cx) => {
      try {
        const result = await bridgeCommand(["intake-status"], { on: null });
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
    const owner = this.owner;
    if (!owner.composeLocal() || association?.project_id !== owner.selected || association.kind !== "result" || association.availability !== "available" || !/^[0-9a-f]{64}$/.test(association.manifest_digest) || !association.job_id) return;
    if (this.intake.working && this.intakeCurrent(this.intake.context)) return;
    this.intakeInput.set_value("");
    this.openIntake(cx, false, { association: association.id, job: association.job_id, digest: association.manifest_digest, source: association.source, verification: association.verification });
  }

  openImplementationIntake(cx) {
    if (!this.owner.composeLocal() || !this.owner.selected || (this.intake.working && this.intakeCurrent(this.intake.context))) return;
    this.intake.kind = "submission";
    this.openIntake(cx);
  }

  async intakeExchange(mode, kind, id, raw, context, cx) {
    if (!this.intakeCurrent(context)) throw new Error("context changed");
    const current = compatibility.transfer(() => this.intakeCurrent(context));
    const launch = randomBytes(32).toString("hex");
    const command = mode === "receive" ? ["intake-receive-text", kind, launch] : mode === "draft" ? [`intake-${kind}-draft`, launch] : ["intake-review-preview", id, launch];
    if (mode !== "preview") {
      const bytes = Buffer.from(raw, "utf8");
      if (!bytes.length || bytes.length > (mode === "draft" && kind === "registration" ? 16384 : 131072) || composeDecode(bytes) !== raw) throw Error("invalid request");
    }
    const value = await compatibility.exchange(command[0], command.slice(1), raw, current);
    if (mode === "receive") {
      if (!/^PREPARED [0-9a-f]{64}$/.test(value.response)) throw Error("preparation uncertain");
      return value.response;
    }
    const exact = value.exact;
    if (!["registration", "submission"].includes(value.kind) || typeof exact?.raw !== "string" || Buffer.from(exact.raw).length > 131072 ||
      createHash("sha256").update(exact.raw).digest("hex") !== exact.sha256) throw Error("exact preview changed");
    if (mode === "draft" && value.state !== "draft") throw Error("draft unavailable");
    if (mode === "preview" && (value.id !== id || !["prepared", "armed", "attempted", "completed", "cancelled"].includes(value.state))) throw Error("preview state changed");
    return { kind: value.kind, raw: exact.raw, exact, ...(mode === "preview" ? { state: value.state } : {}) };
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
      if (!context.project || !Number.isSafeInteger(context.revision) || context.revision < 1 || draft.repo !== context.folder || this.owner.board?.project?.current_binding?.revision !== context.revision) throw new Error("binding changed");
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
      if (context.newProject && (kind !== "registration" || this.owner.projects.some((p) => p.project_id === JSON.parse(raw).project_id))) throw new Error("new project already exists");
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
        const claim = await bridgeCommand(["intake-arm", id], { on: null, current: () => this.intakeCurrent(context) && i.preview === preview && i.id === id });
        if (!claim || claim.code !== 0) throw new Error("claim outcome unknown");
        const armed = JSON.parse(claim.stdout);
        if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
        if (!this.intakeCurrent(context) || !i.confirm || i.preview !== preview || i.id !== id || !this.intakeMatches(preview.kind, preview.raw, context, true)) return;
        i.confirm = false; i.state = "armed";
        const result = await bridgeCommand(["intake-dispatch", id], { on: null, current: () => this.intakeCurrent(context) && i.preview === preview && i.id === id });
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
        const result = await bridgeCommand([`intake-${action}`, id], { on: null, current: () => this.intakeCurrent(context) && i.id === id });
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
}
