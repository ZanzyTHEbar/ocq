import { View, div } from "gpui-kit";
import { h_flex, v_flex, Textarea } from "gpui-base";
import { nextCommand, spaces, theme } from "herdr";
import { PrivateHCBinding } from "./hc-binding.js";
import { panelEpoch, panelVisible } from "./panel-state.js";
import { bridgeCommand, compatibility, enabledButton } from "./compatibility.js";
import { createHash } from "crypto";
import { QueueInspection } from "./queue.js";
import { WorktreeVerification } from "./worktree-check.js";
import { ResultPublication } from "./result-publication.js";
import { ComposeController } from "./compose-controller.js";
import { IntakeController } from "./intake-controller.js";
import { StoredUpdates, acceptObservationVersion } from "./stored-updates.js";
import { WorkOperator } from "./work-operator.js";
import { MainClientControls } from "./main-client.js";
import { OperatorWorkflow } from "./workflow.js";

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

// The bridge is a short-lived Go process. OCQ's database remains authoritative;
// no Herdr status, pane state, or mod state is interpreted as job completion.
export default class OCQ extends View {
  // Stored-view consumers read the actual controller state, never a copy.
  get compose() { return this.composeController.compose; }
  get intake() { return this.intakeController.intake; }

  init(_props, cx) {
    this.queue = new QueueInspection();
    this.updates = new StoredUpdates(this, cx);
    this.workOperator = new WorkOperator(this);
    this.mainClient = new MainClientControls(this);
    this.workflow = new OperatorWorkflow(this);
    this.nativeHC = new PrivateHCBinding(this);
    this.projectPages = [{ start: "0" }];
    this.lanePages = [{ start: "0" }];
    this.boardToken = {};
    this.worktreeCheck = new WorktreeVerification();
    this.worktreeCheck.reset();
    this.publication = new ResultPublication();
    this.queueMode = false;
    this.workspaceView = "projects";
    this.projectView = "operator";
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
    this.panelEpoch = panelEpoch();
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
    this.composeController = new ComposeController(this);
    this.intakeController = new IntakeController(this);
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

  async close() {
    this.closed = true; this.generation++; this.queue.generation++; this.publication.generation++; this.worktreeCheck.generation++;
    this.queue.dispatcher.dispose();
    this.workOperator.scroll.release();
    this.queue.scroll.jobs.release(); this.queue.scroll.events.release();
    // Herdr retains an async View.close until settlement or its own deadline.
    // Returning early cancels this view's host calls without settling their JS
    // promises, leaving the shared one-flight observer permanently draining.
    // The host deadline still applies; expiry cannot be treated as settlement.
    await this.updates.dispose();
  }

  panelCurrent() { return panelVisible() && this.panelEpoch === panelEpoch(); }

  syncPanelEpoch(cx) {
    const marker = panelEpoch();
    if (marker === this.panelEpoch) return;
    const job = this.detailJob, manifest = this.manifestDigest;
    const queueArgs = this.queue.inspecting ? this.queue.detail.snapshot?.args ?? this.queue.detail.pending?.args : null;
    this.panelEpoch = marker;
    // Retain selection, not confirmation authority. Pending private operations
    // may still finish; their receipts remain recovery evidence, never a retry.
    this.resetObservationVersions();
    compatibility.freshPanel(marker);
    this.observationReset = "OCQ panel was hidden or restored. Selection retained; inspect and review afresh before any action.";
    this.detailJob = job; this.manifestDigest = manifest;
    if (job) this.detailLoading = true;
    if (manifest) this.manifestLoading = true;
    if (queueArgs && this.queueMode) this.queue.read(this, "detail", queueArgs, cx, false);
    this.updates.readAgain(cx);
  }

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
    this.updates.suspendRecovery();
    this.workOperator.suspend("Context changed; retained work previews have no authority.");
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
    this.composeController.clearInspection();
    this.intakeController.clearInspection();
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
    return Boolean(!this.closed && this.panelCurrent() && active && active.machine === null && this.machine === null && active.id === this.spaceID && active.folder === this.folder);
  }

