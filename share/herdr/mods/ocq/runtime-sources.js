import { View, div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { machines, spaces, exec, hc, theme, checking, nextCommand, shared, setShared, run, layout } from "herdr";
import { createHash } from "crypto";
import { Buffer } from "buffer";
import { PushConnection } from "./push-connection.js";
import { decimal, compareRevision } from "./observation-version.js";
import { RuntimeProject, projectSpaces } from "./runtime-project.js";

const hex = s => typeof s === "string" && /^[a-f0-9]{64}$/.test(s);
const id = (...parts) => createHash("sha256").update(JSON.stringify(parts)).digest("hex");
const title = machine => machine ?? "This computer";
const activePanel = "__ocq_runtime_panel_v1";

// One physical connection per authenticated runtime. The Machines and Projects
// views never subscribe to jobs, mail or global queue notifications.
class RuntimeSource {
  constructor(owner, runtime, machine, cx) {
    this.owner = owner; this.key = JSON.stringify([runtime.core_id, runtime.queue_identity]);
    this.runtime = runtime; this.machine = machine; this.generation = 0;
    this.projects = { cursor: "0", history: [], items: [] };
    this.start(cx);
  }
  start(cx) {
    this.disposed = false; this.control = 0; this.connectionGeneration = 0; this.bindingEpoch = 0;
    this.scope = Object.freeze({ machine: this.machine, localSpace: this.runtime.space, space: this.runtime.space, folder: this.runtime.folder });
    this.sourceKey = this.key; this.connection = new PushConnection(this);
    this.connection.associate(this.scope, cx);
  }
  current() {
    const route = this.owner.routes.get(this.machine);
    return !this.owner.closed && !this.disposed && route?.key === this.key && route.available;
  }
  online() { return this.current() && this.connection.state === "connected" && !!this.binding && this.health?.state === "current"; }
  publish(state, reason = null) { this.health = { state, reason }; if (!this.owner.closed) this.owner.cx?.notify(); }
  invalidate(reason) {
    this.binding = null; this.lastRequest = null;
    if (this.owner.detail?.source === this) this.owner.detail.invalidate();
    this.publish("unavailable", reason);
  }
  welcome(frame) {
    if (frame.type !== "welcome" || !/^[a-f0-9]{32}$/.test(frame.connection_id) ||
        JSON.stringify(frame.commands) !== JSON.stringify(["subscribe", "unsubscribe", "ping", "close", "command"]) ||
        frame.runtime?.core_id !== this.runtime.core_id || frame.runtime.queue_identity !== this.runtime.queue_identity ||
        frame.descriptor?.queue_identity !== this.runtime.queue_identity || frame.descriptor.image_sha256 !== this.runtime.image_sha256 ||
        !hex(frame.descriptor.observation_context) || !hex(frame.descriptor.observation_store_context) ||
        ![6, 7].includes(frame.descriptor.observation_schema) || typeof frame.mutations !== "boolean") throw Error("Runtime welcome mismatch");
    this.generation++;
    this.connectionID = frame.connection_id; this.sequence = "0"; this.revision = null; this.mutations = frame.mutations;
    this.binding = Object.freeze({ queue_identity: this.runtime.queue_identity, store_context: frame.descriptor.observation_store_context, bridge_context: frame.descriptor.observation_context });
    this.lastRequest = null;
    if (this.owner.detail?.source === this) this.owner.detail.invalidate();
  }
  kick() { this.subscribe(); }
  subscribe(force = false) {
    if (!this.current() || !this.binding || this.connection.state !== "connected") return;
    const detail = this.owner.detail?.source === this ? this.owner.detail : null;
    const captureSessions = detail?.panelCurrent() && detail.sessions.observation();
    const selectors = [{ slot: "projects", kind: "projects", cursor: this.projects.cursor }];
    if (captureSessions) {
      selectors.push({ slot: "board", kind: "board", project_id: detail.selected },
        { slot: "results", kind: "results", project_id: detail.selected, cursor: detail.resultCursor });
    }
    const request = { protocol: "ocq-observe-v1", ...this.binding, selectors,
      ...(captureSessions ? { work: { project_id: detail.selected, fingerprint: "" } } : {}) };
    const key = JSON.stringify(request);
    if (!force && key === this.lastRequest && this.subscription?.detail === detail &&
        this.subscription?.sessions?.generation === captureSessions?.generation) return;
    this.lastRequest = key;
    const capture = { id: String(++this.control), detail, sessions: captureSessions, selectors, generation: this.connectionGeneration };
    this.subscription = capture;
    this.owner.cx.spawn(async cx => {
      try {
        if (!this.current() || capture !== this.subscription || capture.generation !== this.connectionGeneration) return;
        await this.connection.write({ type: "subscribe", id: capture.id, request });
      } catch (_) { if (capture.generation === this.connectionGeneration) await this.connection.disconnect(); }
      if (!this.disposed && !this.owner.closed) cx.notify();
    }, { owner: this.owner });
  }
  message(frame, cx) {
    if (frame.connection_id !== this.connectionID) throw Error("Source connection changed");
    if (frame.type === "pong" || frame.type === "unsubscribed") return;
    if (frame.type !== "snapshot" || !decimal(frame.sequence) || compareRevision(frame.sequence, this.sequence) <= 0) throw Error("Source ordering mismatch");
    this.sequence = frame.sequence;
    const capture = this.subscription, s = frame.snapshot;
    if (!capture || frame.id !== capture.id || capture.generation !== this.connectionGeneration) return;
    if (s?.protocol !== "ocq-observe-v1" || s.status !== "ready" || s.queue_identity !== this.runtime.queue_identity ||
        ![6, 7].includes(s.schema) || s.consistency !== "single_sqlite_snapshot" || !Array.isArray(s.slots) ||
        s.slots.length !== capture.selectors.length || new Set(s.slots.map(v => v.slot)).size !== s.slots.length ||
        capture.selectors.some(v => !s.slots.some(slot => slot.slot === v.slot && slot.kind === v.kind && ["ready", "not_found", "limit_exceeded", "reset_required"].includes(slot.status))) ||
        s.store_context !== this.binding.store_context || s.bridge_context !== this.binding.bridge_context ||
        !decimal(s.revision) || this.revision && compareRevision(s.revision, this.revision) < 0) throw Error("Source snapshot pins changed");
    this.revision = s.revision;
    const slot = s.slots.find(v => v.slot === "projects"), page = this.projects;
    page.error = slot.status === "ready" ? null : slot.status;
    if (!page.error) {
      if (!Array.isArray(slot.data) || !/^k1\.[A-Za-z0-9_-]+$/.test(slot.cursor) || typeof slot.has_more !== "boolean") throw Error("Invalid project page");
      page.items = slot.data; page.next = slot.cursor; page.more = slot.has_more;
    }
    this.publish("current");
    const detail = capture.detail;
    if (detail === this.owner.detail && capture.sessions && detail?.sessions.current(capture.sessions.scope)) {
      const board = s.slots.find(v => v.slot === "board");
      detail.board = board.status === "ready" && board.data?.project?.project_id === detail.selected ? board.data : null;
      if (!s.work) throw Error("Missing project session observation");
      detail.sessions.apply(capture.sessions, s.work, cx, s.main, s.clients);
      detail.observeResults(s.slots.find(v => v.slot === "results"));
    }
    this.connection.attempt = 0;
    this.kick(); cx.notify();
  }
  async command(args, payload, context) {
    const generation = this.generation;
    if (!this.current() || this.connection.state !== "connected") throw Error("Owning machine unavailable");
    const response = await this.connection.command(args, payload, context);
    if (!this.current() || generation !== this.generation) throw Error("Owning source changed; check retained status");
    return response;
  }
  page(cx, direction) {
    const p = this.projects;
    if (!this.online()) return;
    if (direction === "reset") { p.cursor = "0"; p.history = []; }
    else if (direction === "back" && p.history.length) p.cursor = p.history.pop();
    else if (direction === "next" && p.more) { p.history.push(p.cursor); p.cursor = p.next; }
    else return;
    p.error = "reading"; this.subscribe(true); cx.notify();
  }
  async replace(runtime, machine, cx) {
    if (this.replacing) return;
    if (this.machine === machine && ["pid", "start_ticks", "image_sha256", "endpoint", "token"].every(k => this.runtime[k] === runtime[k]) &&
        ["connected", "connecting"].includes(this.connection.state)) return;
    this.replacing = true;
    this.generation++; this.disposed = true; this.invalidate("Runtime route changed");
    try {
      await this.connection.dispose();
      if (this.owner.closed) return;
      this.runtime = runtime; this.machine = machine; this.start(cx);
    } finally { this.replacing = false; }
  }
}

export class RuntimeSources extends View {
  init(_props, cx) {
    this.sources = new Map(); this.routes = new Map(); this.catalog = new Map();
    this.drafts = new Map(); this.notified = new Set();
    this.machineSelection = undefined; this.detail = null;
    this.handle = cx.focus_handle();
    if (!checking()) {
      const previous = globalThis[activePanel];
      globalThis[activePanel] = this;
      if (previous && previous !== this) cx.spawn(task => previous.close(task), { owner: null });
    }
    cx.spawn(async task => { this.cx = task; if (!checking()) await this.discover(task); });
    cx.spawn(async task => {
      for (;;) {
        const command = await nextCommand();
        if (command === "focus") this.handle.focus();
        else if (/^open-[a-f0-9]{64}$/.test(command)) this.detail?.openNotified(command, task);
      }
    });
  }
  async discover(cx) {
    if (this.closed || this.discovering) return;
    this.discovering = true; this.catalogTimer?.cancel();
    try {
      const known = [{ name: null, layout: "ready" }, ...machines().filter(m => m.name !== null)];
      const remoteResult = await hc(["machine", "ls", "--json"]), localResult = await hc(["server", "status", "--json"]);
      if (this.closed) return;
      if (remoteResult.code !== 0 || localResult.code !== 0) throw Error("Herdr machine identities unavailable");
      const remote = JSON.parse(remoteResult.stdout).machines, local = JSON.parse(localResult.stdout).core;
      if (!Array.isArray(remote) || !local?.id) throw Error("Herdr catalog shape unavailable");
      this.catalogFault = false;
      const names = new Set(known.map(m => m.name));
      for (const name of this.catalog.keys()) if (!names.has(name)) {
        this.offline(name); this.catalog.delete(name);
      }
      const queue = known.slice();
      const probe = async () => {
        while (queue.length && !this.closed) {
          const m = queue.shift(), expectedCore = m.name === null ? local.id : remote.find(r => r.name === m.name)?.id;
          const entry = { state: "unavailable" }; this.catalog.set(m.name, entry);
          if (!expectedCore || m.layout !== "ready") { this.offline(m.name); continue; }
          try {
            const result = await exec("ocq-herdr", ["runtime-discover", "--json"], { on: m.name, timeout: 5000 });
            if (this.closed) return;
            if (result.code !== 0 || Buffer.from(result.stdout).length > 32768) throw Error();
            const reply = JSON.parse(result.stdout);
            if (reply.protocol !== "ocq-runtime-v1") throw Error();
            if (reply.state !== "available") {
              entry.state = reply.state === "absent" ? "absent" : "unavailable";
              this.offline(m.name); continue;
            }
            const d = reply.runtime;
            if (d.protocol !== "ocq-runtime-v1" || d.core_id !== expectedCore || !hex(d.queue_identity) ||
                !hex(d.token) || !hex(d.image_sha256) || typeof d.endpoint !== "string" ||
                typeof d.local_only !== "boolean" || !Number.isSafeInteger(d.pid) || typeof d.start_ticks !== "string") throw Error();
            if (d.local_only && m.name !== null) { this.offline(m.name); continue; }
            const key = JSON.stringify([d.core_id, d.queue_identity]);
            const old = this.routes.get(m.name);
            if (old?.key && old.key !== key) this.offline(m.name);
            this.routes.set(m.name, { key, available: true }); entry.state = "available";
            const source = this.sources.get(key);
            if (!source) this.sources.set(key, new RuntimeSource(this, d, m.name, cx));
            else if (source.machine === m.name || !this.routes.get(source.machine)?.available)
              void source.replace(d, m.name, cx).catch(() => source.publish("unavailable", "Prior source did not settle"));
          } catch (_) { entry.state = "unavailable"; this.offline(m.name); }
          cx.notify();
        }
      };
      await Promise.all([probe(), probe()]);
    } catch (_) {
      for (const name of this.routes.keys()) this.offline(name);
      this.catalogFault = true;
    } finally {
      this.discovering = false; cx.notify();
      if (!this.closed) this.catalogTimer = cx.timer.after(30000, task => { task.spawn(async next => this.discover(next)); });
    }
  }
  offline(machine) {
    const route = this.routes.get(machine);
    if (!route) return;
    route.available = false;
    const source = this.sources.get(route.key);
    if (source?.machine === machine) { source.invalidate("Machine unavailable"); void source.connection.disconnect(); }
  }
  draftKey(source, project, space) {
    return JSON.stringify([source.key, project?.project_id ?? null, project?.repo_root || space?.folder]);
  }
  rememberDraft() {
    const d = this.detail;
    if (d) this.drafts.set(this.draftKey(d.source, d.project, d.space), { ...d.workflow.draftText });
  }
  openProject(source, project, space, cx, chooseSpace = false) {
    if (!source.online()) return;
    this.rememberDraft(); this.detail?.dispose();
    const matches = project ? projectSpaces(source.machine, project.repo_root) : [];
    const exact = matches.filter(s => s.folder === project?.repo_root);
    const selected = space || (!chooseSpace && (exact.find(s => s.active) || (exact.length === 1 ? exact[0] : null)));
    this.detail = new RuntimeProject(source, project, selected);
    const draft = this.drafts.get(this.draftKey(source, project, selected));
    if (draft) this.detail.workflow.restoreDraft(draft);
    this.setup = false; this.focusTarget = null;
    source.subscribe(true); cx.notify();
  }
  back(cx) {
    if (this.detail) {
      const source = this.detail.source;
      this.rememberDraft(); this.detail.dispose(); this.detail = null;
      source.subscribe(true);
    } else if (this.setup) this.setup = false;
    else this.machineSelection = undefined;
    this.focusTarget = null; cx.notify();
  }
  button(key, text, action, t, enabled = true, label = text) {
    const invoke = cx => { this.handle.focus(); this.focusTarget = key; action(cx); cx.notify(); };
    if (enabled) this.targets.push({ key, invoke });
    return div().id(key).min_w(0).role("button").accessibility_label(label).px(8).py(6).rounded(5)
      .when(enabled, e => e.cursor_pointer().hover(e => e.bg(t.hover)).on_click((_, cx) => invoke(cx)))
      .when(enabled && this.focusTarget === key, e => e.bg(t.hover))
      .when(!enabled, e => e.text_color(t.muted)).child(div().truncate().child(text));
  }
  key(event, cx) {
    // Only handle navigation when the panel itself owns focus. In particular,
    // never consume Space or Enter from the native goal field.
    if (!this.handle.is_focused() || this.detail?.workflow.focused || event.modifiers.control ||
        event.modifiers.alt || event.modifiers.platform) return;
    if (event.key === "escape" && this.machineSelection !== undefined) this.back(cx);
    else if (["down", "up", "tab"].includes(event.key) && this.targets.length) {
      let at = this.targets.findIndex(t => t.key === this.focusTarget);
      const delta = event.key === "up" || event.key === "tab" && event.modifiers.shift ? -1 : 1;
      if (at < 0) at = delta > 0 ? -1 : 0;
      this.focusTarget = this.targets[(at + delta + this.targets.length) % this.targets.length].key;
    } else if (event.key === "enter" && !event.is_held) {
      this.targets.find(t => t.key === this.focusTarget)?.invoke(cx);
    } else return;
    cx.stop_propagation(); cx.notify();
  }
  render(cx) {
    const transition = shared("ocq.panel.transition");
    const findPanel = node => node?.id === "ocq-panel" ? node : (node?.children || []).map(findPanel).find(Boolean);
    if (!transition || !findPanel(layout())) return this.renderContents(cx);
    const space = spaces().find(s => s.active)?.id;
    const moving = transition.space === space && transition.moving, collapsing = moving && !transition.shown;
    return v_flex().id("ocq-panel-motion").size_full().flex_none().min_w(0).min_h(0).overflow_hidden()
      .when(collapsing, e => e.w(0).transition("width", { duration: 180, easing: "ease-out" }))
      .child(v_flex().id(`ocq-panel-viewport-${transition.revision}`).size_full().min_w(0).min_h(0).on_size_change(size => {
        const current = shared("ocq.panel.transition");
        if (!current || current.revision !== transition.revision || current.space !== space || spaces().find(s => s.active)?.id !== space) return;
        if (current.shown) {
          if (size.width > 0 && (current.moving || current.width !== size.width))
            setShared("ocq.panel.transition", { ...current, moving: false, width: size.width });
        } else if (moving && current.moving) {
          run("layout.resize", { id: "ocq-panel", px: size.width });
          if (size.width === 0) {
            run("layout.hide", { id: "ocq-panel" });
            setShared("ocq.panel.transition", { ...current, moving: false });
          }
        }
      }).child(this.renderContents(cx).w_full().when(collapsing, e => e.w(transition.width)).flex_none()));
  }
  renderContents(cx) {
    const t = theme(), signature = JSON.stringify(machines());
    if (signature !== this.catalogSignature) {
      this.catalogSignature = signature;
      if (this.cx && !checking()) cx.spawn(task => this.discover(task));
    }
    this.targets = [];
    const button = (key, text, action, enabled) => this.button(key, text, action, t, enabled);
    const root = v_flex().size_full().min_w(0).min_h(0).bg(t.background).text_color(t.foreground).text_size(13)
      .track_focus(this.handle).on_key_down((event, task) => this.key(event, task));
    const selected = this.machineSelection !== undefined;
    let heading = selected ? "Projects" : "Machines";
    if (this.setup) heading = "Choose a space";
    if (this.detail) heading = this.detail.project?.label || "New project";
    const header = h_flex().flex_none().items_center().gap(6).px(8).py(5).border_b(1).border_color(t.border);
    if (selected) header.child(button("ocq-all-back", "Back", task => this.back(task)));
    const labels = v_flex().flex_1().min_w(0).child(div().truncate().text_size(16).font_semibold().child(heading));
    if (selected) labels.child(div().truncate().text_size(12).text_color(t.muted).child(title(this.machineSelection)));
    root.child(header.child(labels));
    if (this.detail) return root.child(this.detail.render(cx, t));
    const body = v_flex().id(selected ? "ocq-projects" : "ocq-machines").flex_1().min_h(0).overflow_y_scroll().p(12).gap(4);
    const note = text => body.child(div().text_color(t.muted).child(text));
    if (!selected) {
      for (const [machine] of this.catalog) body.child(button(id("machine", machine), title(machine),
        task => { this.machineSelection = machine; this.focusTarget = null; task.notify(); }));
      if (this.discovering && !this.catalog.size) note("Finding Herdr machines…");
      if (this.catalogFault) note("Machines unavailable. Check Herdr’s connection.");
      return root.child(body);
    }
    const route = this.routes.get(this.machineSelection), source = this.sources.get(route?.key);
    if (!source?.online() || !route?.available || source.machine !== this.machineSelection) {
      note(this.catalog.get(this.machineSelection)?.state === "absent" ? "OCQ is not set up on this machine." :
        "Project connection unavailable. Check this machine in Herdr.");
      return root.child(body);
    }
    if (this.setup) {
      const choices = projectSpaces(source.machine);
      if (!choices.length) note("Open a terminal in the project checkout in Herdr.");
      for (const s of choices) body.child(button(id(source.key, s.id, s.setupContext.pane), `${s.name} · ${s.setupContext.tabName}`,
        task => this.openProject(source, null, s, task)));
      return root.child(body);
    }
    const page = source.projects;
    if (page.error) {
      note(page.error === "reading" ? "Reading projects…" : "Could not read projects.");
      if (page.error !== "reading") body.child(button("ocq-reload-projects", "Reload projects", task => source.page(task, "reset")));
      return root.child(body);
    }
    if (!page.items.length) note("No projects on this machine yet.");
    for (const project of page.items) body.child(button(id(source.key, project.project_id), project.label,
      task => this.openProject(source, project, null, task)));
    if (page.history.length) body.child(button("ocq-projects-previous", "Previous projects", task => source.page(task, "back")));
    if (page.more) body.child(button("ocq-projects-next", "More projects", task => source.page(task, "next")));
    if (source.mutations) body.child(button("ocq-new-project", "Set up project", task => { this.setup = true; task.notify(); }));
    return root.child(body);
  }
  async close(cx) {
    if (this.closed) return;
    this.closed = true; this.catalogTimer?.cancel(); this.detail?.dispose();
    await Promise.allSettled([...this.sources.values()].map(s => { s.disposed = true; return s.connection.dispose(cx); }));
  }
}
