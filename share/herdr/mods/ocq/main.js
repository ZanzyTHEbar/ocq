import { View, div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { exec, nextCommand, spaces, theme } from "herdr";

function emptyCollection() {
  return { items: [], readTimes: {}, cursor: "0", readAt: null, loaded: false, loading: false, hasMore: false, error: null };
}

// The bridge is a short-lived Go process. OCQ's database remains authoritative;
// no Herdr status, pane state, or mod state is interpreted as job completion.
export default class OCQ extends View {
  init(_props, cx) {
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
    cx.timer.every(15000, (cx) => this.refresh(cx));
  }

  clearInspection() {
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
  }

  refresh(cx) {
    if (this.loading) return;
    const active = spaces().find((s) => s.active);
    if (!active) {
      this.error = null;
      cx.notify();
      return;
    }
    if (active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) { cx.notify(); return; }
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
            if (!this.collections[kind].loaded && !this.collections[kind].loading) this.loadCollection(kind, false, cx);
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
    cx.notify();
  }

  loadCollection(kind, reset, cx) {
    const active = spaces().find((s) => s.active);
    if (!this.selected || !active || active.id !== this.spaceID || active.machine !== this.machine || active.folder !== this.folder) return;
    if (reset) this.collections[kind] = emptyCollection();
    const collection = this.collections[kind];
    if (collection.loading) return;
    const projectID = this.selected, machine = this.machine;
    collection.loading = true;
    collection.error = null;
    cx.spawn(async (cx) => {
      try {
        const response = await exec("ocq-herdr", [kind, projectID, collection.cursor], { on: machine });
        if (response.code !== 0) throw new Error(response.stderr || `Cannot read ${kind}`);
        const page = JSON.parse(response.stdout);
        if (this.collections[kind] !== collection) return;
        const items = kind === "inbox" ? page.messages : page.items;
        collection.items = collection.items.concat(items);
        for (const item of items) collection.readTimes[kind === "inbox" ? item.id : item.event.id] = page.read_at;
        collection.cursor = page.cursor;
        collection.hasMore = items.length === 20;
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

  targets() {
    const projects = this.projects.length > 1 ? this.projects.map((p) => ({
      kind: "project", id: p.project_id, label: `Project: ${p.label} · ${p.project_id}`,
    })) : [];
    const jobs = (this.board?.project?.jobs ?? []).map((j) => ({
      kind: "job", id: j.job_id, label: `Job: ${j.role} · ${j.state} · ${j.job_id}`,
    }));
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
    const manifests = [];
    const addManifest = (id, label) => {
      if (id && !manifests.some((m) => m.id === id)) manifests.push({ kind: "manifest", id, label: `Manifest: ${label} · ${id}` });
    };
    addManifest(this.board?.project?.current_binding?.handoff_manifest_digest, "handoff");
    addManifest(this.detail?.brief_digest, "brief");
    for (const a of this.detail?.associations ?? []) addManifest(a.manifest_digest, `${a.kind} · ${a.verification}`);
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
    return [{ kind: "refresh", label: "Refresh board" }, ...projects, ...summary, ...jobs, ...lanes, ...manifests, ...controls, ...results, ...messages];
  }

  key(e, cx) {
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
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
      else if (target.kind === "project") this.select(target.id, cx);
      else if (target.kind === "job") this.inspect(target.id, cx);
      else if (target.kind === "manifest") this.inspectManifest(target.id, cx);
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
    if (!this.mailContextCurrent(context)) return;
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
        await exec("ocq-herdr", ["mail-ack", context.project, context.id, context.sequence, mode], { on: context.machine });
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
          inbox.items = inbox.items.map((m) => m.id === item.id ? item : m);
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
      value = { stored_job: this.board?.project?.jobs?.find((j) => j.job_id === target.id),
        job_detail: this.detailJob === target.id ? (this.detailLoading ? "Loading job detail…" : this.detailError || this.detail) : "Activate to inspect associations" };
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
    else value = target.kind === "refresh" ? "Activate to refresh the stored project board." : `Activate to ${target.label.toLowerCase()}.`;
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
      this.refresh(cx);
    }
    const t = theme();
    const button = (id, text, action) => div().id(id).role("button").accessibility_label(text).px(8).py(4).rounded(5)
      .text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
      .on_click((_e, cx) => action(cx)).child(text);
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
        .child(button("ocq-refresh", this.loading ? "Refreshing…" : "Refresh", (cx) => this.refresh(cx))))
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
          .when(Boolean(project?.current_binding?.handoff_manifest_digest), (e) => e.child(button("ocq-handoff", "Read handoff", (cx) => this.openTarget("manifest", project.current_binding.handoff_manifest_digest, cx))))
          .child(div().pt(8).font_semibold().child("Work"))
          .when((project?.jobs ?? []).length === 0, (e) => e.child(div().text_color(t.dim).child("No jobs.")))
          .children((project?.jobs ?? []).map((j) => v_flex().id(`ocq-job-${j.job_id}`).py(5).gap(2).border_b(1).border_color(t.border)
            .cursor_pointer().hover((s) => s.bg(t.hover)).on_click((_e, cx) => this.inspect(j.job_id, cx))
            .child(div().child(`${j.role} · ${j.state} · ${j.outcome}`))
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
                .child(div().child(`Lane: ${this.detail?.lane_id || "not assigned"}`))
                .child(div().child(`Worktree: ${this.detail?.fsfork_path || "not recorded"}`))
                .child(div().child(`Integration: ${this.detail?.integration || "unknown"}`))
                .child(div().child(`Validation verified: ${this.detail?.validation?.verified ? "yes" : "no"}`))
                .child(div().role("button").accessibility_label("View all job fields").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.openTarget("job", j.job_id, cx); }).child("View all job fields"))
                .when(Boolean(this.detail?.brief_digest), (e) => e.child(div().role("button").accessibility_label("Read job brief").text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => { cx.stop_propagation(); this.openTarget("manifest", this.detail.brief_digest, cx); }).child("Read job brief")))
                .children((this.detail?.associations ?? []).map((a) => div().text_size(12).text_color(t.accent).cursor_pointer()
                  .on_click((_e, cx) => {
                    cx.stop_propagation();
                    this.openTarget("manifest", a.manifest_digest, cx);
                  })
                  .child(`Read ${a.kind} · ${a.verification} · ${a.manifest_digest}`))))))))
          .child(div().pt(8).font_semibold().child("Lanes"))
          .when((project?.lanes ?? []).length === 0, (e) => e.child(div().text_color(t.dim).child("No lanes.")))
          .children((project?.lanes ?? []).map((lane) => button(`ocq-lane-${lane.lane_id}`, `${lane.role} · ${lane.job_state} · ${lane.lane_id}`, (cx) => this.openTarget("lane", lane.lane_id, cx))))
          .child(div().pt(8).font_semibold().child("Completion history"))
          .child(div().text_size(12).text_color(t.muted).child(`Stored pages · latest read ${this.collections.results.readAt || "not yet read"}; each page is a separate snapshot`))
          .when(Boolean(this.collections.results.error), (e) => e.child(div().text_color(t.ansi1).child(`Results read failed: ${this.collections.results.error}`)))
          .when(this.collections.results.loading, (e) => e.child(div().child("Loading results…")))
          .when(this.collections.results.loaded && !this.collections.results.items.length, (e) => e.child(div().text_color(t.dim).child("No completion events.")))
          .children(this.collections.results.items.map((r) => v_flex().gap(2)
            .child(button(`ocq-result-${r.event.id}`, `${r.role} · historical ${r.event.outcome} · current ${r.current_job.state} · ${r.job_id}`, (cx) => this.openTarget("result", r.event.id, cx)))
            .children((r.result_summary?.associations ?? []).map((a) => button(`ocq-result-manifest-${a.id}`, `Read ${a.kind} · ${a.verification}`, (cx) => this.openTarget("manifest", a.manifest_digest, cx))))))
          .when(this.collections.results.hasMore, (e) => e.child(button("ocq-results-more", "Load more results", (cx) => this.loadCollection("results", false, cx))))
          .child(button("ocq-results-reload", "Reload results", (cx) => this.loadCollection("results", true, cx)))
          .child(div().pt(8).font_semibold().child("Inbox · all recipients"))
          .child(div().text_size(12).text_color(t.muted).child(`Stored pages · latest read ${this.collections.inbox.readAt || "not yet read"}; includes read and archived mail`))
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
          .child(div().pt(8).text_size(12).text_color(t.muted).child("Stored observations only. Imported claims are unverified; worktree drift is not checked. Opening mail does not mark it read.")))));
  }
}
