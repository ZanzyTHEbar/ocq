import { View, div } from "gpui-kit";
import { h_flex, v_flex, ScrollHandle } from "gpui-base";
import { machines, spaces, exec, hc, theme, checking, nextCommand, shared, setShared, run, layout } from "herdr";
import { createHash } from "crypto";
import { Buffer } from "buffer";
import { PushConnection } from "./push-connection.js";
import { decimal, compareRevision } from "./stored-updates.js";
import { RuntimeProject, projectSpaces } from "./runtime-project.js";

const hex = s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s);
const id = (...parts) => createHash("sha256").update(JSON.stringify(parts)).digest("hex");
const title = machine => machine ?? "This computer";
const kinds = { projects: "projects", jobs: "runtime-jobs", events: "runtime-notifications", inbox: "all-inbox" };

// One source owns one stream, cursors and last observed pages. No queue data,
// credentials or authority is kept in localStorage or routed by a display label.
class RuntimeSource {
  constructor(owner, runtime, machine, cx) {
    this.owner = owner; this.key = JSON.stringify([runtime.core_id, runtime.queue_identity]);
    this.runtime = runtime; this.machine = machine; this.generation = 0;
    this.pages = Object.fromEntries(Object.keys(kinds).map(k => [k, { cursor: "0", history: [], items: [], loaded: false }]));
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
  publish(state, reason = null) { this.health = { state, reason }; this.owner.cx?.notify(); }
  invalidate(reason) {
    this.binding = null; this.lastRequest = null;
    if (this.owner.detail?.source === this) this.owner.detail.invalidate();
    this.publish("unavailable", reason);
  }
  welcome(frame) {
    if (frame.type !== "welcome" || !/^[a-f0-9]{32}$/.test(frame.connection_id) ||
        JSON.stringify(frame.commands) !== JSON.stringify(["subscribe", "unsubscribe", "ping", "close", "command"]) ||
        frame.runtime?.core_id !== this.runtime.core_id || frame.runtime.queue_identity !== this.runtime.queue_identity ||
        frame.descriptor?.queue_identity !== this.runtime.queue_identity ||
        frame.descriptor.image_sha256 !== this.runtime.image_sha256 ||
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
    const selectors = Object.entries(kinds).map(([slot, kind]) => ({ slot, kind, cursor: this.pages[slot].cursor }));
    if (detail?.selected) selectors.push({ slot: "board", kind: "board", project_id: detail.selected });
    const request = { protocol: "ocq-observe-v1", ...this.binding, selectors,
      ...(detail?.selected ? { work: { project_id: detail.selected, fingerprint: "" } } : {}) };
    const key = JSON.stringify(request);
    const work = detail?.workOperator.observation();
    if (!force && key === this.lastRequest) {
      if (this.subscription) { this.subscription.detail = detail; this.subscription.work = work; }
      return;
    }
    this.lastRequest = key;
    const capture = { id: String(++this.control), detail, work, selectors, generation: this.connectionGeneration };
    this.subscription = capture;
    this.owner.cx.spawn(async cx => {
      try {
        if (!this.current() || capture !== this.subscription || capture.generation !== this.connectionGeneration) return;
        await this.connection.write({ type: "subscribe", id: capture.id, request });
      }
      catch (_) { if (capture.generation === this.connectionGeneration) await this.connection.disconnect(); }
      cx.notify();
    });
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
    for (const slot of s.slots) {
      if (slot.slot === "board") {
        if (capture.detail === this.owner.detail && slot.status === "ready") capture.detail.board = slot.data;
        continue;
      }
      const page = this.pages[slot.slot];
      if (!page || slot.kind !== kinds[slot.slot]) throw Error("Unexpected source selector");
      if (slot.status !== "ready") { page.error = slot.status; continue; }
       const validCursor = slot.kind === "projects" ? typeof slot.cursor === "string" && /^k1\.[A-Za-z0-9_-]+$/.test(slot.cursor) : decimal(slot.cursor);
       if (!Array.isArray(slot.data) || !validCursor || typeof slot.has_more !== "boolean") throw Error("Invalid source page");
      page.items = slot.data; page.next = slot.cursor; page.more = slot.has_more;
      page.loaded = true; page.error = null;
    }
    const detail = capture.detail;
    if (detail === this.owner.detail && detail?.panelCurrent() && s.work && capture.work)
      detail.workOperator.apply(capture.work, s.work, s.revision, cx, s.main, s.clients);
    const created = detail?.workflow.status;
    if (detail === this.owner.detail && !detail?.selected && created?.project_id) {
      const project = this.pages.projects.items.find(p => p.project_id === created.project_id);
      if (project) this.owner.openProject(this, project, detail.space, cx);
    }
    this.publish("current"); cx.notify();
    this.connection.attempt = 0;
    this.kick();
  }
  async command(args, payload, context) {
    const generation = this.generation;
    if (!this.current() || this.connection.state !== "connected") throw Error("Owning source is offline; no action sent");
    const response = await this.connection.command(args, payload, context);
    if (!this.current() || generation !== this.generation) throw Error("Owning source changed; outcome requires exact recovery, never resend");
    return response;
  }
  page(kind, cx, back = false) {
    const p = this.pages[kind];
    if (!this.online() || (back ? !p.history.length : !p.more)) return;
    if (back) p.cursor = p.history.pop();
    else { p.history.push(p.cursor); p.cursor = p.next; }
    p.error = "reading";
    this.subscribe(); cx.notify();
  }
  async replace(runtime, machine, cx) {
    if (this.replacing) return;
    if (this.machine === machine && ["pid", "start_ticks", "image_sha256", "endpoint", "token"].every(k => this.runtime[k] === runtime[k]) &&
        ["connected", "connecting"].includes(this.connection.state)) return;
    this.replacing = true;
    this.generation++; this.disposed = true; this.invalidate("Runtime route/incarnation changed; prior reviews invalid.");
    try {
      await this.connection.dispose(); // An unsettled old owner keeps only this source unavailable.
      if (this.owner.closed) return;
      this.runtime = runtime; this.machine = machine; this.start(cx);
    } finally { this.replacing = false; }
  }
}

export class RuntimeSources extends View {
  init(_props, cx) {
    this.sources = new Map(); this.routes = new Map(); this.catalog = new Map();
    this.view = "work"; this.filter = null; this.detail = null; this.inspection = null;
    this.handle = cx.focus_handle();
    this.jobsScroll = ScrollHandle.new(); this.eventsScroll = ScrollHandle.new();
    this.workScroll = ScrollHandle.new(); this.mailScroll = ScrollHandle.new();
    this.catalogSignature = null;
    cx.spawn(async task => { this.cx = task; if (!checking()) await this.discover(task); });
    cx.spawn(async () => { for (;;) { if (await nextCommand() === "focus") this.handle.focus(); } });
  }
  async discover(cx) {
    if (this.closed || this.discovering) return;
    this.discovering = true;
    try {
      const known = [{ name: null, layout: "ready" }, ...machines().filter(m => m.name !== null)];
      const remoteResult = await hc(["machine", "ls", "--json"]), localResult = await hc(["server", "status", "--json"]);
      if (this.closed) return;
      if (remoteResult.code !== 0 || localResult.code !== 0) throw Error("Herdr machine identities unavailable");
      const remote = JSON.parse(remoteResult.stdout).machines, local = JSON.parse(localResult.stdout).core;
      if (!Array.isArray(remote) || !local?.id) throw Error("Herdr catalog shape unavailable");
      this.catalogFault = null;
      const names = new Set(known.map(m => m.name));
      for (const name of this.catalog.keys()) if (!names.has(name)) {
        this.offline(name, "Machine removed from Herdr catalog");
        this.catalog.delete(name);
      }
      const queue = known.slice();
      const probe = async () => {
        while (queue.length && !this.closed) {
          const m = queue.shift(), expectedCore = m.name === null ? local?.id : remote.find(r => r.name === m.name)?.id;
          const entry = { machine: m.name, expectedCore, state: "unavailable" }; this.catalog.set(m.name, entry);
          if (!expectedCore || m.layout !== "ready") { this.offline(m.name, "Herdr machine unavailable"); continue; }
          try {
            const result = await exec("ocq-herdr", ["runtime-discover", "--json"], { on: m.name, timeout: 5000 });
            if (this.closed) return;
            if (result.code !== 0 || Buffer.from(result.stdout).length > 32768) throw Error("Runtime discovery unavailable or unsupported");
            const reply = JSON.parse(result.stdout);
            if (reply.protocol !== "ocq-runtime-v1") throw Error("Unsupported runtime discovery");
            if (reply.state !== "available") {
              entry.state = ["absent", "unsupported", "unavailable"].includes(reply.state) ? reply.state : "unavailable";
              this.offline(m.name, entry.state); continue;
            }
            const d = reply.runtime;
            if (d.protocol !== "ocq-runtime-v1" || d.core_id !== expectedCore || !hex(d.queue_identity) ||
                !hex(d.token) || !hex(d.image_sha256) || typeof d.endpoint !== "string" ||
                typeof d.local_only !== "boolean" || !Number.isSafeInteger(d.pid) || typeof d.start_ticks !== "string")
              throw Error("Runtime and Herdr core identity differ");
            const key = JSON.stringify([d.core_id, d.queue_identity]);
            entry.key = key; entry.state = d.local_only && m.name !== null ? "runtimeDetectedConnectionUnavailable" : "available";
            if (entry.state !== "available") { this.offline(m.name, entry.state); continue; }
            const old = this.routes.get(m.name);
            if (old?.key && old.key !== key) {
              const previous = this.sources.get(old.key);
              previous?.invalidate("Herdr alias now names another runtime; retained data is stale.");
              if (previous?.machine === m.name) void previous.connection.disconnect();
            }
            this.routes.set(m.name, { key, available: true });
            let source = this.sources.get(key);
            if (!source) {
              source = new RuntimeSource(this, d, m.name, cx); this.sources.set(key, source);
            } else if (source.machine === m.name || !this.routes.get(source.machine)?.available) {
              void source.replace(d, m.name, cx).catch(() => source.publish("unavailable", "Prior source did not settle"));
            }
          } catch (_) { entry.state = "unavailable"; this.offline(m.name, "Runtime discovery unavailable"); }
          cx.notify();
        }
      };
      await Promise.all([probe(), probe()]); // bounded catalog work, never queue polling
    } catch (_) {
      for (const name of this.routes.keys()) this.offline(name, "Herdr catalog unavailable");
      this.catalogFault = "Herdr machine identities unavailable; retained sources are stale.";
    } finally {
      this.discovering = false; cx.notify();
      if (!this.closed) this.catalogTimer = cx.timer.after(30000, task => { task.spawn(async next => this.discover(next)); });
    }
  }
  offline(machine, reason) {
    const route = this.routes.get(machine);
    if (route) {
      route.available = false;
      const source = this.sources.get(route.key);
      if (source?.machine === machine) { source.invalidate(reason); void source.connection.disconnect(); }
    }
  }
  openProject(source, project, space, cx) {
    if (!source.current()) return;
    this.detail?.dispose(); this.inspection = null;
    const matches = projectSpaces(source.machine).filter(s => s.folder === project?.repo_root);
    this.detail = new RuntimeProject(source, project, space || (matches.length === 1 ? matches[0] : null), cx);
    source.subscribe(true); cx.notify();
  }
  back(cx) { const source = this.detail?.source; this.detail?.dispose(); this.detail = null; this.inspection = null; source?.subscribe(true); cx.notify(); }
  inspect(source, kind, item, cx) {
    const view = { source, kind, item, generation: source.generation };
    this.inspection = view;
    if (source.online()) cx.spawn(async task => {
      try {
        const args = kind === "inbox" ? ["mail", item.project_id, item.id, item.sequence] :
          [kind === "jobs" ? "queue-job" : "notification", item.id];
        const response = await source.command(args);
        if (this.inspection !== view || view.generation !== source.generation) return;
        if (response.code !== 0) throw Error("Stored detail unavailable");
        view.detail = JSON.parse(response.stdout);
      } catch (_) { if (this.inspection === view) view.error = "Owning source detail unavailable. Retained observation is unchanged; nothing was acknowledged."; }
      task.notify();
    });
    cx.notify();
  }
  button(key, text, action, t) {
    return div().id(key).role("button").accessibility_label(text).px(8).py(5).rounded(5).cursor_pointer()
      .hover(e => e.bg(t.hover)).on_click((_e, cx) => action(cx)).child(text);
  }
  row(source, kind, item, t) {
    const entity = kind === "projects" ? item.project_id : item.id;
    const projects = kind === "projects" ? [item.project_id] : item.project_ids || (item.project_id ? [item.project_id] : []);
    const labels = projects.map(p => source.pages.projects.items.find(v => v.project_id === p)?.label || p).join(", ") || "No project association";
    const first = kind === "projects" ? item.label || item.project_id : kind === "inbox" ? item.subject : `${item.id} · ${item.state || item.outcome}`;
    return v_flex().id(id(source.key, kind, entity)).py(3).border_b(1).border_color(t.border)
      .child(this.button(id(source.key, kind, entity, "open"), first, cx => {
        if (kind === "projects") this.openProject(source, item, null, cx);
        else this.inspect(source, kind, item, cx);
      }, t))
      .child(div().px(8).text_size(12).text_color(t.muted).child(`${title(source.machine)} · ${labels}${source.online() && !source.pages[kind].error ? "" : " · stale"}`));
  }
  region(kind, heading, t) {
    const region = v_flex().flex_1().min_h(0).gap(4).overflow_y_scroll()
      .when(kind === "jobs", e => e.track_scroll(this.jobsScroll))
      .when(kind === "events", e => e.track_scroll(this.eventsScroll))
      .when(kind === "projects", e => e.track_scroll(this.workScroll))
      .when(kind === "inbox", e => e.track_scroll(this.mailScroll))
      .child(div().px(8).font_semibold().child(heading));
    for (const source of this.sources.values()) {
      if (this.filter && source.key !== this.filter) continue;
      const page = source.pages[kind];
      const generation = source.generation;
      for (const item of page.items) region.child(this.row(source, kind, item, t));
      if (!page.error && !page.items.length) {
        const first = page.cursor === "0";
        const empty = kind === "projects" ? (first ? "No projects registered. Use Set up project to review a Main." : "No projects on this page.") :
          kind === "jobs" ? (first ? "No OCQ jobs submitted. In Work, create Main and give it a goal; other OpenCode sessions are not tracked automatically." : "No jobs on this page.") :
          kind === "inbox" ? "No project mail on this page. Submitted work and reviews produce mail; an empty inbox is not completion." :
          "No pending notifications on this page.";
        region.child(div().px(8).text_color(t.muted).child(`${title(source.machine)} · ${!source.online() ? "Source unavailable; no current observation. Retained data may be stale." : !page.loaded ? "Reading stored data…" : empty}`));
      }
      if (page.error) region.child(div().px(8).text_color(page.error === "reading" ? t.muted : t.blocked).child(`${title(source.machine)} · ${page.error === "reading" ? "Reading stored page…" : page.error === "reset_required" ? "Queue changed; restart paging." : "Page unavailable; retained data may be stale"}`));
       if (page.error === "reset_required") region.child(this.button(id(source.key, kind, "reset"), "Restart pages", cx => {
         if (!source.online() || source.generation !== generation || source.pages[kind] !== page) return;
        page.cursor = "0"; page.history = []; page.error = "reading"; source.subscribe(true); cx.notify();
      }, t));
      if (page.more) region.child(this.button(id(source.key, kind, "next"), `More ${heading.toLowerCase()} · ${title(source.machine)}`, cx => source.page(kind, cx), t));
      if (page.history.length) region.child(this.button(id(source.key, kind, "previous"), `Previous ${heading.toLowerCase()} · ${title(source.machine)}`, cx => source.page(kind, cx, true), t));
    }
    return region;
  }
  render(cx) {
    const transition = shared("ocq.panel.transition");
    const findPanel = node => node?.id === "ocq-panel" ? node : (node?.children || []).map(findPanel).find(Boolean);
    const panel = findPanel(layout());
    if (!transition || !panel) return this.renderContents(cx);
    const space = spaces().find(s => s.active)?.id;
    const moving = !!transition && transition.space === space && transition.moving;
    const target = transition.shown === false ? 0 : moving ? transition.width : panel.size?.px ?? transition.width;
    return v_flex().id("ocq-panel-motion").w(target).h_full().flex_none().min_w(0).min_h(0)
      .overflow_hidden().transition("width", { duration: 180, easing: "ease-out" })
      .child(v_flex().id(`ocq-panel-viewport-${transition?.revision ?? "open"}`).size_full().min_w(0).min_h(0)
      .on_size_change(size => {
        const current = shared("ocq.panel.transition");
        if (!moving || !current?.moving || current.revision !== transition.revision || current.space !== space ||
            spaces().find(s => s.active)?.id !== space) return;
        run("layout.resize", { id: "ocq-panel", px: size.width });
        if (size.width === target) {
          if (!current.shown) run("layout.hide", { id: "ocq-panel" });
          setShared("ocq.panel.transition", { ...current, moving: false });
        }
      })
      .child(this.renderContents(cx).w(moving ? transition.width : target).flex_none()));
  }
  renderContents(cx) {
    const t = theme(), signature = JSON.stringify(machines());
    if (signature !== this.catalogSignature) {
      this.catalogSignature = signature;
      if (this.cx && !checking()) { this.catalogTimer?.cancel(); cx.spawn(async task => this.discover(task)); }
    }
    const root = v_flex().size_full().min_h(0).bg(t.background).text_color(t.foreground).text_size(13).track_focus(this.handle);
    root.child(h_flex().flex_none().px(8).py(4).items_center()
      .child(div().flex_1().font_semibold().child(this.detail ? `${title(this.detail.machine)} · ${this.detail.project?.label || "New project"}` : "All machines"))
      .child(this.button("ocq-all-details", this.details ? "Done" : "Details", task => { this.details = !this.details; task.notify(); }, t)));
    if (this.detail || this.inspection) root.child(this.button("ocq-all-back", "Back to all machines", task => this.back(task), t));
    else root.child(h_flex().flex_none().px(4).gap(2).children([["work", "Work"], ["queue", "Queue"], ["inbox", "Inbox"]].map(([name, text]) =>
      this.button("ocq-all-"+name, text, task => { this.view = name; task.notify(); }, t).when(this.view === name, e => e.bg(t.hover).font_semibold()))));
    if (this.details) {
      const info = v_flex().px(12).max_h(180).overflow_y_scroll();
      const groups = new Map();
      const group = (core, machine) => {
        const key = JSON.stringify(core ? ["core", core] : ["machine", machine]);
        if (!groups.has(key)) groups.set(key, { core, names: new Set(), entries: [], sources: [] });
        const value = groups.get(key);
        value.names.add(title(machine));
        return value;
      };
      for (const entry of this.catalog.values()) group(entry.expectedCore, entry.machine).entries.push(entry);
      for (const source of this.sources.values()) group(source.runtime.core_id, source.machine).sources.push(source);
      for (const value of groups.values()) {
        info.child(div().text_size(12).font_semibold().child(
          `${[...value.names].join(" / ")}${value.core ? ` · core ${value.core}` : ""}`));
        for (const entry of value.entries) {
          if (entry.state === "available" && value.sources.some(source => source.key === entry.key)) continue;
          info.child(div().pl(12).text_size(12).child(`${title(entry.machine)} · ${entry.state}`));
        }
        for (const source of value.sources)
          info.child(div().pl(12).text_size(12).child(
            `Queue ${source.runtime.queue_identity} · ${source.online() ? "connected" : "connection unavailable; retained observations are stale"}`));
      }
      if (this.catalogFault) info.child(div().text_color(t.blocked).child(this.catalogFault));
      root.child(info);
    }
    if (this.detail) return root.child(this.detail.render(cx, t));
    if (this.inspection) {
      const v = this.inspection;
      const projects = v.item.project_ids || (v.item.project_id ? [v.item.project_id] : []);
      return root.child(v_flex().flex_1().min_h(0).overflow_y_scroll().p(12).gap(6)
        .child(div().font_semibold().child(`${title(v.source.machine)} · ${v.kind === "events" ? "Notification" : v.kind === "inbox" ? "Project mail" : "Job"}`))
        .child(div().text_size(12).child(projects.join(", ") || "No project association"))
        .child(div().text_color(t.muted).child(v.source.online() && v.generation === v.source.generation ? "Stored observation · opening does not acknowledge" : "Stale observation · owning source unavailable or changed"))
        .when(!!v.error, e => e.child(div().text_color(t.blocked).child(v.error)))
        .child(div().child(JSON.stringify(v.detail || v.item, null, 2))));
    }
    if (!this.sources.size) {
      const states = [...this.catalog.values()].map(e => e.state);
      const absent = !this.catalogFault && states.length > 0 && states.every(s => s === "absent");
      root.child(div().px(12).py(8).child(this.discovering ? "Looking for existing OCQ runtimes…" :
        absent ? "No registered OCQ runtimes found. Discovery does not install or start them." :
        states.includes("runtimeDetectedConnectionUnavailable") ? "No connected OCQ runtimes. A runtime was detected, but its endpoint is unavailable. Check Details." :
        "No current queue observation. Discovery is unavailable; existing runtime state is unknown. Check Details."));
    }
    if (this.view === "work") {
      root.child(this.region("projects", "Work", t));
      for (const source of this.sources.values()) if (source.online()) {
        const choices = projectSpaces(source.machine);
        if (this.newOn === source.key) {
          root.child(div().px(8).text_size(12).text_color(t.muted).child("Choose a Herdr space and terminal folder to verify—not a project name."));
          for (const s of choices) root.child(this.button(id(source.key, s.id, s.setupContext.pane, "setup"), `${title(source.machine)} · ${s.name} · Tab ${s.setupContext.tabName}\n${s.folder}`, task => this.openProject(source, null, s, task), t));
          if (!choices.length) root.child(div().px(8).child("No live terminal folder available. Open a terminal in the intended repository; stored launch folders are not used."));
        }
        root.child(this.button(id(source.key, "new"), `Set up project · ${title(source.machine)}`, task => { this.newOn = this.newOn === source.key ? null : source.key; task.notify(); }, t));
      }
    } else {
      root.child(v_flex().flex_1().min_h(0).gap(8)
        .child(this.region(this.view === "queue" ? "jobs" : "inbox", this.view === "queue" ? "Jobs" : "Project mail", t))
        .child(this.region("events", "Notifications", t)));
      root.child(this.button("ocq-all-go-work", "Go to Work", task => { this.view = "work"; task.notify(); }, t));
    }
    const unavailable = new Set([...this.catalog.values()].filter(e => !["available", "absent"].includes(e.state)).map(e => e.machine));
    for (const source of this.sources.values()) if (!source.online()) unavailable.add(source.machine);
    if (unavailable.size) root.child(this.button("ocq-all-unavailable", `${unavailable.size} machine${unavailable.size === 1 ? "" : "s"} unavailable · Details`,
      task => { this.details = true; task.notify(); }, t).flex_none().text_size(12));
    return root;
  }
  async close() {
    this.closed = true; this.catalogTimer?.cancel(); this.detail?.dispose();
    this.jobsScroll.release(); this.eventsScroll.release();
    this.workScroll.release(); this.mailScroll.release();
    await Promise.allSettled([...this.sources.values()].map(s => { s.disposed = true; return s.connection.dispose(); }));
  }
}
