import { View, div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { exec, nextCommand, spaces, theme } from "herdr";
import { connect } from "net";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";

function emptyCollection() {
  return { items: [], history: [], recent: [], readTimes: {}, cursor: "0", refreshCursor: "0", readAt: null, recentReadAt: null, loaded: false, loading: false, recentLoading: false, refreshingHistory: false, hasMore: false, error: null, recentError: null };
}

function readOrder(value) {
  return (value || "").replace(/(?:\.(\d+))?Z$/, (_match, fraction) => `.${(fraction || "").padEnd(9, "0")}Z`);
}

// The shell has synchronous hashes, not a usable async webcrypto.subtle.
function composeProof(capability, nonce, role) {
  const key = Buffer.from(capability, "hex"), inner = new Uint8Array(64), outer = new Uint8Array(64);
  for (let i = 0; i < 64; i++) { const b = i < key.length ? key[i] : 0; inner[i] = b ^ 0x36; outer[i] = b ^ 0x5c; }
  const digest = createHash("sha256").update(inner).update(`ocq-compose-v1:${role}:${nonce}`).digest();
  return createHash("sha256").update(outer).update(digest).digest("hex");
}

function composeEqual(a, b) {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return difference === 0;
}

function composeDecode(bytes) {
  const text = Buffer.from(bytes).toString("utf8"), encoded = Buffer.from(text, "utf8");
  if (encoded.length !== bytes.length) throw new Error("Invalid UTF-8 frame");
  for (let i = 0; i < bytes.length; i++) if (encoded[i] !== bytes[i]) throw new Error("Invalid UTF-8 frame");
  return text;
}

function socketFrames(socket) {
  let pending = [];
  async function more() {
      const chunk = await socket.read(4096);
      if (chunk === null || chunk.length === 0) throw new Error("Connection closed");
      for (const byte of chunk) pending.push(byte);
      if (pending.length > 135168) throw new Error("Frame too large");
  }
  return {
    async line(limit = 131072) {
      for (;;) {
        const end = pending.indexOf(10);
        if (end >= 0) { if (end > limit) throw new Error("Frame too large"); const frame = pending.slice(0, end); pending = pending.slice(end + 1); return composeDecode(frame); }
        if (pending.length > limit) throw new Error("Frame too large");
        await more();
      }
    },
    async bytes(count) {
      if (!Number.isInteger(count) || count < 0 || count > 131072) throw new Error("Invalid frame size");
      while (pending.length < count) await more();
      const frame = pending.slice(0, count); pending = pending.slice(count); return frame;
    },
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

function intakeStructured(kind, raw) {
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
      else for (const key of keys) walk(`${label} · ${key}`, value[key]);
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
  if (kind === "registration") {
    const request = intakeObject(parsed, ["version", "key", "project_id", "label", "expected_revision", "repo", "commit_anchor", "elephant_session_id", "expected_opencode_project_id", "expected_opencode_directory", "auth_ref", "handoff_manifest_digest", "handoff_manifest"]);
    const handoff = intakeObject(intakeEmbedded(request.handoff_manifest), ["version", "kind", "project_id", "source", "observed_at", "intent", "decisions", "risks", "outstanding_job_ids", "outstanding_event_ids", "outstanding_manifest_digests", "notes"]);
    if (handoff.kind !== "handoff") throw new Error("unsupported manifest");
    fieldList("Registration and binding", request, ["handoff_manifest"]);
    fieldList("Handoff", handoff);
  } else if (kind === "submission") {
    const request = intakeObject(parsed, ["version", "key", "project_id", "binding_revision", "role", "lane_id", "fsfork_driver", "request", "brief_digest", "brief_manifest_digest", "brief_manifest", "target_job_id", "target_result_digest", "referenced_manifests"]);
    if (request.role !== "implementation" || (request.referenced_manifests && request.referenced_manifests.length)) throw new Error("unsupported submission representation");
    const agent = intakeObject(intakeEmbedded(request.request), ["version", "kind", "repo", "base", "prompt", "agent", "model", "permissions", "provision_policy", "memory_bytes"]);
    model(agent.model); permissions(agent.permissions);
    const brief = intakeObject(intakeEmbedded(request.brief_manifest), ["version", "kind", "project_id", "source", "observed_at", "goal", "scope", "exclusions", "acceptance_criteria", "base", "agent", "model", "permissions", "allowed_actions", "context_references"]);
    if (brief.kind !== "brief") throw new Error("unsupported manifest");
    model(brief.model); permissions(brief.permissions);
    fieldList("Submission and binding", request, ["request", "brief_manifest"]);
    if (!request.lane_id) walk("Submission and binding · effective lane_id (derived from key)", `lane-${createHash("sha256").update(request.key).digest("hex").slice(0, 16)}`);
    if (!request.fsfork_driver) walk("Submission and binding · effective fsfork_driver (default)", "git_worktree");
    fieldList("Agent request", agent);
    fieldList("Brief", brief);
  } else throw new Error("unsupported kind");
  return sections;
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
    this.compose = { screen: false, state: "loading", id: null, preview: null, working: false, error: null };
    this.composeSubject = InputState.new({ placeholder: "Subject" });
    this.composeBody = TextareaState.new({ placeholder: "Message", rows: 3 });
    this.composeInputFocused = false;
    for (const input of [this.composeSubject, this.composeBody]) {
      input.on("change", (_e, cx) => cx.notify());
      input.on("focus", (_e, cx) => { this.composeInputFocused = true; cx.notify(); });
      input.on("blur", (_e, cx) => { this.composeInputFocused = false; cx.notify(); });
    }
    this.composeSubject.on("submit", () => this.composeBody.focus());
    this.composeBody.on("submit", () => this.handle.focus());
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
    this.compose.screen = false;
    this.compose.preview = null;
    this.compose.context = null;
    this.intake.screen = false;
    this.intake.preview = null;
    this.intake.context = null;
    this.intake.confirm = false;
  }

  refresh(cx, notify = true) {
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
    return { space: this.spaceID, folder: this.folder, project: this.selected, lane: lane.lane_id, job: lane.owner_job_id };
  }

  composeContextCurrent(context) {
    return Boolean(context && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected &&
      (this.board?.project?.lanes ?? []).some((lane) => lane.lane_id === context.lane && lane.owner_job_id === context.job));
  }

  composePreviewPages(request) {
    const pages = [];
    for (const [label, value] of [
      ["Machine", "this computer"], ["Project", request.ProjectID], ["Lane", request.LaneID], ["Job", request.JobID],
      ["From", request.From], ["To", request.To], ["Kind", request.Kind],
      ["Manifest", request.Manifest || "none"], ["Reply to", request.ReplyTo || "none"], ["Worktree (empty uses lane default)", request.FsforkPath ?? ""],
      ["Subject", request.Subject], ["Message", request.Body || ""],
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

  refreshCompose(cx) {
    const c = this.compose;
    if (!this.composeLocal() || c.working) return;
    c.working = true;
    cx.spawn(async (cx) => {
      try {
        const result = await exec("ocq-herdr", ["compose-status"], { on: null });
        if (result.code !== 0) throw new Error("status unavailable");
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
        if (c.preview && (!["prepared", "armed"].includes(status.state) || c.preview.id !== status.id)) c.preview = null;
        c.error = null;
      } catch (_e) { if (c.state !== "uncertain") c.state = "blocked"; c.preview = null; c.error = "Compose status unavailable. Do not send; check the local store."; }
      finally { c.working = false; cx.notify(); }
    });
  }

  openCompose(lane, cx) {
    const c = this.compose;
    c.screen = true; c.confirm = false; c.error = null;
    if (lane && c.state === "idle") {
      c.context = this.composeContext(lane);
      if (c.context) {
        this.composeSubject.set_value(""); this.composeBody.set_value("");
        const context = c.context;
        cx.timer.after(100, () => { if (c.screen && c.context === context && c.state === "idle" && this.composeContextCurrent(context)) this.composeSubject.focus(); });
      }
    } else this.handle.focus();
    this.keyboardMode = false;
    this.refreshCompose(cx);
    cx.notify();
  }

  async composeExchange(command, id, payload, context, cx) {
    const launch = randomBytes(32).toString("hex");
    const args = command === "compose-preview" ? [command, id, launch] : [command, launch];
    const worker = exec("ocq-herdr", args, { on: null, timeout: 25000 }).then((value) => value, () => null);
    let socket = null;
    try {
      let rendezvous = null;
      for (let i = 0; i < 100; i++) {
        if (!this.composeContextCurrent(context)) throw new Error("context changed");
        const result = await exec("ocq-herdr", ["compose-rendezvous", launch], { on: null });
        if (result.code === 0) { rendezvous = JSON.parse(result.stdout); break; }
        await cx.sleep(50);
      }
      if (!rendezvous || rendezvous.launch !== launch || !/^[0-9a-f]{64}$/.test(rendezvous.capability) || !Number.isInteger(rendezvous.port) || rendezvous.port < 1 || rendezvous.port > 65535) throw new Error("rendezvous unavailable");
      socket = await connect("127.0.0.1", rendezvous.port);
      const frames = socketFrames(socket), line = () => frames.line(), nonce = randomBytes(32).toString("hex");
      await socket.write(nonce + "\n");
      if (!composeEqual(await line(), composeProof(rendezvous.capability, nonce, "server"))) throw new Error("server authentication failed");
      await socket.write(composeProof(rendezvous.capability, nonce, "client") + "\n");
      if (await line() !== "READY" || !this.composeContextCurrent(context)) throw new Error("context or client authentication failed");
      let response;
      if (command === "compose-receive") {
        await socket.write(payload + "\n");
        response = await line();
        if (!/^PREPARED [0-9a-f]{64}$/.test(response)) throw new Error("preparation uncertain");
      } else response = await line();
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !this.composeContextCurrent(context)) throw new Error("compose exchange uncertain");
      if (command === "compose-preview") {
        const status = JSON.parse(done.stdout);
        if (status.id !== id || !["prepared", "armed"].includes(status.state)) throw new Error("preview status mismatch");
        return { response, status };
      }
      return response;
    } finally { if (socket) socket.close(); }
  }

  prepareCompose(cx) {
    const c = this.compose, context = c.context;
    if (c.working || c.state !== "idle" || !this.composeContextCurrent(context)) return;
    const subject = this.composeSubject.value(), body = this.composeBody.value();
    try {
      if (!subject || Buffer.from(subject, "utf8").length > 512 || Buffer.from(body, "utf8").length > 16384 || composeDecode(Buffer.from(subject, "utf8")) !== subject || composeDecode(Buffer.from(body, "utf8")) !== body || subject.includes("\0") || body.includes("\0")) throw new Error("invalid text");
    } catch (_e) { c.error = "Enter a subject and valid UTF-8 text within the size limits."; cx.notify(); return; }
    const payload = JSON.stringify({ ProjectID: context.project, LaneID: context.lane, JobID: context.job, From: `lane:${context.lane}`, To: `integrator:${context.project}`, Kind: "lane.blocked", Subject: subject, Body: body });
    c.working = true; c.error = null; c.observation = null;
    cx.spawn(async (cx) => {
      try {
        const reply = await this.composeExchange("compose-receive", null, payload, context, cx);
        c.id = reply.slice(9); c.state = "prepared"; c.preview = null;
      } catch (_e) { c.state = "blocked"; c.error = "Preparation outcome unknown. Refresh journal status before doing anything else."; }
      finally { c.working = false; this.handle.focus(); cx.notify(); }
    });
    cx.notify();
  }

  previewCompose(cx) {
    const c = this.compose, id = c.id, initialState = c.state, initialRecovery = c.recoveryState, generation = this.detailGeneration;
    if (c.working || !(["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))) || !id || !this.composeLocal() || !this.selected) return;
    // The selected project must be explicit; the returned lane/job is checked
    // against the current stored board before it can be confirmed.
    const context = { space: this.spaceID, folder: this.folder, project: this.selected };
    const current = () => c.screen && c.id === id && c.state === initialState && c.recoveryState === initialRecovery && generation === this.detailGeneration && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.project === this.selected;
    c.working = true; c.error = null; c.preview = null; c.confirm = false;
    cx.spawn(async (cx) => {
      try {
        // Recovery does not know the lane until the private request is read.
        const lane = (this.board?.project?.lanes ?? [])[0];
        if (!current() || !lane) throw new Error("no selected project lanes");
        const transport = { ...context, lane: lane.lane_id, job: lane.owner_job_id };
        const { response, status } = await this.composeExchange("compose-preview", id, null, transport, cx);
        if (!current() || ((initialState === "armed" || initialRecovery === "armed") && status.state !== "armed")) throw new Error("preview state changed");
        const request = JSON.parse(response);
        const actual = { ...context, lane: request.LaneID, job: request.JobID };
        const route = request.Kind === "lane.blocked"
          ? (request.From === `lane:${actual.lane}` || request.From === `job:${actual.job}`) && request.To === `integrator:${context.project}`
          : request.Kind === "lane.assigned" && request.From.startsWith("integrator:") && (this.board?.project?.lanes ?? []).some((lane) => request.To === `lane:${lane.lane_id}`);
        if (!current() || !this.composeContextCurrent(actual) || request.ProjectID !== context.project || !route) throw new Error("context mismatch");
        if (initialState === "uncertain") c.recoveryState = status.state;
        else c.state = status.state;
        c.context = actual; c.preview = { id, state: status.state, request }; c.page = 0; c.confirm = false;
      } catch (_e) {
        if (current() && c.state !== "uncertain") c.state = "blocked";
        c.error = "Preview unavailable or belongs to another project/lane. Refresh journal status; nothing was sent.";
      }
      finally { c.working = false; this.handle.focus(); cx.notify(); }
    });
    cx.notify();
  }

  composeAction(action, cx) {
    const c = this.compose, id = c.id, preview = c.preview, wasUncertain = c.state === "uncertain";
    if (c.working || !id || !this.composeLocal()) return;
    if (action === "dispatch" && (c.state !== "prepared" || !c.confirm || c.preview?.id !== id || c.preview.state !== "prepared" || c.page !== this.composePreviewPages(c.preview.request).length - 1 || !this.composeContextCurrent(c.context))) return;
    if (action === "cancel" && !((["prepared", "armed"].includes(c.state) || (wasUncertain && ["prepared", "armed"].includes(c.recoveryState))) && preview?.id === id && this.composeContextCurrent(c.context))) return;
    if (action === "reconcile" && !["dispatch_intent", "uncertain"].includes(c.state)) return;
    if (action === "retire" && !["completed", "cancelled"].includes(c.state)) return;
    c.working = true; c.confirm = false; c.error = null;
    if (action === "dispatch") { c.state = "uncertain"; c.recoveryState = null; c.preview = null; }
    cx.spawn(async (cx) => {
      try {
        if (action === "dispatch") {
          // The send process is never launched until this separate, durable
          // claim has been acknowledged. A delayed claim cannot send by itself.
          const claim = await exec("ocq-herdr", ["compose-arm", id], { on: null, timeout: 30000 });
          if (claim.code !== 0) throw new Error("claim outcome unavailable");
          const armed = JSON.parse(claim.stdout);
          if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
          if (!this.composeContextCurrent(c.context)) {
            c.state = "armed";
            c.error = "Context changed. Send was not launched by this view; revoke the claim before starting over.";
            return;
          }
        }
        if (action === "cancel" && (c.id !== id || c.preview !== preview || !this.composeContextCurrent(c.context))) throw new Error("context changed");
        const result = await exec("ocq-herdr", [`compose-${action}`, id], { on: null, timeout: 30000 });
        if (result.code !== 0) throw new Error("action unavailable");
        const status = JSON.parse(result.stdout);
        if (status.id !== id) throw new Error("identity mismatch");
        if (action === "retire" && status.state === "retired") { c.state = "idle"; c.id = null; c.preview = null; c.context = null; c.observation = null; }
        else if (["dispatch_intent", "completed", "cancelled"].includes(status.state)) { c.state = status.state; c.observation = status.observation; c.preview = null; c.recoveryState = null; }
        else throw new Error("unexpected state");
      } catch (_e) { c.error = action === "dispatch" || action === "reconcile" || wasUncertain ? "Outcome uncertain. Refresh and check the stored intent; do not resend." : "Action outcome unavailable. Refresh journal status."; if (wasUncertain) { c.state = "uncertain"; c.recoveryState = null; c.preview = null; } else if (action !== "dispatch" && action !== "reconcile") c.state = "blocked"; }
      finally { c.working = false; cx.notify(); }
    });
    cx.notify();
  }

  intakeCurrent(context) {
    return Boolean(context && this.intake.context === context && this.intake.screen && this.composeLocal() && context.space === this.spaceID && context.folder === this.folder && context.selection === this.selected);
  }

  intakeMatches(kind, raw, context) {
    const request = JSON.parse(raw);
    if (!request || typeof request !== "object" || Array.isArray(request) || typeof request.project_id !== "string" || !/^proj-[A-Za-z0-9_-]+$/.test(request.project_id)) return false;
    if (kind === "registration") return request.repo === context.folder && request.expected_opencode_directory === context.folder && (context.newProject ? request.expected_revision === 0 : (!context.project || request.project_id === context.project));
    if (kind !== "submission" || !context.project || request.project_id !== context.project || this.board?.project?.current_binding?.repo_root !== context.folder) return false;
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

  intakeReviewPages(kind, raw) {
    const pages = [];
    for (const section of intakeStructured(kind, raw)) {
      const chars = Array.from(section.text);
      const count = Math.max(1, Math.ceil(chars.length / 160));
      for (let n = 0; n < count; n++) pages.push({ title: section.title, text: chars.slice(n * 160, (n + 1) * 160).join(""), part: n + 1, parts: count });
    }
    if (!pages.length) throw new Error("empty review");
    return pages;
  }

  openIntake(cx, newProject = false) {
    const i = this.intake;
    if (i.working) return;
    i.screen = true; i.context = { space: this.spaceID, folder: this.folder, selection: this.selected, project: newProject ? null : this.selected, newProject };
    if (newProject) i.kind = "registration";
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
      finally { i.working = false; cx.notify(); }
    });
  }

  async intakeExchange(mode, kind, id, raw, context, cx) {
    if (!this.intakeCurrent(context)) throw new Error("context changed");
    const launch = randomBytes(32).toString("hex");
    const command = mode === "receive" ? ["intake-receive-text", kind, launch] : ["intake-preview", id, launch];
    const worker = exec("ocq-herdr", command, { on: null, timeout: 25000 }).then((value) => value, () => null);
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
      const frames = socketFrames(socket), nonce = randomBytes(32).toString("hex");
      await socket.write(nonce + "\n");
      if (!composeEqual(await frames.line(64), composeProof(rendezvous.capability, nonce, "server"))) throw new Error("server authentication failed");
      await socket.write(composeProof(rendezvous.capability, nonce, "client") + "\n");
      if (await frames.line(64) !== "READY" || !this.intakeCurrent(context)) throw new Error("context or authentication changed");
      let response;
      if (mode === "receive") {
        const bytes = Buffer.from(raw, "utf8");
        if (!bytes.length || bytes.length > 131072 || composeDecode(bytes) !== raw) throw new Error("invalid request");
        await socket.write(`${bytes.length}\n`);
        if (!this.intakeCurrent(context)) throw new Error("context changed");
        await socket.write(raw);
        response = await frames.line(80);
        if (!/^PREPARED [0-9a-f]{64}$/.test(response)) throw new Error("preparation uncertain");
      } else {
        const header = await frames.bytes(5);
        const length = header[1] * 16777216 + header[2] * 65536 + header[3] * 256 + header[4];
        if ((header[0] !== 82 && header[0] !== 83) || !length || length > 131072) throw new Error("invalid preview frame");
        response = { kind: header[0] === 82 ? "registration" : "submission", raw: composeDecode(await frames.bytes(length)) };
      }
      socket.close(); socket = null;
      const done = await worker;
      if (!done || done.code !== 0 || !this.intakeCurrent(context)) throw new Error("exchange uncertain");
      if (mode === "preview") {
        const status = JSON.parse(done.stdout);
        if (status.id !== id || !["prepared", "armed", "attempted", "completed", "cancelled"].includes(status.state)) throw new Error("preview state changed");
        response.state = status.state;
      }
      return response;
    } finally { if (socket) socket.close(); }
  }

  prepareIntake(cx) {
    const i = this.intake, context = i.context, raw = this.intakeInput.value(), kind = i.kind;
    if (i.working || i.state !== "idle" || !this.intakeCurrent(context)) return;
    try {
      const bytes = Buffer.from(raw, "utf8");
      if (!bytes.length || bytes.length > (kind === "registration" ? 16384 : 131072) || composeDecode(bytes) !== raw || !this.intakeMatches(kind, raw, context)) throw new Error("invalid request or context");
      if (context.newProject && (kind !== "registration" || this.projects.some((p) => p.project_id === JSON.parse(raw).project_id))) throw new Error("new project already exists");
      this.intakeReviewPages(kind, raw);
    } catch (_e) { i.error = "Request is invalid, belongs to another context, or uses an unsupported review representation. Nothing was prepared."; cx.notify(); return; }
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("receive", kind, null, raw, context, cx);
        if (this.intakeCurrent(context)) { i.id = result.slice(9); i.state = "prepared"; i.preview = null; }
      } catch (_e) { if (this.intakeCurrent(context)) { i.state = "blocked"; i.error = "Preparation outcome unknown. Refresh the journal; do not prepare again until it is resolved."; } }
      finally { i.working = false; this.handle.focus(); cx.notify(); }
    });
    cx.notify();
  }

  previewIntake(cx) {
    const i = this.intake, context = i.context, id = i.id;
    if (i.working || !id || !this.intakeCurrent(context) || !["prepared", "armed", "attempted", "completed", "cancelled"].includes(i.state)) return;
    i.working = true; i.preview = null; i.error = null;
    cx.spawn(async (cx) => {
      try {
        const result = await this.intakeExchange("preview", null, id, null, context, cx);
        if (!this.intakeCurrent(context) || !this.intakeMatches(result.kind, result.raw, context)) throw new Error("context mismatch");
        const rawPages = this.intakePages(result.raw).map((text) => ({ title: "Exact raw JSON string", text }));
        let review = null;
        try { review = this.intakeReviewPages(result.kind, result.raw); } catch (_e) { /* Keep exact raw view, never invent a summary. */ }
        i.state = result.state; i.preview = { id, kind: result.kind, raw: result.raw, state: result.state, review, rawPages, pages: review || rawPages, view: review ? "review" : "raw", openedThrough: 0 }; i.page = 0; i.confirm = false;
        if (!review) i.error = "This envelope representation is not supported for structured review. Exact raw view only; dispatch remains unavailable.";
      } catch (_e) { if (this.intakeCurrent(context)) i.error = "Preview unavailable or belongs to another project or folder. Nothing was dispatched."; }
      finally { i.working = false; this.handle.focus(); cx.notify(); }
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
    return Boolean(!i.working && !i.uncertainID && this.intakeCurrent(i.context) && i.state === "prepared" && p?.id === i.id && p.state === "prepared" && p.review && p.view === "review" && i.page === p.review.length - 1 && p.openedThrough === p.review.length - 1 && this.intakeMatches(p.kind, p.raw, i.context));
  }

  confirmIntake(cx) {
    const i = this.intake, context = i.context, preview = i.preview, id = i.id;
    if (!i.confirm || !this.canConfirmIntake()) return;
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      let armLaunched = false;
      try {
        const exact = await this.intakeExchange("preview", null, id, null, context, cx);
        if (!this.intakeCurrent(context) || !i.confirm || i.preview !== preview || i.id !== id || exact.state !== "prepared" || exact.kind !== preview.kind || exact.raw !== preview.raw || !this.intakeMatches(exact.kind, exact.raw, context)) throw new Error("preview changed");
        i.uncertainID = id; armLaunched = true;
        const claim = await exec("ocq-herdr", ["intake-arm", id], { on: null, timeout: 30000 });
        if (!claim || claim.code !== 0) throw new Error("claim outcome unknown");
        const armed = JSON.parse(claim.stdout);
        if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
        if (!this.intakeCurrent(context) || !i.confirm || i.preview !== preview || i.id !== id) return;
        i.confirm = false; i.state = "armed";
        const result = await exec("ocq-herdr", ["intake-dispatch", id], { on: null, timeout: 30000 });
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
      } finally { i.working = false; cx.notify(); }
    });
    cx.notify();
  }

  intakeAction(action, cx) {
    const i = this.intake, context = i.context, id = i.id;
    if (i.working || !this.intakeCurrent(context) || i.preview?.id !== id || i.preview?.state !== i.state) return;
    if (action === "cancel" && !["prepared", "armed"].includes(i.state)) return;
    if (action === "reconcile" && !["attempted", "completed"].includes(i.state)) return;
    if (action === "retire" && !["cancelled", "completed"].includes(i.state)) return;
    if (!["cancel", "reconcile", "retire"].includes(action)) return;
    i.working = true; i.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.intakeCurrent(context) || i.id !== id || i.preview?.id !== id || i.preview?.state !== i.state) throw new Error("context changed");
        const result = await exec("ocq-herdr", [`intake-${action}`, id], { on: null, timeout: 30000 });
        if (result.code !== 0 || !this.intakeCurrent(context)) throw new Error("action unavailable");
        const status = JSON.parse(result.stdout);
        const expected = action === "cancel" ? ["cancelled"] : action === "retire" ? ["retired"] : ["attempted", "completed", "blocked"];
        if (status.id !== id || !expected.includes(status.state)) throw new Error("identity mismatch");
        if (action === "retire") { i.state = "idle"; i.id = null; i.preview = null; i.uncertainID = null; i.observation = null; }
        else { i.state = status.state; i.preview.state = status.state; i.observation = status.observation ?? null; if (["completed", "cancelled"].includes(status.state)) i.uncertainID = null; }
      } catch (_e) { if (this.intakeCurrent(context)) { i.state = "blocked"; i.preview = null; i.error = "Action outcome unavailable. Refresh the journal before proceeding."; } }
      finally { i.working = false; cx.notify(); }
    });
    cx.notify();
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
    const composeLanes = (this.board?.project?.lanes ?? []).map((lane) => ({ kind: "compose-lane", id: lane.lane_id, label: `Compose update from lane ${lane.lane_id}` }));
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
    return [{ kind: "refresh", label: "Refresh board" }, { kind: "compose", label: "Compose and recovery" }, { kind: "intake", label: "Prepare project intake" }, ...projects, ...summary, ...jobs, ...lanes, ...composeLanes, ...manifests, ...controls, ...results, ...messages];
  }

  key(e, cx) {
    if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
    if (this.intake.screen) {
      if (this.intakeInputFocused) {
        if (e.key === "escape") { this.handle.focus(); cx.stop_propagation(); }
        return;
      }
      const i = this.intake;
      if (i.confirm) {
        if (e.key === "escape" && !i.working) { i.confirm = false; cx.stop_propagation(); cx.notify(); }
        return;
      }
      if (e.key === "escape") i.screen = false;
      else if (!e.is_held && e.key === "r") this.refreshIntake(cx);
      else if (!e.is_held && e.key === "p") this.prepareIntake(cx);
      else if (!e.is_held && e.key === "v") this.previewIntake(cx);
      else if (!e.is_held && e.key === "c") this.intakeAction("cancel", cx);
      else if (!e.is_held && e.key === "t") this.intakeAction("retire", cx);
      else if (!e.is_held && e.key === "left" && i.preview) this.advanceIntakePage(-1, cx);
      else if (!e.is_held && e.key === "right" && i.preview) this.advanceIntakePage(1, cx);
      else return;
      cx.stop_propagation(); cx.notify(); return;
    }
    if (this.compose.screen) {
      if (this.composeInputFocused) {
        if (e.key === "escape") { this.handle.focus(); cx.stop_propagation(); }
        return;
      }
      const c = this.compose;
      if (e.key === "escape") { if (c.confirm) c.confirm = false; else c.screen = false; }
      else if (!e.is_held && e.key === "r") this.refreshCompose(cx);
      else if (!e.is_held && e.key === "p" && c.state === "idle") this.prepareCompose(cx);
      else if (!e.is_held && e.key === "v") this.previewCompose(cx);
      else if (!e.is_held && e.key === "c") this.composeAction("cancel", cx);
      else if (!e.is_held && e.key === "k") this.composeAction("reconcile", cx);
      else if (!e.is_held && e.key === "t") this.composeAction("retire", cx);
      else if (e.key === "left" && c.preview && !c.confirm) c.page = Math.max(0, c.page - 1);
      else if (e.key === "right" && c.preview && !c.confirm) c.page = Math.min(this.composePreviewPages(c.preview.request).length - 1, c.page + 1);
      else if (!e.is_held && e.key === "s" && c.state === "prepared" && c.preview?.state === "prepared" && !c.confirm && c.page === this.composePreviewPages(c.preview.request).length - 1) c.confirm = true;
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
      else if (target.kind === "compose") this.openCompose(null, cx);
      else if (target.kind === "intake") this.openIntake(cx);
      else if (target.kind === "compose-lane") this.openCompose((this.board?.project?.lanes ?? []).find((lane) => lane.lane_id === target.id), cx);
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
      this.refresh(cx, false);
    }
    const t = theme();
    const button = (id, text, action) => div().id(id).role("button").accessibility_label(text).px(8).py(4).rounded(5)
      .text_color(t.foreground).hover((s) => s.bg(t.hover)).cursor_pointer()
      .on_click((_e, cx) => action(cx)).child(text);
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
            .child(div().font_semibold().child(`${preview.kind} · ${preview.view === "review" ? "field" : "raw"} ${i.page + 1}/${preview.pages.length}`))
            .child(div().flex_1()).child(button("ocq-intake-review-back", "Back", (cx) => { i.preview = null; cx.notify(); })))
          .child(v_flex().id(`ocq-intake-page-${preview.view}-${i.page}`).flex_1().min_h(0).overflow_y_scroll().gap(8)
            .child(div().text_size(12).text_color(t.muted).child(preview.view === "raw" ? "Exact JSON string: join lines and pages to recover the retained text." : "Scroll to see the entire field. Controls and invisible characters are escaped. Opening pages is not confirmation."))
            .child(div().font_semibold().child(page.title + (page.parts > 1 ? ` · ${page.part}/${page.parts}` : "")))
            .child(div().child(page.text))
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
        .when(local && !i.working, (e) => e.child(button("ocq-intake-context", i.context.newProject ? "Use selected project context" : "Register a new project…", (cx) => this.openIntake(cx, !i.context.newProject))))
        .when(local, (e) => e.child(div().child(stateText[i.state] || "Journal status unavailable.")))
        .when(Boolean(i.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${i.id}`)))
        .when(Boolean(i.error), (e) => e.child(div().text_color(t.ansi1).child(i.error)))
        .when(local && !i.working, (e) => e.child(button("ocq-intake-refresh", "Refresh journal", (cx) => this.refreshIntake(cx))))
        .when(local && i.state === "idle", (e) => e.child(v_flex().gap(6)
          .child(div().child("Paste the complete exact JSON envelope. Typing does not prepare or dispatch."))
          .child(h_flex().gap(8)
            .child(button("ocq-intake-registration", i.kind === "registration" ? "Registration ✓" : "Registration", (cx) => { i.kind = "registration"; cx.notify(); }))
            .when(!i.context.newProject, (e) => e.child(button("ocq-intake-submission", i.kind === "submission" ? "Submission ✓" : "Submission", (cx) => { i.kind = "submission"; cx.notify(); }))))
          .child(Textarea.new(this.intakeInput).id("ocq-intake-input").accessibility_label("Exact intake JSON envelope").h(150).px(8).border_1().border_color(t.border).text_color(t.foreground))
          .when(!i.working, (e) => e.child(button("ocq-intake-prepare", "Prepare retained request", (cx) => this.prepareIntake(cx))))))
        .when(local && !i.working && Boolean(i.id), (e) => e.child(button("ocq-intake-preview", "Preview exact retained request", (cx) => this.previewIntake(cx))))
        .child(div().text_size(12).text_color(t.muted).child("R refresh · P prepare · V preview · C cancel · T retire · ←/→ pages · Esc close"));
    }
    if (this.compose.screen) {
      const c = this.compose, local = this.composeLocal(), request = c.preview?.request;
      const previewPages = request ? this.composePreviewPages(request) : [];
      const stateText = { loading: "Checking local journal…", idle: "No retained compose intent.", busy: "Another compose operation holds the journal. Refresh later.", prepared: "Prepared locally. A confirmation from another view may still be pending. Preview before confirming.", armed: "Send confirmed and durably claimed. A send process may be delayed; do not confirm again. You can revoke only before an attempt begins.", dispatch_intent: "Send outcome uncertain. Check the stored receipt; never resend automatically.", uncertain: "Send outcome uncertain. Check the stored receipt; never resend automatically.", completed: "Stored receipt matched. Retire to prepare another message.", cancelled: c.observation === "revoked_before_attempt" ? "Claim revoked before any attempt. A delayed send process will be refused. Retire to prepare another message." : "Prepared message cancelled. A delayed confirmation will be refused. Retire to prepare another message.", blocked: "Journal blocked or unverifiable. Do not send; investigate the local store." };
      return v_flex().id("ocq-compose").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
        .track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
        .child(h_flex().items_center().gap(8).child(div().font_semibold().child("Compose · local only")).child(div().flex_1())
          .child(button("ocq-compose-close", "Close", (cx) => { c.screen = false; cx.notify(); })))
        .child(div().text_color(local ? t.muted : t.blocked).child(local ? "This computer only · journal survives panel copies" : "Remote compose unavailable. Switch to a local space; nothing will be redirected."))
        .when(local, (e) => e.child(div().text_color(["blocked", "uncertain", "armed", "dispatch_intent"].includes(c.state) ? t.blocked : t.foreground).child(stateText[c.state] || "Compose state unavailable.")))
        .when(Boolean(c.error), (e) => e.child(div().text_color(t.ansi1).child(c.error)))
        .when(Boolean(c.observation), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Last observation: ${c.observation}`)))
        .when(Boolean(c.id), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Intent: ${c.id}`)))
        .when(local && !c.working, (e) => e.child(button("ocq-compose-refresh", "Refresh journal status", (cx) => this.refreshCompose(cx))))
        .when(local && c.state === "idle" && Boolean(c.context), (e) => e.child(v_flex().gap(6)
          .child(div().child(`Project: ${c.context?.project}\nLane: ${c.context?.lane}\nOwner job: ${c.context?.job}\nTo: integrator:${c.context?.project}`))
          .child(div().child("Subject"))
          .child(Input.new(this.composeSubject).id("ocq-compose-subject").accessibility_label("Message subject").h(34).px(8).border_1().border_color(t.border).text_color(t.foreground))
          .child(div().child("Message"))
          .child(Textarea.new(this.composeBody).id("ocq-compose-body").accessibility_label("Message body").h(70).px(8).border_1().border_color(t.border).text_color(t.foreground))
          .child(div().text_size(12).text_color(t.muted).child("Typing does not send. Enter moves to the message; Ctrl+Enter leaves it, then P prepares."))
          .when(!c.working, (e) => e.child(button("ocq-compose-prepare", "Prepare message", (cx) => this.prepareCompose(cx))))))
        .when(local && c.state === "idle" && !c.context, (e) => e.child(div().text_color(t.muted).child("Close and choose a lane to compose an update to the integrator.")))
        .when(local && !c.working && !request && (["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))), (e) => e.child(button("ocq-compose-preview", "Preview exact retained message", (cx) => this.previewCompose(cx))))
        .when(Boolean(local && request), (e) => e.child(v_flex().flex_1().min_h(0).gap(6)
          .child(div().font_semibold().child(`Retained preview · ${c.page + 1}/${previewPages.length}`))
          .child(div().text_size(12).text_color(t.muted).child("Quoted fields escape controls and invisible characters; continuations preserve their order."))
          .child(div().flex_1().min_h(0).overflow_y_scroll().child(previewPages[c.page]))
          .when(!c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-previous", "Previous", (cx) => { c.page = Math.max(0, c.page - 1); cx.notify(); }))
            .child(button("ocq-compose-next", "Next", (cx) => { c.page = Math.min(previewPages.length - 1, c.page + 1); cx.notify(); }))))
          .when(!c.working && !c.confirm, (e) => e.child(h_flex().gap(8)
            .child(button("ocq-compose-cancel", "Cancel before attempt", (cx) => this.composeAction("cancel", cx)))
            .when(c.state === "prepared" && c.preview?.state === "prepared" && c.page === previewPages.length - 1, (e) => e.child(button("ocq-compose-review", "Review send…", (cx) => { c.confirm = true; cx.notify(); })))))
          .when(!c.working && c.state === "prepared" && c.preview?.state === "prepared" && c.confirm, (e) => e.child(v_flex().gap(6)
            .child(div().text_color(t.blocked).child("Send this exact retained message once? A lost response remains uncertain."))
            .child(h_flex().gap(8).child(button("ocq-compose-back", "Back", (cx) => { c.confirm = false; cx.notify(); }))
              .child(button("ocq-compose-confirm", "Confirm send once", (cx) => this.composeAction("dispatch", cx))))))))
        .when(local && !c.working && ["dispatch_intent", "uncertain"].includes(c.state), (e) => e.child(button("ocq-compose-reconcile", "Check stored receipt", (cx) => this.composeAction("reconcile", cx))))
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
          .when(this.composeLocal() && this.compose.state === "idle", (e) => e.children((project?.lanes ?? []).map((lane) => button(`ocq-compose-lane-${lane.lane_id}`, `Compose update · ${lane.lane_id}`, (cx) => this.openCompose(lane, cx)))))
          .child(div().pt(8).font_semibold().child("Completion history"))
          .child(div().text_size(12).text_color(t.muted).child(`Recent 20 · ${this.collections.results.recentReadAt || "not yet read"}; historical pages refresh separately`))
          .when(Boolean(this.collections.results.recentError), (e) => e.child(div().text_color(t.ansi1).child(`Recent results read failed: ${this.collections.results.recentError}`)))
          .when(Boolean(this.collections.results.error), (e) => e.child(div().text_color(t.ansi1).child(`Results read failed: ${this.collections.results.error}`)))
          .when(this.collections.results.loading, (e) => e.child(div().child("Loading results…")))
          .when(this.collections.results.loaded && !this.collections.results.items.length, (e) => e.child(div().text_color(t.dim).child("No completion events.")))
          .children(this.collections.results.items.map((r) => v_flex().gap(2)
            .child(button(`ocq-result-${r.event.id}`, `${r.role} · historical ${r.event.outcome} · current ${r.current_job.state} · ${r.job_id}`, (cx) => this.openTarget("result", r.event.id, cx)))
            .children((r.result_summary?.associations ?? []).map((a) => button(`ocq-result-manifest-${a.id}`, `Read ${a.kind} · ${a.verification}`, (cx) => this.openTarget("manifest", a.manifest_digest, cx))))))
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
          .child(div().pt(8).text_size(12).text_color(t.muted).child("Stored observations only. Imported claims are unverified; worktree drift is not checked. Opening mail does not mark it read.")))));
  }
}
