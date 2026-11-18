import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { spaces, panes, exec } from "herdr";
import { createHash } from "crypto";
import { sourceCompatibility, enabledButton } from "./compatibility.js";
import { WorkOperator } from "./work-operator.js";
import { MainClientControls } from "./main-client.js";
import { OperatorWorkflow } from "./workflow.js";
import { PrivateHCBinding } from "./hc-binding.js";

// Space.folder is a launch default, not its current project. Pick an exact
// terminal in an exact tab; never substitute the active unrelated space.
export function projectSpaces(machine) {
  const live = panes();
  return spaces().filter(s => s.machine === machine).flatMap(s => s.tabs.flatMap(tab =>
    live.filter(p => p.tab === tab.id && p.terminal && p.cwd).map(p => ({
      ...s, folder: p.cwd,
      setupContext: { tab: tab.id, tabName: tab.name, pane: p.pane, terminal: p.terminal, cwd: p.cwd, branch: p.branch },
    }))));
}

// A project detail owns reviews/controllers, not a connection, dispatcher or HC.
// All reads/actions use its source's one stream and an explicit project context.
export class RuntimeProject {
  constructor(source, project, space, cx) {
    this.source = source; this.sourceKey = source.key; this.root = source.owner;
    this.selected = project?.project_id || null; this.project = project;
    this.spaceID = space?.setupContext ? space.id : null;
    this.folder = project?.repo_root || space?.setupContext?.cwd || null;
    this.machine = source.machine; this.space = space;
    this.setupContext = space?.setupContext || null;
    this.generation = 0; this.panelEpoch = source.generation; this.closed = false;
    this.workspaceView = "projects"; this.projectView = "operator";
    this.worktreeCheck = {}; this.publication = {}; this.intake = {}; this.compose = {};
    this.renderTargets = []; this.headerTargets = []; this.handle = this.root.handle;
    this.compatibility = sourceCompatibility(() => this.panelCurrent() && this.spaceID ? {
      id: this.spaceID, machine: this.machine, folder: this.folder,
      source: this.sourceKey, generation: this.source.generation,
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
      get revision() { return source.revision; },
      scope: { machine: this.machine, localSpace: this.localSpace(), folder: this.folder },
      current: () => this.panelCurrent(),
      binding: null,
    };
    this.workOperator = new WorkOperator(this);
    this.mainClient = new MainClientControls(this);
    this.workflow = new OperatorWorkflow(this);
    this.nativeHC = new PrivateHCBinding(this);
    this.initialize(cx);
  }
  localSpace() { return this.spaceID?.startsWith(`${this.machine}/`) && this.machine !== null ? this.spaceID.slice(this.machine.length + 1) : this.spaceID; }
  panelCurrent() {
    return !this.closed && this.root.detail === this && this.source.current() && this.source.generation === this.panelEpoch &&
      (!this.spaceID || spaces().some(s => s.id === this.spaceID && s.machine === this.machine &&
        s.tabs.some(tab => tab.id === this.setupContext?.tab))) &&
      (!this.setupContext || panes().some(p => p.pane === this.setupContext.pane && p.tab === this.setupContext.tab &&
        p.terminal === this.setupContext.terminal && p.cwd === this.setupContext.cwd && p.branch === this.setupContext.branch));
  }
  invalidate() {
    this.generation++; this.compatibility.invalidateObservation("Owning runtime/route changed; review again.");
    this.workOperator.suspend(); this.nativeHC.approved = null;
    this.nativeHC.review = null; this.workflow.review = null;
  }
  async initialize(cx) {
    if (!this.spaceID || !this.folder) return;
    const generation = this.source.generation;
    this.verifyingRepository = true;
    try {
      if (this.setupContext) {
        const repo = await exec("git", ["rev-parse", "--show-toplevel"], { cwd: this.setupContext.cwd, on: this.machine, timeout: 5000 });
        if (!this.panelCurrent() || generation !== this.source.generation) return;
        const folder = repo.stdout?.replace(/\r?\n$/, "");
        if (repo.code !== 0 || !folder || /[\r\n\0]/.test(folder) || this.project && this.project.repo_root !== folder)
          throw Error("Selected terminal repository unavailable");
        this.folder = folder; this.updates.scope.folder = folder;
      }
      const response = await this.updates.connection.command(["compatibility", this.localSpace(), this.folder]);
      if (!this.panelCurrent() || generation !== this.source.generation) return;
      if (response.code !== 0) throw Error("Repository compatibility unavailable");
      const status = this.compatibility.validate(JSON.parse(response.stdout), { id: this.spaceID, machine: this.machine, folder: this.folder });
      if (status.queue_identity !== this.source.runtime.queue_identity || status.image_sha256 !== this.source.runtime.image_sha256 ||
           status.observation_store_context !== this.source.binding?.store_context || ![6, 7].includes(status.observation_schema) ||
          !/^[a-f0-9]{64}$/.test(status.observation_context)) throw Error("Project/source pins changed");
      this.compatibility.observe();
      this.compatibility.status = status; this.compatibility.allowActions = this.source.mutations;
      this.updates.binding = { queue_identity: status.queue_identity, store_context: status.observation_store_context, bridge_context: status.observation_context };
      this.workOperator.sync();
      this.source.subscribe();
    } catch (_) { if (this.panelCurrent() && generation === this.source.generation) this.error = "Cannot verify this terminal's repository. Choose a terminal in the intended repository; the space's launch folder is not used."; }
    finally { this.verifyingRepository = false; }
    cx.notify();
  }
  dispose() { this.closed = true; this.invalidate(); this.workOperator.scroll.release(); }
  resetObservationVersions() { this.invalidate(); }
  refresh(cx) {
    const created = this.workflow.status;
    if (!this.selected && ["create", "adopt"].includes(created?.mode) && created.state === "ready" &&
        created.repo === this.folder && created.project_id && created.session_id) {
      this.root.openProject(this.source, { project_id: created.project_id, repo_root: created.repo, label: this.workflow.label.value() }, this.space, cx);
    } else this.source.subscribe(true);
    cx.notify();
  }
  navigationScope() {
    return { source: this.sourceKey, core: this.source.runtime.core_id, queue: this.source.runtime.queue_identity,
      project: this.selected, sourceGeneration: this.source.generation, generation: this.generation };
  }
  navigationCurrent(scope) {
    return this.panelCurrent() && this.source.online() && JSON.stringify(scope) === JSON.stringify(this.navigationScope());
  }
  select(project, cx) {
    const scope = this.navigationScope();
    if (!this.navigationCurrent(scope)) return;
    cx.spawn(async task => {
      try {
        const response = await this.source.command(["board", project], undefined,
          { project_id: project, space: this.localSpace(), folder: this.folder });
        if (!this.navigationCurrent(scope)) return;
        const board = response.code === 0 && JSON.parse(response.stdout), summary = board?.project;
        if (summary?.project_id !== project || summary.current_binding?.repo_root !== this.folder) throw Error("Project navigation mismatch");
        this.root.openProject(this.source, { ...summary, repo_root: this.folder }, this.space, task);
      } catch (_) { if (this.navigationCurrent(scope)) this.error = "Owning project unavailable. No Main or goal was started."; }
      task.notify();
    });
  }
  inspect(job, cx) { this.openTarget("job", job, cx); }
  openTarget(kind, id, cx) {
    const scope = this.navigationScope();
    if (!this.selected || !this.navigationCurrent(scope) || !["job", "manifest"].includes(kind)) return;
    if (this.navigation?.kind === kind && this.navigation.id === id && this.navigationCurrent(this.navigation.scope)) { cx.notify(); return; }
    const view = { kind, id, scope, page: 0 };
    this.navigation = view;
    if (kind === "job") this.detailJob = id;
    this.source.subscribe();
    cx.spawn(async task => {
      try {
        const response = await this.updates.connection.command([kind, scope.project, id]);
        if (this.navigation !== view || !this.navigationCurrent(scope)) return;
        if (response.code !== 0) throw Error("Stored detail unavailable");
        const value = JSON.parse(response.stdout);
        if (kind === "job" ? value?.project_id !== scope.project || value.job_id !== id :
            typeof value !== "string" || createHash("sha256").update(value).digest("hex") !== id) throw Error("Stored detail identity mismatch");
        view.pages = this.workOperator.pagesFor(value);
      } catch (_) { if (this.navigation === view && this.navigationCurrent(scope)) view.error = "Owning source detail unavailable; retained execution state is unchanged."; }
      task.notify();
    });
    cx.notify();
  }
  panelControl(id, text, action, t, targets = this.headerTargets) {
    const scope = this.navigationScope();
    const run = task => { if (this.navigationCurrent(scope)) action(task); };
    targets.push({ id, run });
    return div().id(id).role("button").accessibility_label(text).px(8).py(5).rounded(5).cursor_pointer()
      .hover(e => e.bg(t.hover)).on_click((_e, cx) => run(cx)).child(text);
  }
  render(cx, t) {
    this.renderTargets = []; this.headerTargets = [];
    const box = v_flex().flex_1().min_h(0).gap(4);
    const scope = this.navigationScope();
    const button = (id, text, action) => {
      const allowed = () => this.navigationCurrent(scope) && enabledButton(id, this.compatibility);
      this.renderTargets.push({ id, run: task => { if (allowed()) action(task); } });
      return div().id(id).role("button").accessibility_label(text).px(8).py(5).rounded(5).cursor_pointer()
        .when(!allowed(), e => e.opacity(0.4)).hover(e => e.bg(t.hover))
        .on_click((_e, task) => { if (allowed()) action(task); }).child(text);
    };
    if (!this.source.online() || !this.panelCurrent()) return box.child(div().p(12).child("Owning runtime or project context changed. Retained details are stale; return to Work and reopen the project."));
    if (!this.spaceID) {
      const choices = projectSpaces(this.machine);
      box.child(div().p(12).child("Choose a terminal folder to verify for this project. Space names and launch folders are not repository identity."));
      for (const s of choices) box.child(this.panelControl(`space-${s.id}-${s.setupContext.pane}`, `${this.machine ?? "This computer"} · ${s.name} · Tab ${s.setupContext.tabName}\n${s.folder}`, task => this.root.openProject(this.source, this.project, s, task), t));
      if (!choices.length) box.child(div().p(12).text_color(t.muted).child("No live terminal folder is available. Open a terminal in the intended repository, then choose it here."));
      return box;
    }
    if (this.error) return box.child(div().p(12).text_color(t.blocked).child(this.error));
    if (this.verifyingRepository) return box.child(div().p(12).child("Verifying the selected terminal's repository…"));
    if (this.navigation) {
      const v = this.navigation, current = this.navigationCurrent(v.scope);
      return box.child(v_flex().flex_1().min_h(0).p(12).gap(6).overflow_y_scroll()
        .child(div().font_semibold().child(v.kind === "job" ? "Stored project job" : "Result manifest"))
        .child(div().text_size(12).child(`${v.scope.project}\n${v.id}`))
        .child(div().child(!current ? "Context changed; return to Work and reopen this project." : v.error || v.pages?.[v.page] || "Reading owning source…"))
        .when(current && !!v.pages, e => e.child(div().text_size(12).child(`Page ${v.page + 1}/${v.pages.length}`))
          .when(v.page > 0, p => p.child(this.panelControl("ocq-read-project-previous", "Previous", task => { v.page--; task.notify(); }, t)))
          .when(v.page < v.pages.length - 1, p => p.child(this.panelControl("ocq-read-project-next", "Next", task => { v.page++; task.notify(); }, t))))
        .child(this.panelControl("ocq-read-project-main", "Return to Main", task => this.select(this.selected, task), t)));
    }
    if (this.selected) box.child(this.workOperator.render(cx, t, button));
    else box.child(v_flex().p(12).overflow_y_scroll().child(this.workflow.render(cx, t, button)));
    return box;
  }
}