  targets() {
    return [...(this.headerTargets || []), ...(this.compatibilityTargets || []), ...(this.renderTargets || [])];
  }

  // Register alongside the rendered control, including its original callback
  // guard. Hidden sections never contribute keyboard actions. Inspection identity
  // is separate from navigation identity so data updates cannot redirect it.
  registerAction(id, label, action, list = this.renderTargets) {
    const reveal = this.actionReveal;
    const run = cx => { this.handle.focus(); this.navigationTarget = id; this.navigationLabel = label; action(cx); reveal?.(); };
    list.push({ id, label, run, reveal });
    if (this.navigationTarget === id) this.navigationLabel = label;
    return (_e, cx) => { cx.stop_propagation(); run(cx); };
  }

  key(e, cx) {
    if (this.workflow.focused) {
      if (e.key === "f6") { this.handle.focus(); this.workflow.focused = false; cx.stop_propagation(); cx.notify(); }
      return;
    }
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    const intake = this.intakeController, compose = this.composeController;
    if (this.intake.screen && !this.intake.authoritySuspended) {
      if (e.key === "f6") { this.handle.focus(); intake.intakeInputFocused = false; if (intake.registrationForm) intake.registrationForm.focused = false; if (intake.submissionForm) intake.submissionForm.focused = false; cx.stop_propagation(); cx.notify(); return; }
      if (intake.intakeInputFocused || (this.intake.entry === "form" && (this.intake.kind === "registration" ? intake.registrationForm?.focused : intake.submissionForm?.focused))) {
        if (e.key === "escape") { this.handle.focus(); cx.stop_propagation(); }
        return;
      }
      const i = this.intake;
      if (i.confirm) {
        if (e.key === "escape" && !i.working) { i.confirm = false; cx.stop_propagation(); cx.notify(); }
        return;
      }
      if (e.key === "escape") i.screen = false;
      else if (!e.is_held && e.key === "g" && i.entry === "form") { if (i.kind === "registration") intake.buildRegistration(cx); else intake.buildSubmission(cx); }
      else if (!e.is_held && ["left", "right"].includes(e.key) && !i.preview && i.state === "idle" && i.entry === "form" && !i.working) (i.kind === "registration" ? intake.registrationForm : intake.submissionForm).advance(e.key === "right" ? 1 : -1, intake, cx);
      else if (!e.is_held && e.key === "r") intake.refreshIntake(cx);
      else if (!e.is_held && e.key === "p") intake.prepareIntake(cx, i.preview?.draft ? i.preview : null);
      else if (!e.is_held && e.key === "v") intake.previewIntake(cx);
      else if (!e.is_held && e.key === "c") intake.intakeAction("cancel", cx);
      else if (!e.is_held && e.key === "t") intake.intakeAction("retire", cx);
      else if (!e.is_held && e.key === "left" && i.preview) intake.advanceIntakePage(-1, cx);
      else if (!e.is_held && e.key === "right" && i.preview) intake.advanceIntakePage(1, cx);
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
      if (e.key === "escape") { if (c.confirm) c.confirm = false; else compose.closeCompose(cx); }
      else if (!e.is_held && e.key === "r") compose.refreshCompose(cx);
      else if (!e.is_held && e.key === "p" && c.state === "idle") compose.prepareCompose(cx);
      else if (!e.is_held && e.key === "v") compose.previewCompose(cx);
      else if (!e.is_held && e.key === "c") compose.composeAction("cancel", cx);
      else if (!e.is_held && e.key === "k") compose.composeAction("reconcile", cx);
      else if (!e.is_held && e.key === "t") compose.composeAction("retire", cx);
      else if (!e.is_held && ["left", "right"].includes(e.key) && c.state === "idle" && c.form && !c.working) c.form.advance(e.key === "right" ? 1 : -1, compose, cx);
      else if (!e.is_held && e.key === "left" && c.preview && !c.confirm) compose.advanceComposePage(-1, cx);
      else if (!e.is_held && e.key === "right" && c.preview && !c.confirm) compose.advanceComposePage(1, cx);
      else if (!e.is_held && e.key === "s" && !c.confirm && compose.canReviewCompose()) c.confirm = true;
      else if (!e.is_held && e.key === "y" && c.confirm) compose.composeAction("dispatch", cx);
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
      targets[index].reveal?.();
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
        await bridgeCommand(["mail-ack", context.project, context.id, context.sequence, mode], { on: context.machine, current: () => this.mailContextCurrent(context) });
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
      const result = await bridgeCommand(["mail", context.project, context.id, state.sequence], { on: context.machine });
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
    this.syncPanelEpoch(cx);
    compatibility.observe();
    this.renderTargets = []; this.headerTargets = []; this.compatibilityTargets = [];
    const content = this.renderContent(cx);
    const t = theme();
    const workspace = !(this.intake.screen && !this.intake.authoritySuspended) && !(this.compose.screen && !this.compose.authoritySuspended) && !this.publication.screen && !this.worktreeCheck.screen && !(this.mailConfirmation && this.mailContextCurrent(this.mailConfirmation)) &&
      ![this.queue.dispatcher, this.queue.submission, this.queue.agentSubmission].some(helper => helper.screen && helper.current(this)) &&
      !(this.queue.ack.confirm && this.queue.ackCurrent(this, this.queue.ack)) &&
      ![this.queue.cancel, this.queue.resolution, this.queue.reconciliation].some(helper => helper.confirm && helper.current(this));
    // Keep compatibility discovery running; register its controls only while
    // visible so keyboard navigation never lands on a hidden diagnostic action.
    const compatibilityView = compatibility.notice(cx, t, (id, label, action) =>
      this.contextDetails ? this.registerAction(id, label, action, this.compatibilityTargets) : (_e, cx) => action(cx));
    return v_flex().size_full().min_h(0).bg(t.background).text_color(t.foreground)
      .when(workspace, e => e.child(this.workspaceHeader(cx, t)))
      .when(this.contextDetails, e => e.child(div().max_h(120).overflow_y_scroll().child(compatibilityView)))
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
    if (view !== "projects") this.workOperator.suspend();
    if (view === "queue" || view === "advanced") {
      if (!this.queueMode) this.openQueue(cx);
      this.queue.advanced = view === "advanced";
      this.queue.inspecting = false;
    }
    else { if (this.queueMode) this.leaveQueue(cx); this.workspaceView = view; this.keyboardMode = false; }
    cx.notify();
  }

  panelControl(id, label, action, t, targets = this.headerTargets, selected = false) {
    // Presentation-only controls. Mutation controls continue through the
    // existing enabledButton/confirmation paths in their original controllers.
    return div().id(id).role("button").accessibility_label(selected ? `${label}, selected` : label).px(8).py(5).rounded(5).cursor_pointer()
      .when(selected, e => e.bg(t.hover).font_semibold())
      .when(this.navigationTarget === id, e => e.bg(t.hover))
      .hover(e => e.bg(t.hover)).on_click(this.registerAction(id, label, action, targets)).child(label);
  }

  workspaceHeader(cx, t) {
    const control = (id, label, action, selected = false) => this.panelControl(id, label, action, t, this.headerTargets, selected);
    const queueID = this.updates.binding?.queue_identity || this.queue.jobs.data?.queue_identity || compatibility.status?.queue_identity;
    const view = this.queueMode ? this.queue.advanced ? "advanced" : "queue" : this.workspaceView;
    return v_flex().id("ocq-workspace-header").flex_none().px(12).pt(6).pb(4).gap(2).border_b(1).border_color(t.border)
      .child(h_flex().items_center().gap(6)
        .child(div().flex_1().min_w(0).truncate().font_semibold().child(`${this.board?.project?.label || this.selected || "OCQ"} · ${this.machine ?? this.folder?.split("/").pop() ?? "This computer"}`))
        .child(control("ocq-context-details", this.updates.connection.fault ? "Check setup" : "Details", cx => { this.contextDetails = !this.contextDetails; cx.notify(); })
          .when(Boolean(this.updates.connection.fault), e => e.text_color(t.blocked))))
      .child(h_flex().items_center().gap(2)
        .children([["projects", "Work"], ["queue", "Queue"], ["inbox", "Inbox"], ["advanced", "Advanced"]].map(([name, label]) => control(`ocq-workspace-${name}`, label, cx => this.navigateWorkspace(name, cx), view === name))))
      .when(this.contextDetails, e => e.child(v_flex().max_h(260).overflow_y_scroll().gap(4)
        .child(div().text_size(12).text_color(t.muted).child(`Folder: ${this.folder || "not selected"}\nSpace: ${this.spaceID || "not selected"}\nQueue: ${queueID || "not observed"}\nRevision: ${this.updates.revision ?? "not observed"}\nSnapshot: ${this.updateHealth.readAt || "not yet read"}\nProjects: ${this.projectsReadAt || "not yet read"}`))
        .child(div().text_size(12).text_color(t.muted).child(this.updateHealth.reason || "Observations are not execution or permission proof."))
        .when(this.machine !== null, e => e.child(div().text_size(12).text_color(t.muted).child("Remote: only explicitly prepared work can be confirmed. Other mutations stay disabled.")))
        .child(h_flex().gap(2)
          .child(control("ocq-copy-update-diagnostics", "Copy diagnostics", cx => cx.write_to_clipboard(JSON.stringify({ machine: this.machine, health: this.updateHealth, revision: this.updates.revision })))))
        .child(control("ocq-project-picker", "Change project", cx => { this.projectPicker = true; this.navigateWorkspace("projects", cx); }, false))
        .child(control("ocq-workspace-overview", "Project summary", cx => this.navigateWorkspace("overview", cx)))));
  }

  emptyProjects(cx, t, button) {
    return v_flex().py(12).gap(8).child(this.workflow.render(cx, t, button));
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
    const intake = this.intakeController, compose = this.composeController;
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
    const button = (id, text, action, label = text) => div().id(id).role("button").accessibility_label(label).px(8).py(4).rounded(5)
      .text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
      .when(!enabledButton(id) || (this.queueMode && !this.queue.detailControlAllowed(id)), e => e.opacity(0.4))
      .when(this.navigationTarget === id, e => e.bg(t.hover))
      .on_click(!this.queueMode && enabledButton(id)
        ? this.registerAction(id, label, cx => { if (enabledButton(id)) action(cx); })
        : (_e, cx) => { cx.stop_propagation(); if (enabledButton(id) && (!this.queueMode || this.queue.detailControlAllowed(id))) { this.handle.focus(); action(cx); } }).child(text);
    if (this.queueMode && active) return this.queue.render(this, cx, t, button);
    if (this.worktreeCheck.screen) return this.worktreeCheck.render(this, cx, t, button);
    if (this.publication.screen) return this.publication.render(this, cx, t, button);
    if (this.intake.screen && !this.intake.authoritySuspended) {
      const i = this.intake, local = intake.intakeCurrent(i.context), preview = i.preview;
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
            .when(!i.working && intake.canConfirmIntake(), (e) => e.child(div().id("ocq-intake-confirm-once").px(8).py(8).rounded(5).text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
              .on_mouse_down("left", (_e, cx) => intake.confirmIntake(cx)).child("Press to confirm once")))
            .when(!i.working, (e) => e.child(button("ocq-intake-confirm-back", "Back to fields", (cx) => { i.confirm = false; cx.notify(); }))));
      }
      if (local && preview) {
        const page = preview.pages[i.page];
        return v_flex().id("ocq-intake-review").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
          .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
          .child(h_flex().flex_none().items_center().gap(8)
            .child(div().font_semibold().child(`${preview.draft ? "Draft" : preview.kind} · ${preview.view === "review" ? "field" : "raw"} ${i.page + 1}/${preview.pages.length}`))
            .child(div().flex_1()).child(button("ocq-intake-review-back", preview.draft ? "Edit form" : "Back", (cx) => { if (preview.draft) intake.intakeEntry("form", cx); else { i.preview = null; cx.notify(); } })))
          .child(v_flex().id(`ocq-intake-page-${preview.view}-${i.page}`).flex_1().min_h(0).overflow_y_scroll().gap(8)
            .when(Boolean(preview.draft), (e) => e.child(div().text_size(12).child(`Generated by the core from this form; not retained or ${preview.kind === "submission" ? "submitted" : "registered"}. P prepares only after all fields have been opened. The retained request then needs its own review and confirmation.`)))
            .child(div().text_size(12).text_color(t.muted).child(preview.view === "raw" ? "Exact JSON string: join lines and pages to recover the retained text." : "Scroll to see the entire field. Controls and invisible characters are escaped. Opening pages is not confirmation."))
            .child(div().font_semibold().child(page.title + (page.parts > 1 ? ` · ${page.part}/${page.parts}` : "")))
            .child(div().child(page.text))
            .when(preview.draft && !i.working && preview.view === "review" && i.page === preview.review.length - 1 && preview.openedThrough === preview.review.length - 1, (e) => e.child(button("ocq-intake-draft-prepare", preview.kind === "submission" ? "Prepare these generated bytes (no submission yet)" : "Prepare these generated bytes (no registration yet)", (cx) => intake.prepareIntake(cx, preview))))
            .when(Boolean(i.error), (e) => e.child(div().text_color(t.ansi1).child(i.error)))
            .when(Boolean(i.observation), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Last observation: ${i.observation}`)))
            .when(intake.canConfirmIntake(), (e) => e.child(button("ocq-intake-review-confirm", "Review confirmation…", (cx) => { if (intake.canConfirmIntake()) i.confirm = true; cx.notify(); })))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["prepared", "armed"].includes(i.state), (e) => e.child(button("ocq-intake-cancel", "Cancel before attempt", (cx) => intake.intakeAction("cancel", cx))))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["attempted", "completed"].includes(i.state), (e) => e.child(button("ocq-intake-reconcile", "Check exact stored receipt", (cx) => intake.intakeAction("reconcile", cx))))
            .when(!i.working && preview.id === i.id && preview.state === i.state && ["cancelled", "completed"].includes(i.state), (e) => e.child(button("ocq-intake-retire", "Retire retained intent", (cx) => intake.intakeAction("retire", cx)))))
          .child(h_flex().flex_none().gap(8)
            .child(button("ocq-intake-previous", "Previous", (cx) => intake.advanceIntakePage(-1, cx)))
            .child(button("ocq-intake-next", "Next", (cx) => intake.advanceIntakePage(1, cx)))
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
        .when(local && !i.working, (e) => e.child(button("ocq-intake-context", i.context.newProject ? "Use selected project context" : "Register a new project…", (cx) => intake.openIntake(cx, !i.context.newProject))))
        .when(local, (e) => e.child(div().child(stateText[i.state] || "Journal status unavailable.")))
        .when(Boolean(i.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${i.id}`)))
        .when(Boolean(i.error), (e) => e.child(div().text_color(t.ansi1).child(i.error)))
        .when(local && !i.working, (e) => e.child(button("ocq-intake-refresh", "Refresh journal", (cx) => intake.refreshIntake(cx))))
        .when(local && i.state === "idle", (e) => e.child(v_flex().gap(6)
          .child(h_flex().gap(8)
            .when(!i.context.reviewTarget, (e) => e.child(button("ocq-intake-registration", i.kind === "registration" ? "Registration ✓" : "Registration", (cx) => { i.kind = "registration"; cx.notify(); })))
            .when(!i.context.newProject, (e) => e.child(button("ocq-intake-submission", i.kind === "submission" ? "Submission ✓" : "Submission", (cx) => { i.kind = "submission"; cx.notify(); }))))
          .child(h_flex().gap(8)
            .when(!i.working, (e) => e.child(button("ocq-intake-guided", i.entry === "form" ? "Guided form ✓" : "Guided form", (cx) => intake.intakeEntry("form", cx))))
            .when(!i.working, (e) => e.child(button("ocq-intake-advanced", i.entry === "raw" ? "Advanced raw ✓" : "Advanced raw", (cx) => intake.intakeEntry("raw", cx)))))
          .when(i.kind === "registration" && i.entry === "form", (e) => e.child(intake.registrationForm.render(intake, cx, t, button)))
          .when(i.kind === "submission" && i.entry === "form", (e) => e.child(intake.submissionForm.render(intake, cx, t, button)))
          .when(i.entry === "raw", (e) => e.child(v_flex().gap(6)
            .child(div().child("Advanced raw import: exact original JSON bytes are retained, never rebuilt by the form. Typing does not prepare or dispatch."))
            .child(Textarea.new(intake.intakeInput).id("ocq-intake-input").accessibility_label("Exact intake JSON envelope").h(150).px(8).border_1().border_color(t.border).text_color(t.foreground))
            .when(!i.working, (e) => e.child(button("ocq-intake-prepare", "Prepare retained request", (cx) => intake.prepareIntake(cx))))))))
        .when(local && !i.working && Boolean(i.id), (e) => e.child(button("ocq-intake-preview", "Preview exact retained request", (cx) => intake.previewIntake(cx))))
        .child(div().text_size(12).text_color(t.muted).child("R refresh · P prepare · V preview · C cancel · T retire · ←/→ pages · Esc close"));
    }
    if (this.compose.screen && !this.compose.authoritySuspended) {
      const c = this.compose, local = this.composeLocal(), request = c.preview?.request;
      const previewPages = c.preview?.pages || [];
      const stateText = { loading: "Checking local journal…", idle: "No retained compose intent.", busy: "Another compose operation holds the journal. Refresh later.", prepared: "Prepared locally. A confirmation from another view may still be pending. Preview before confirming.", armed: "Send confirmed and durably claimed. A send process may be delayed; do not confirm again. You can revoke only before an attempt begins.", dispatch_intent: "Send outcome uncertain. Check the stored receipt; never resend automatically.", uncertain: "Send outcome uncertain. Check the stored receipt; never resend automatically.", completed: "Stored receipt matched. Retire to prepare another message.", cancelled: c.observation === "revoked_before_attempt" ? "Claim revoked before any attempt. A delayed send process will be refused. Retire to prepare another message." : "Prepared message cancelled. A delayed confirmation will be refused. Retire to prepare another message.", blocked: "Journal blocked or unverifiable. Do not send; investigate the local store." };
      return v_flex().id("ocq-compose").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
        .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .child(h_flex().items_center().gap(8).child(div().font_semibold().child("Compose · local only")).child(div().flex_1())
          .child(button("ocq-compose-close", "Close", (cx) => compose.closeCompose(cx))))
        .child(div().text_color(local ? t.muted : t.blocked).child(local ? "This computer only · journal survives panel copies" : "Remote compose unavailable. Switch to a local space; nothing will be redirected."))
        .when(local, (e) => e.child(div().text_color(["blocked", "uncertain", "armed", "dispatch_intent"].includes(c.state) ? t.blocked : t.foreground).child(stateText[c.state] || "Compose state unavailable.")))
        .when(Boolean(c.error), (e) => e.child(div().text_color(t.ansi1).child(c.error)))
        .when(Boolean(c.observation), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Last observation: ${c.observation}`)))
        .when(Boolean(c.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${c.id}`)))
        .when(local && !c.working, (e) => e.child(button("ocq-compose-refresh", "Refresh journal status", (cx) => compose.refreshCompose(cx))))
        .when(local && c.working, (e) => e.child(div().child("Checking the exact local context/journal…")))
        .when(local && !c.working && c.state === "idle" && Boolean(c.context && c.form && c.options), (e) => e.child(c.form.render(compose, cx, t, button, c.options)))
        .when(local && c.state === "idle" && !c.context, (e) => e.child(div().text_color(t.muted).child("Close and choose a stored owner lane to compose lane.blocked or lane.assigned mail.")))
        .when(local && !c.working && !request && (["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))), (e) => e.child(button("ocq-compose-preview", "Preview exact retained message", (cx) => compose.previewCompose(cx))))
        .when(Boolean(local && request), (e) => e.child(v_flex().flex_1().min_h(0).gap(6)
          .child(div().font_semibold().child(`Retained preview · ${c.page + 1}/${previewPages.length}`))
          .child(div().text_size(12).text_color(t.muted).child("Quoted fields escape controls and invisible characters; continuations preserve their order."))
          .child(div().flex_1().min_h(0).overflow_y_scroll().child(previewPages[c.page]))
          .when(!c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-previous", "Previous", (cx) => compose.advanceComposePage(-1, cx)))
            .child(button("ocq-compose-next", "Next", (cx) => compose.advanceComposePage(1, cx)))))
          .when(!c.working && !c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-cancel", "Cancel before attempt", (cx) => compose.composeAction("cancel", cx)))
            .when(compose.canReviewCompose(), (e) => e.child(button("ocq-compose-review", "Review send…", (cx) => { if (compose.canReviewCompose()) c.confirm = true; cx.notify(); })))))
          .when(!c.working && c.state === "prepared" && c.preview?.state === "prepared" && c.confirm, (e) => e.child(v_flex().gap(6)
            .child(div().text_color(t.blocked).child("Send this exact retained message once? Every field was opened sequentially; opening is not proof of comprehension. Exact fields, links and the reviewed lane default are rechecked before the claim. A lost response remains uncertain."))
            .child(h_flex().gap(8).child(button("ocq-compose-back", "Back", (cx) => { c.confirm = false; cx.notify(); }))
              .child(button("ocq-compose-confirm", "Confirm send once", (cx) => compose.composeAction("dispatch", cx))))))))
        .when(local && !c.working && ["dispatch_intent", "uncertain", "completed"].includes(c.state), (e) => e.child(button("ocq-compose-reconcile", "Check exact stored receipt", (cx) => compose.composeAction("reconcile", cx))))
        .when(local && !c.working && ["completed", "cancelled"].includes(c.state), (e) => e.child(button("ocq-compose-retire", "Retire retained intent", (cx) => compose.composeAction("retire", cx))))
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
    // Tasks own one native scroll viewport. Do not put their whole list inside
    // a second scroll child: native reveal indices must address individual rows.
    if (active && this.selected && this.projects.length && !this.projectPicker &&
        !this.error && !this.boardError && !this.keyboardMode &&
        this.workspaceView === "projects" && this.projectView === "operator") {
      return v_flex().id("ocq-tasks-panel").size_full().min_h(0).bg(t.background).text_color(t.foreground).text_size(13)
        .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .when(this.contextDetails, e => e.child(this.projectTabs(t, button).px(12)))
        .child(this.workOperator.render(cx, t, button));
    }
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
           .when(Boolean(active) && Boolean(this.projectPages[0].readAt) && !this.error && !this.projectsHasMore && this.projects.length === 0, e => e.child(this.emptyProjects(cx, t, button)))
          .when(Boolean(this.boardError), e => e.child(this.readFault(this.boardError, "selected project", t)))
          .when(this.workspaceView === "overview" && this.projects.length > 0, e => e
            .child(div().font_semibold().child("Project summary"))
            .child(div().child(this.selected ? project?.label || this.selected : "Choose a project in Work."))
            .child(this.panelControl("ocq-summary-work", "Open work", cx => { this.projectView = "operator"; this.navigateWorkspace("projects", cx); }, t, this.renderTargets))
           .when(Boolean(project), e => e.child(div().child(`Selected project · ${(project.jobs || []).length} loaded jobs · ${(project.jobs || []).filter(j => attention(j)).length} need inspection\nLoaded inbox items: ${this.collections.inbox.items.length} · loaded completion items: ${this.collections.results.items.length}`))))
          .when(this.workspaceView === "projects" && this.projects.length > 0, e => e
            .child(h_flex().items_center().gap(6)
              .child(div().flex_1().min_w(0).truncate().font_semibold().child(project?.label || this.selected || "Choose a project"))
              .child(this.panelControl("ocq-project-picker", this.projectPicker ? "Done" : "Change", cx => { this.projectPicker = !this.projectPicker; cx.notify(); }, t, this.renderTargets))))
        .when(!project && this.projects.length > 1, (e) => e.child(div().child("Choose a project")))
          .when(this.workspaceView !== "overview" && this.projects.length > 0 && (this.projectPicker || !this.selected), (e) => e.child(v_flex().gap(3).children(this.projects.map((p) =>
           button(`ocq-project-${p.project_id}`, `${p.label || p.project_id}${p.integrator_unread_mail_count ? ` · ${p.integrator_unread_mail_count} unread` : ""}`, cx => { this.select(p.project_id, cx); this.projectPicker = false; })))))
          .when(this.workspaceView !== "overview" && this.projectsHasMore, e => e.child(button("ocq-project-pages-more", "Load next 20 projects", cx => { const last = this.projectPages[this.projectPages.length - 1]; if (last.next && !this.projectPages.some(page => page.start === last.next)) this.projectPages.push({ start: last.next }); cx.notify(); })))
          .when(Boolean(this.selected) && this.workspaceView !== "overview", (e) => e
            .when(this.workspaceView === "projects", e => e.child(this.projectTabs(t, button)))
           .when(this.workspaceView === "projects" && this.projectView === "operator", e => e.child(this.workOperator.render(cx, t, button)))
           .when(this.workspaceView === "projects" && this.projectView === "binding", e => e
           .child(button("ocq-project-overview", "View project and binding", (cx) => this.openTarget("summary", undefined, cx)))
           .when(this.composeLocal(), e => e.child(button("ocq-intake-open", "Project intake / binding…", cx => intake.openIntake(cx))))
           .when(Boolean(project?.current_binding?.handoff_manifest_digest), (e) => e.child(button("ocq-handoff", "Read handoff", (cx) => this.openTarget("manifest", project.current_binding.handoff_manifest_digest, cx)))))
            .when(Boolean(project) && this.workspaceView === "projects" && this.projectView === "work", e => e
          .when(this.composeLocal(), (e) => e.child(button("ocq-implementation-submit", "Submit implementation job…", (cx) => intake.openImplementationIntake(cx))))
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
                .when(this.composeLocal(), (e) => e.children((this.detail?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-review-submit-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => intake.openReviewIntake(a, cx))))))))))
           )
           .when(this.workspaceView === "projects" && this.projectView === "lanes", e => e
           .child(button("ocq-compose-open", "Compose / recover retained message…", cx => compose.openCompose(null, cx)))
           .child(div().pt(8).font_semibold().child("Lanes"))
           .children(this.lanePages.filter(page => page.error).map(page => this.readFault(page.error, `lanes page ${page.start}`, t)))
           .when(Boolean(this.lanePages[0].readAt) && !this.lanePages[0].error && !(this.lanes || []).length, (e) => e.child(div().text_color(t.dim).child("No lanes.")))
           .children((this.lanes || []).map((lane) => button(`ocq-lane-${lane.lane_id}`, `${lane.role} · ${lane.job_state} · ${lane.lane_id}`, (cx) => this.openTarget("lane", lane.lane_id, cx))))
           .when(this.composeLocal() && this.compose.state === "idle", (e) => e.children((this.lanes || []).map((lane) => button(`ocq-compose-lane-${lane.lane_id}`, `Compose update · ${lane.lane_id}`, (cx) => compose.openCompose(lane, cx)))))
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
            .when(this.composeLocal(), (e) => e.children((r.result_summary?.associations ?? []).filter((a) => a.kind === "result" && a.availability === "available").map((a) => button(`ocq-result-review-${a.id}`, `Submit review job for this result… · ${a.manifest_digest}`, (cx) => intake.openReviewIntake(a, cx)))))))
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
  projectTabs(t, button) {
    return h_flex().gap(2).children([["operator", "Tasks"], ["work", "Jobs"], ["results", "Results"], ["lanes", "Lanes"], ["binding", "Setup"]].map(([view, label]) =>
      button(`ocq-project-tab-${view}`, label, cx => {
        if (view !== this.projectView) this.workOperator.suspend();
        this.projectView = view; cx.notify();
      }).when(this.projectView === view, e => e.bg(t.hover).font_semibold())));
  }
}
