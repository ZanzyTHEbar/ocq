import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { spaces, panes, exec, notify } from "herdr";
import { createHash } from "crypto";
import { bridgeCommand, sourceCompatibility } from "./compatibility.js";
import { ProjectSessions } from "./project-sessions.js";
import { MainClientControls } from "./main-client.js";
import { GoalComposer } from "./workflow.js";
import { TaskGoalComposer } from "./task-goal.js";
import { PrivateHCBinding } from "./hc-binding.js";
import { panelEpoch, panelVisible } from "./panel-state.js";

export function projectSpaces(machine, repo = null) {
  const live = panes();
  const choices = [];
  for (const space of spaces().filter(s => s.machine === machine)) {
    const matches = space.tabs.flatMap(tab => live.filter(p => p.tab === tab.id && p.terminal && p.cwd &&
      (!repo || p.cwd === repo || p.cwd.startsWith(repo.endsWith("/") ? repo : `${repo}/`))).map(p => ({
      ...space, folder: p.cwd, activeTab: tab.active,
      setupContext: { tab: tab.id, tabName: tab.name, pane: p.pane, terminal: p.terminal, cwd: p.cwd, branch: p.branch },
    })));
    if (repo) {
      // Prefer an exact root in each existing space. Git and the endpoint
      // still reverify the chosen location before it can authorize actions.
      matches.sort((a, b) => Number(b.folder === repo) - Number(a.folder === repo) || Number(b.activeTab) - Number(a.activeTab));
      if (matches.length) choices.push(matches[0]);
    } else choices.push(...matches);
  }
  return choices;
}

export class RuntimeProject {
  constructor(source, project, space) {
    this.source = source; this.sourceKey = source.key; this.root = source.owner;
    this.selected = project?.project_id || null; this.project = project;
    this.spaceID = space?.id || null; this.space = space;
    this.folder = project?.repo_root || space?.folder || null;
    this.machine = source.machine; this.setupContext = space?.setupContext || null;
    this.generation = 0; this.panelEpoch = source.generation; this.marker = panelEpoch();
    this.resultCursor = "0"; this.historyRead = false;
    this.compatibility = sourceCompatibility(() => this.panelCurrent() && this.spaceID ? {
      id: this.spaceID, machine: this.machine, folder: this.folder, source: this.sourceKey, generation: source.generation,
    } : null);
    const connection = {
      updates: { current: () => this.panelCurrent() && source.online() },
      get state() { return source.connection.state; },
      get executionPeer() { return source.machine ?? "local"; },
      get executionCore() { return source.runtime.core_id; },
      command: (args, payload) => {
        const at = args.indexOf("--"), positions = at >= 0 ? args.slice(at + 1) : args.slice(1);
        const target = args[0].startsWith("main-") ? positions[2] : this.selected;
        return source.command(args, payload, { project_id: target || "", space: this.localSpace(), folder: this.folder });
      },
    };
    this.compatibility.pushConnection = connection;
    this.updates = {
      connection, get connectionID() { return source.connectionID; },
      get connectionGeneration() { return source.generation; },
      get bindingEpoch() { return source.generation; },
      scope: { machine: this.machine, localSpace: this.localSpace(), folder: this.folder },
      current: () => this.panelCurrent(), binding: null,
    };
    this.sessions = new ProjectSessions(this);
    this.mainClient = new MainClientControls(this);
    this.workflow = new GoalComposer(this);
    this.taskGoal = new TaskGoalComposer(this);
    this.nativeHC = new PrivateHCBinding(this);
    this.needsVerification = true;
  }
  localSpace() {
    return this.machine !== null && this.spaceID?.startsWith(`${this.machine}/`) ? this.spaceID.slice(this.machine.length + 1) : this.spaceID;
  }
  terminalCurrent() {
    const s = this.setupContext;
    return !s || (spaces().some(space => space.id === this.spaceID && space.machine === this.machine && space.tabs.some(t => t.id === s.tab)) &&
      panes().some(p => p.pane === s.pane && p.tab === s.tab && p.terminal === s.terminal && p.cwd === s.cwd && p.branch === s.branch));
  }
  panelCurrent() {
    return !this.closed && this.root.detail === this && this.source.current() && this.source.machine === this.machine &&
      this.source.generation === this.panelEpoch && this.marker === panelEpoch() && panelVisible() && this.terminalCurrent();
  }
  invalidate() {
    this.generation++;
    this.compatibility.invalidateObservation("Project connection changed");
    this.updates.binding = null; this.board = null;
    this.sessions.reset(); this.workflow.review = null;
    this.nativeHC.approved = null; this.nativeHC.review = null;
    this.deleteIntent = null;
    this.needsVerification = true; this.error = null;
    this.resultCursor = "0"; this.historyRead = false;
  }
  dispose() { this.closed = true; this.invalidate(); }
  refresh(cx) {
    const created = this.workflow.status;
    // Replacement advances the binding of this same project and checkout.
    // Reuse the current panel and its draft instead of reopening the view.
    if (["create", "adopt"].includes(created?.mode) && created.state === "ready" &&
        created.repo === this.folder && created.project_id && created.session_id) {
      this.root.openProject(this.source, { project_id: created.project_id, repo_root: created.repo,
        label: this.workflow.label.value() || this.project?.label }, this.space, cx);
    } else this.source.subscribe(true);
    cx.notify();
  }
  async initialize(cx) {
    if (!this.spaceID || this.verifying) return;
    const generation = this.generation;
    const current = () => this.panelCurrent() && generation === this.generation;
    this.verifying = true; this.error = null;
    let reason = "Could not verify this checkout. Choose a project terminal.";
    try {
      const result = await exec("git", ["rev-parse", "--show-toplevel"], { cwd: this.setupContext.cwd, on: this.machine, timeout: 5000 });
      if (!current()) return;
      const folder = result.stdout?.replace(/\r?\n$/, "");
      if (result.code !== 0 || !folder || /[\r\n\0]/.test(folder) || this.project && this.project.repo_root !== folder) throw Error();
      this.folder = folder; this.updates.scope.folder = folder;
      reason = "Project connection could not be verified.";
      const response = await this.updates.connection.command(["compatibility", this.localSpace(), folder]);
      if (!current()) return;
      if (response.code !== 0) throw Error();
      const status = this.compatibility.validate(JSON.parse(response.stdout), { id: this.spaceID, machine: this.machine, folder });
      if (status.queue_identity !== this.source.runtime.queue_identity || status.image_sha256 !== this.source.runtime.image_sha256 ||
          status.observation_store_context !== this.source.binding?.store_context || ![6, 7].includes(status.observation_schema) ||
          !/^[a-f0-9]{64}$/.test(status.observation_context)) throw Error();
      this.compatibility.observe();
      this.compatibility.status = status; this.compatibility.allowActions = this.source.mutations;
      this.updates.binding = { queue_identity: status.queue_identity, store_context: status.observation_store_context, bridge_context: status.observation_context };
      this.source.subscribe(true);
    } catch (_) { if (current()) this.error = reason; }
    finally { this.verifying = false; cx.notify(); }
  }
  observeResults(slot) {
    if (!this.panelCurrent() || slot.status !== "ready") return;
    for (const result of slot.data.items) {
      const row = this.sessions.rows.find(r => r.job.job_id === result.job_id);
      const event = result.event;
      if (!event?.id || !row) continue;
      const key = JSON.stringify([this.sourceKey, event.id]);
      if (this.root.notified.has(key)) continue;
      this.root.notified.add(key);
      if (this.historyRead) {
        try {
          const blocked = event.state === "unknown";
          const action = row.job.session_state || !row.job.session_id ? undefined : { name: `ocq.${this.sessionCommand(row)}` };
          notify({ title: `${this.project.label} · ${blocked ? "Session needs attention" : "Job ended"}`,
            body: row.title, level: blocked ? "warning" : "info", action });
        } catch (_) { /* Notification delivery does not change stored state. */ }
      }
    }
    if (!slot.has_more) this.historyRead = true;
    // Keep listening from the last page's start until it fills. This is a
    // non-consuming project result cursor; never ack global queue events.
    if (slot.has_more) this.resultCursor = slot.cursor;
  }
  sessionCommand(row) {
    return `open-${createHash("sha256").update(JSON.stringify([this.sourceKey, this.selected, row.request_id, row.job.session_id])).digest("hex")}`;
  }
  openNotified(command, cx) {
    if (!this.sessions.current(this.sessions.bound) || !this.compatibility.machineReady()) return;
    const row = this.sessions.rows.find(item => item.job.session_id && !item.job.session_state && this.sessionCommand(item) === command);
    if (row) this.mainClient.openConversation("worker", row.request_id, cx);
  }
  deleteCurrent(intent) {
    return this.deleteIntent === intent && this.sessions.current(intent.scope) &&
      this.mainClient.targetSession(intent.kind, intent.work) === intent.session;
  }
  requestDelete(kind, work, session, label, cx) {
    const scope = this.sessions.bound;
    if (!this.sessions.current(scope) || this.mainClient.targetSession(kind, work) !== session || this.deleteIntent?.state === "deleting") return;
    const available = this.compatibility.machineReady() &&
      this.compatibility.status?.actions?.["session-delete-v1"] === "ocq-session-delete-v1";
    const intent = { scope, kind, work, session, label, state: available ? "checking" : "unsupported" };
    this.deleteIntent = intent;
    if (available) this.readDelete(intent, cx);
  }
  deleteArgs(intent) {
    const s = intent.scope;
    return [s.space, s.queue, s.project, intent.session, s.machine ?? "local", s.folder, s.store,
      this.compatibility.status?.image_sha256, intent.work];
  }
  readDeleteResult(response, intent) {
    const value = response.code === 0 && JSON.parse(response.stdout);
    if (value?.protocol !== "ocq-session-delete-v1" || value.session_id !== intent.session ||
        !["ready", "active", "pending", "resources", "job_unsettled", "changed", "unavailable", "uncertain", "deleted"].includes(value.state) ||
        (value.state === "ready" && !/^[a-f0-9]{64}$/.test(value.preview_sha256 || ""))) throw Error("Exact deletion check unavailable");
    return value;
  }
  readDelete(intent, cx) {
    cx.spawn(async task => {
      try {
        const response = await bridgeCommand(["session-delete-check-v1", "--work-observation-context", intent.scope.bridge,
          "--", ...this.deleteArgs(intent)], { on: this.machine, compatibility: this.compatibility });
        const value = this.readDeleteResult(response, intent);
        if (this.deleteCurrent(intent)) { intent.state = value.state; intent.preview = value.preview_sha256; }
      } catch (_) { if (this.deleteCurrent(intent)) intent.state = "unavailable"; }
      task.notify();
    });
  }
  confirmDelete(intent, cx) {
    if (!this.deleteCurrent(intent) || intent.state !== "ready") return;
    intent.state = "deleting";
    cx.spawn(async task => {
      try {
        const response = await bridgeCommand(["session-delete-v1", ...this.deleteArgs(intent), intent.preview], {
          on: this.machine, compatibility: this.compatibility,
          current: () => this.deleteCurrent(intent) && intent.state === "deleting",
        });
        const value = this.readDeleteResult(response, intent);
        if (this.deleteIntent !== intent) return;
        if (value.state === "deleted") { this.deleteIntent = null; this.source.subscribe(true); }
        else intent.state = value.state === "ready" ? "unavailable" : value.state;
      } catch (_) {
        if (this.deleteIntent === intent) { intent.state = "uncertain"; this.source.subscribe(true); }
      } finally { task.notify(); }
    });
  }
  renderDelete(kind, work, session, button, note) {
    const intent = this.deleteIntent;
    if (!intent || intent.kind !== kind || intent.work !== work || intent.session !== session) return null;
    const reason = {
      checking: "Checking whether the session is idle…",
      deleting: "Deleting session…",
      active: "Open the session and stop it in OpenCode before deleting.",
      pending: "Open the session and resolve pending requests before deleting.",
      job_unsettled: "Wait for OCQ to finish checking the job before deleting.",
      resources: "Close the session’s running tools or child sessions before deleting.",
      changed: "This session changed. Check it again before deleting.",
      uncertain: "Deletion could not be confirmed. Do not repeat it.",
      deleted: "This session was already deleted.",
      unsupported: "Deletion requires an updated machine bridge.",
      unavailable: "Could not verify this session. Nothing was deleted.",
    }[intent.state];
    return v_flex().gap(6).py(6)
      .child(div().font_semibold().child(`Delete ${intent.label}?`))
      .child(note(reason || "Keep this session idle until deletion finishes. The checkout and recorded results stay."))
      .child(h_flex().gap(6)
        .child(button("ocq-delete-cancel", "Cancel", task => { this.deleteIntent = null; task.notify(); }, intent.state !== "deleting"))
        .child(button("ocq-delete-confirm", "Delete session", task => this.confirmDelete(intent, task), intent.state === "ready")));
  }
  render(cx, t) {
    if (this.marker !== panelEpoch() || this.panelEpoch !== this.source.generation) {
      this.marker = panelEpoch(); this.panelEpoch = this.source.generation; this.invalidate();
    }
    const box = v_flex().id("ocq-project-conversations").w_full().flex_1().min_w(0).min_h(0).overflow_y_scroll().p(12).gap(8);
    const button = (id, text, action, enabled = true, label = text) => this.root.button(id, text, action, t, enabled, label);
    const note = text => div().text_size(12).text_color(t.muted).child(text);
    if (!this.source.online()) return box.child(note("Machine unavailable. Check its Herdr connection."));
    if (!this.spaceID || !this.terminalCurrent()) {
      const choices = projectSpaces(this.machine, this.project?.repo_root);
      if (!choices.length) return box.child(note("Open a terminal in this project’s checkout in Herdr."));
      box.child(note("Choose the Herdr space for this project."));
      for (const s of choices) box.child(button(`space-${s.id}-${s.setupContext.pane}`, s.name, task => this.root.openProject(this.source, this.project, s, task)));
      return box;
    }
    if (this.needsVerification && !this.verifying && this.panelCurrent()) {
      this.needsVerification = false;
      cx.spawn(task => this.initialize(task));
    }
    if (this.error) return box.child(note(this.error))
      .child(button("ocq-verify-project", "Check project", task => task.spawn(next => this.initialize(next))))
      .child(button("ocq-choose-space", "Choose space", task => this.root.openProject(this.source, this.project, null, task, true)));
    if (!this.updates.binding || this.selected && !this.sessions.readAt) return box.child(note("Reading project…"));
    const main = this.mainClient, scope = this.sessions.bound;
    const ready = this.compatibility.machineReady();
    const open = (kind, request, session, task) => {
      if (this.sessions.current(scope) && main.targetSession(kind, request) === session) main.openConversation(kind, request, task);
      else main.openRefused(task);
    };
    const opening = session => {
      if (main.openIntent?.session !== session || !(main.working || main.tabNavigation || main.error || main.attachmentNotice)) return null;
      const checking = main.working && main.openIntent?.readOnly;
      const pending = main.working || main.tabNavigation;
      return v_flex().gap(4)
        .child(note(checking ? "Checking this opening…" : pending ? "Opening conversation…" :
          main.error || "Attachment is unconfirmed. OCQ won’t repeat it."))
        .when(!!(main.error || main.attachmentNotice || checking), v => v.child(
          button("ocq-check-opening", "Check opening", task => main.checkOpeningStatus(task), ready && !pending)));
    };
    const standalone = this.taskGoal.supported();
    if (standalone) box.child(this.taskGoal.render(cx, t, button));
    if (main.selectedMain) {
      const id = main.selectedMain.session_id;
      const legacy = this.workflow.render(cx, t, button, true, standalone);
      if (legacy) box.child(legacy);
      const row = h_flex().items_center().gap(4).py(5).border_b(1).border_color(t.border)
        .child(div().flex_1().font_semibold().child("Main"))
        .child(button("ocq-open-main", "Open", task => open("integrator", "", id, task),
          ready && !main.working && !main.tabNavigation, "Open Main"));
      if (main.selectedMain.ownership_mode === "ocq_main") row.child(button("ocq-delete-main", "Delete…",
        task => this.requestDelete("integrator", "", id, "Main", task), !this.deleteIntent || this.deleteIntent.state !== "deleting", "Delete Main"));
      box.child(row);
      const status = opening(id);
      if (status) box.child(status);
      const confirmation = this.renderDelete("integrator", "", id, button, note);
      if (confirmation) box.child(confirmation);
    } else if (main.retirement === "uncertain") box.child(note("Main’s deletion could not be confirmed. Ask the machine owner to check."));
    else box.child(this.workflow.render(cx, t, button, false));
    if (!main.openIntent && main.error) box.child(note(main.error));
    for (const row of this.sessions.rows) {
      const state = sessionState(row.job), role = row.kind === "review" ? "Reviewer" : "Worker";
      const line = h_flex().id(`ocq-session-${row.job.session_id || row.request_id}`).items_center().gap(4).py(5).border_b(1).border_color(t.border)
        .child(v_flex().flex_1().min_w(0).gap(2)
          .child(div().truncate().font_semibold().child(row.title))
          .child(note(`${row.goalTitle} · ${state}`)));
      if (row.job.session_id && !row.job.session_state) {
        line.child(button(`ocq-open-${row.job.session_id}`, "Open", task => open("worker", row.request_id, row.job.session_id, task),
          ready && !main.working && !main.tabNavigation, `Open ${role.toLowerCase()}: ${row.title}`));
        if (!row.task_goal) line.child(button(`ocq-delete-${row.job.session_id}`, "Delete…",
          task => this.requestDelete("worker", row.request_id, row.job.session_id, role.toLowerCase(), task),
          !this.deleteIntent || this.deleteIntent.state !== "deleting", `Delete ${role.toLowerCase()}: ${row.title}`));
      }
      box.child(line);
      const status = opening(row.job.session_id);
      if (status) box.child(status);
      const confirmation = this.renderDelete("worker", row.request_id, row.job.session_id, button, note);
      if (confirmation) box.child(confirmation);
    }
    if (main.selectedMain && !this.sessions.rows.length) box.child(note(standalone ? "Task sessions appear here when you start a goal." : "Goal sessions appear here when Main assigns them."));
    return box;
  }
}

function sessionState(job) {
  if (job.session_state === "uncertain") return "Deletion unconfirmed";
  if (job.state === "unknown") return "Needs attention";
  if (job.terminal_proven) return { agent_succeeded: "Job finished", agent_failed: "Job failed", agent_interrupted: "Job interrupted" }[job.outcome] || "Job ended";
  return { queued: "Waiting", launching: "Starting", running: "Running" }[job.state] || "Status unconfirmed";
}
