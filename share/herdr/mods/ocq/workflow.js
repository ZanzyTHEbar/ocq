import { div } from "gpui-kit";
import { v_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { spaces } from "herdr";
import { bridgeCommand, compatibility } from "./compatibility.js";

const protocol = "ocq-operator-workflow-v1";
const hex = value => /^[0-9a-f]{64}$/.test(value || "");
const token = prefix => `${prefix}-${randomBytes(16).toString("hex")}`;
const pin = b => JSON.stringify([b?.image_sha256, b?.bridge_context, b?.queue_identity, b?.observation_context, b?.observation_store_context]);
const activeTab = () => { const space = spaces().find(s => s.active); return [space?.id, space?.tabs.find(t => t.active)?.id]; };
const earlierGoal = "Earlier goal for Main";

function savedSendMessage(value, attempted) {
  switch (value?.state) {
    case "ready": return "The exact goal was found in Main’s conversation. This does not confirm the goal is complete.";
    case "busy": return "The exact goal was found in Main’s conversation, and Main was active when checked.";
    case "prompt_pending": return "Main’s inbox acknowledged this goal. Main has not been observed reading it.";
    case "prepared": return attempted ? "The saved draft was found, but its send could not be confirmed." : "The saved draft is prepared and unsent.";
    case "prompt_attempted_unknown": return value.reason === "exact retained input not observed; never resend" ?
      "The exact goal was not observed in Main’s conversation or inbox. The earlier send remains unconfirmed." :
      "The check could not finish verifying the exact goal in Main. The earlier send remains unconfirmed.";
    case "missing_or_unknown": return "The original Main session could not be observed. The earlier send remains unconfirmed.";
    case "mismatch":
    case "role_unverified": return "The original Main session or role could not be verified. The earlier send remains unconfirmed.";
    default: return "Could not confirm the saved send to Main.";
  }
}

// Authoring is separate from project observations so first-project setup does
// not require the binding it is about to create. Core journals remain authority.
export class GoalComposer {
  constructor(host) {
    this.host = host; this.choices = {}; this.draftText = { goal: "", label: "" };
    this.compatibility = host.compatibility || compatibility;
    this.command = (args, options) => bridgeCommand(args, { ...options, compatibility: this.compatibility });
    this.label = InputState.new({ placeholder: "Project name" });
    this.goal = TextareaState.new({ placeholder: "What should this goal accomplish?", rows: 4 });
    // Retain text while its native input is live; leaving a project must not
    // need to dereference an input whose owning view has already gone away.
    for (const key of ["label", "goal"]) this[key].on("change", (_e, cx) => {
      this.draftText[key] = this[key].value(); cx.notify();
    });
    for (const field of [this.label, this.goal]) {
      field.on("focus", (_e, cx) => { this.focused = true; cx.notify(); });
      field.on("blur", (_e, cx) => { this.focused = false; cx.notify(); });
    }
    try {
      this.saved = host.root.workflowPointers ?? JSON.parse(localStorage.getItem("operator-workflow-pointers-v1") || "{}");
      if (!this.saved || typeof this.saved !== "object" || Array.isArray(this.saved)) throw Error("Invalid pointers");
      host.root.workflowPointers = this.saved;
    }
    catch (_) { this.storageError = true; }
  }
  scope() {
    const h = this.host, b = this.compatibility.status;
    return !h.closed && h.panelCurrent() && this.compatibility.machineReady() && b && {
      machine: h.machine,
      ...(h.sourceKey ? { source: h.sourceKey } : {}),
      folder: h.folder, space: b.space_id, queue: b.queue_identity, store: b.observation_store_context,
      bridge: b.observation_context, image: b.image_sha256, epoch: h.panelEpoch, owner: h.generation,
      project: h.selected || null,
      retiredRevision: h.mainClient.retirement === "deleted" ? String(h.board?.project?.current_binding?.revision) : null,
      mainSetup: h.mainClient.selectedMain?.setup_id || null,
      ...(h.setupContext ? { tab: h.setupContext.tab, terminal: h.setupContext.terminal, pane: h.setupContext.pane, cwd: h.setupContext.cwd, branch: h.setupContext.branch } : {}),
    };
  }
  current(scope) { return !!scope && this.host.source.online() && JSON.stringify(scope) === JSON.stringify(this.scope()); }
  restoreDraft(draft) {
    this.draftText = { ...draft };
    this.goal.set_value(draft.goal); this.label.set_value(draft.label);
  }
  key(s) {
    const group = [...(s.source ? [s.source] : []), s.space, s.queue, s.folder, s.project];
    if (s.retiredRevision) return JSON.stringify([...group, "replace", s.retiredRevision]);
    const base = JSON.stringify(group), saved = this.saved?.[base];
    const prior = saved?.mode === "send" && this.host.mainClient.mains.find(m => m.operation_id === saved.operation);
    if (prior && s.mainSetup && prior.setup_id !== s.mainSetup) return JSON.stringify([...group, "main", s.mainSetup]);
    return base;
  }
  args(s, project, operation) { return [s.space, s.queue, project, operation, s.machine ?? "local", s.folder, s.store, s.image]; }
  async read(action, args) {
    const r = await this.command([action, "--", ...args], { on: this.host.machine });
    if (r.code !== 0 || Buffer.byteLength(r.stdout || "") > 1048576) throw Error("Workflow unavailable");
    return JSON.parse(r.stdout);
  }
  async persist() {
    if (this.storageError || !this.saved || typeof this.saved !== "object" || Array.isArray(this.saved)) throw Error("Pointer storage unavailable");
    localStorage.setItem("operator-workflow-pointers-v1", JSON.stringify(this.saved)); await localStorage.flush();
  }
  run(cx, action) {
    const scope = this.scope();
    if (!this.current(scope) || this.busy) return;
    this.busy = true; this.error = null;
    const origin = JSON.stringify(activeTab());
    const intended = () => this.current(scope) && JSON.stringify(activeTab()) === origin;
    cx.spawn(async cx => {
      try { await action(scope, cx, intended); }
      catch (_) {
        if (this.current(scope)) {
          const record = this.saved?.[this.key(scope)];
          this.error = record?.phase === "start_attempted"
            ? "Could not check this saved action. It was not repeated."
            : record?.phase === "author_uncertain"
              ? "Could not confirm the draft was saved. Check the saved goal before continuing."
              : "Could not refresh project setup. Check the project before trying again.";
        }
      }
      finally { this.busy = false; cx.notify(); }
    }); cx.notify();
  }
  async readiness(s) {
    const value = await this.read("workflow-context", [s.space, s.queue]);
    if (!this.current(s) || value.protocol !== protocol || typeof value.ready !== "boolean" || !Array.isArray(value.selections) ||
        value.ready && (value.repo !== s.folder || typeof value.branch !== "string")) throw Error("Workflow context changed");
    this.capability = value; this.capabilityScope = JSON.stringify(s);
    if (value.ready) {
      this.choices = {};
      for (const role of ["main", "implementation", "review"]) {
        const choices = value.selections.filter(item => item.role === role);
        if (choices.length === 1 && !this.choices[role]) this.choices[role] = choices[0].id;
      }
      if (!this.labelSeeded) {
        this.labelSeeded = true;
        if (!this.label.value()) {
          this.draftText.label = value.repo.split(/[\\/]/).filter(Boolean).pop() || "";
          this.label.set_value(this.draftText.label);
        }
      }
    }
    return value;
  }
  submit(mode, cx) {
    const draft = this.review?.text ?? (mode === "send" ? this.goal.value() : this.label.value());
    if (!draft.trim() || Buffer.byteLength(draft) > (mode === "send" ? 14 << 10 : 160)) return;
    this.run(cx, async (s, task, intended) => {
      const fallback = this.serverDraft();
      const retained = this.saved?.[this.key(s)] || fallback;
      if (retained) {
        if (this.review?.record === retained && retained.phase === "prepared" && intended()) {
          if (retained === fallback && !this.saved[this.key(s)]) {
            this.saved[this.key(s)] = retained;
            await this.persist();
          }
          await this.startPrepared(s, task, intended);
        }
        return;
      }
      const ready = await this.readiness(s);
      if (!intended()) return;
      if (!ready.ready || mode !== "send" && this.setupReason(s)) {
        this.error = "Main is not ready. Ask the machine owner to check project setup.";
        return;
      }
      const key = this.key(s);
      if (this.saved?.[key]) throw Error("Recover retained operation first");
      const project = s.project || token("proj");
      const operation = token(mode);
      const binding = this.host.board?.project?.current_binding;
      const input = { protocol, mode };
      if (mode !== "send") Object.assign(input, {
        label: draft, expected_revision: mode === "create" ? "0" : String(binding?.revision),
        main_selection: this.choices.main, implementation_selection: this.choices.implementation, review_selection: this.choices.review,
      });
      if (mode === "send") input.goal = draft;
      const record = { project, operation, mode, phase: "author_uncertain" };
      this.status = null; this.review = null;
      this.saved[key] = record; await this.persist();
      if (!intended()) return;
      const b = await this.compatibility.check(intended, true, true);
      const epoch = this.compatibility.epoch, serial = this.compatibility.serial, pins = pin(b);
      const current = () => intended() && this.compatibility.epoch === epoch && this.compatibility.serial === serial && pin(this.compatibility.status) === pins;
      const response = await this.command(["main-author-v1", ...this.args(s, project, operation)], {
        on: s.machine, current, payload: JSON.stringify(input),
      });
      if (!current() || response.code !== 0) throw Error("Retained author response unavailable");
      const status = JSON.parse(response.stdout);
      if (status.operation_id !== operation || status.project_id !== project || status.protocol !== "ocq-main-v1") throw Error("Different operation");
      record.phase = "prepared"; await this.persist();
      await this.preview(s, record);
      if (mode === "send" && this.review?.text !== draft) throw Error("Retained goal differs from submitted text");
      if (mode !== "send" && this.review?.text !== draft) throw Error("Retained project differs from submitted name");
      await this.startPrepared(s, task, intended);
    });
  }
  async preview(s, record) {
    const b = await this.compatibility.check(() => this.current(s), true, true);
    const epoch = this.compatibility.epoch, serial = this.compatibility.serial, pins = pin(b);
    const response = await this.command(["main-preview", "--work-observation-context", s.bridge, "--", ...this.args(s, record.project, record.operation)], { on: s.machine });
    const value = response.code === 0 && JSON.parse(response.stdout), c = value?.context;
    if (!this.current(s) || epoch !== this.compatibility.epoch || serial !== this.compatibility.serial || pin(this.compatibility.status) !== pins ||
      value.protocol !== "ocq-main-v1" || !hex(value.preview_sha256) || value.status?.project_id !== record.project ||
      value.status.operation_id !== record.operation || value.status.mode !== record.mode || value.status.repo !== s.folder ||
      value.status.state !== "prepared" ||
      record.mode === "send" && (value.status.session_id !== this.host.mainClient.selectedMain?.session_id ||
        value.status.setup_id !== this.host.mainClient.selectedMain?.setup_id ||
        record.intent && record.intent !== value.status.intent_sha256) ||
      c?.protocol !== "ocq-main-v1" || c.action !== "start" || c.image_role !== "bridge" || c.peer !== (s.machine ?? "local") ||
      c.space !== s.space || c.folder !== s.folder || c.queue_identity !== s.queue || c.store_context !== s.store || c.image_sha256 !== s.image ||
      value.role?.session_id !== value.status.session_id || value.role.project_id !== record.project ||
      value.role.repo !== s.folder || value.role.queue_identity !== s.queue || value.role.store_context !== s.store ||
      !hex(value.role.sha256) || typeof value.role.instructions !== "string" ||
      ["input_json", "session_json", "registration_json", "prompt_json"].some(key => typeof value[key] !== "string"))
      throw Error("Exact Main preview/context mismatch");
    const hc = await this.host.nativeHC.load();
    if (!this.current(s) || epoch !== this.compatibility.epoch || serial !== this.compatibility.serial || pin(this.compatibility.status) !== pins) throw Error("Native review context changed");
    const input = JSON.parse(value.input_json);
    const prefix = `OCQ retained goal operation: ${record.operation}. `;
    const start = typeof input.prompt === "string" && input.prompt.startsWith(prefix) ? input.prompt.indexOf("\n\n") : -1;
    const text = record.mode === "send" ? (start >= 0 ? input.prompt.slice(start + 2) : input.prompt) : input.label;
    if (typeof text !== "string" || !text.trim()) throw Error("Retained text is unavailable");
    this.review = { scope: s, record, value, epoch, serial, pins, hc, text };
  }
  pinned(r) {
    return r && this.review === r && this.current(r.scope) && this.compatibility.workReady() &&
      r.hc === this.host.nativeHC.review && r.hc.context === this.host.nativeHC.context() &&
      this.compatibility.epoch === r.epoch && this.compatibility.serial === r.serial && pin(this.compatibility.status) === r.pins;
  }
  async startPrepared(s, cx, intended) {
    const review = this.review;
    if (!intended() || !this.pinned(review) || review.record.phase !== "prepared") return;
      const record = review.record;
      // Server rechecks actual capabilities too. No stale readiness can authorize
      // a new prompt after its agent dispatcher or pinned native backend is lost.
      const ready = await this.readiness(s);
      if (!intended() || !this.pinned(review)) return;
      if (!ready.ready) {
        this.error = "Main is not ready. The goal has not been sent.";
        return;
      }
      if (review.hc !== this.host.nativeHC.review || review.hc.context !== this.host.nativeHC.context()) throw Error("Reviewed native backend changed");
      // The explicit action confirms the semantic view of these SAME frozen
      // bytes and backend identity, never permissions.
      record.phase = "start_attempted"; await this.persist();
      if (!intended() || !this.pinned(review)) {
        record.phase = "prepared"; await this.persist(); return;
      }
      const response = await this.command(["main-start", ...this.args(s, record.project, record.operation), review.value.preview_sha256],
        { on: s.machine, current: () => intended() && this.pinned(review) });
      if (!this.current(s) || response.code !== 0) throw Error("Start outcome unknown");
      const value = JSON.parse(response.stdout);
      if (value.protocol !== "ocq-main-v1" || value.project_id !== record.project || value.operation_id !== record.operation ||
          value.mode !== record.mode || value.session_id !== review.value.status.session_id ||
          value.intent_sha256 !== review.value.status.intent_sha256 || !["ready", "busy", "prompt_pending"].includes(value.state) ||
          record.mode !== "send" && value.state !== "ready") throw Error("Start outcome unknown");
      await this.accept(s, record, value, cx, review.text);
  }
  async accept(s, record, value, cx, text) {
      this.status = value;
      const key = this.key(s);
      delete this.saved[key];
      try { await this.persist(); } catch (error) { this.saved[key] = record; this.storageError = true; throw error; }
      if (!this.current(s)) return;
      this.review = null; this.error = null;
      if (record.mode === "send" && this.draftText.goal === text) {
        this.draftText.goal = ""; this.goal.set_value("");
      }
      this.host.refresh(cx);
  }
  readSaved(cx, recover = false) {
    this.run(cx, async (s, task, intended) => {
      const record = this.saved?.[this.key(s)] || this.serverDraft();
      if (!record) return;
      const attempted = record.phase === "start_attempted", checkSend = recover && record.mode === "send";
      const main = this.host.mainClient.selectedMain;
      const earlier = this.host.mainClient.goals.find(g => g.main.operation_id === record.operation);
      const checked = checkSend && { scope: JSON.stringify(s), operation: record.operation,
        session: main?.session_id, title: earlier?.title || earlierGoal, attempted };
      let value;
      try {
        value = await this.read(recover && attempted ? "main-recover" : "main-status", [s.space, s.queue, record.project, record.operation]);
        if (!this.current(s) || value.protocol !== "ocq-main-v1" || value.operation_id !== record.operation ||
          value.project_id !== record.project || value.mode !== record.mode ||
          checkSend && (!checked.session || value.session_id !== checked.session || value.setup_id !== s.mainSetup ||
            value.repo !== s.folder || this.host.mainClient.selectedMain?.session_id !== checked.session ||
            (earlier || record.intent) && value.intent_sha256 !== (earlier?.main.intent_sha256 || record.intent)))
          throw Error("Different recovered operation");
      } catch (error) {
        if (!checkSend) throw error;
        if (this.current(s)) this.checkedSend = { ...checked, message: "Could not verify this saved action. Nothing was resent." };
        return;
      }
      if (checkSend) this.checkedSend = { ...checked, message: savedSendMessage(value, attempted) };
      this.status = value;
      if (recover && intended() && record.phase === "start_attempted" &&
          (value.state === "ready" || record.mode === "send" && ["busy", "prompt_pending"].includes(value.state))) {
        await this.accept(s, record, value, task);
        if (checkSend && this.current(s)) this.checkedSend.accepted = true;
        return;
      }
      if (record.phase === "author_uncertain" && value.state === "prepared") {
        record.phase = "prepared"; await this.persist();
      }
      if (record.phase === "prepared") await this.preview(s, record);
    });
  }
  setupReason(s) {
    const ready = this.capabilityScope === JSON.stringify(s) && this.capability;
    if (!ready?.ready) return "Ask the machine owner to enable project setup.";
    if (!this.label.value().trim()) return "Enter a project name.";
    const missing = ["main", "implementation", "review"].filter(role => !ready.selections.some(v => v.role === role && v.id === this.choices[role]));
    return missing.length ? "Ask the machine owner to configure one default per role." : null;
  }
  serverDraft() {
    const main = this.host.mainClient.selectedMain;
    const drafts = (this.host.mainClient.goals || []).map(g => g.main).filter(g =>
      g.state === "prepared" && g.setup_id === main?.setup_id && g.session_id === main?.session_id);
    this.ambiguousDraft = drafts.length > 1;
    if (drafts.length !== 1) { this.observedDraft = null; return null; }
    const draft = drafts[0];
    if (this.observedDraft?.operation !== draft.operation_id || this.observedDraft?.intent !== draft.intent_sha256) {
      this.observedDraft = { project: draft.project_id, operation: draft.operation_id, mode: "send",
        phase: "prepared", intent: draft.intent_sha256 };
    }
    return this.observedDraft;
  }
  render(cx, t, button, selectedMain, legacyOnly = false) {
    const s = this.scope();
    const box = v_flex().id("ocq-goal-composer").w_full().min_w(0).gap(6);
    const note = text => div().text_size(12).text_color(t.muted).child(text);
    const mode = selectedMain ? "send" : this.host.mainClient.retirement === "deleted" ? "replace" : this.host.selected ? "adopt" : "create";
    const setup = mode === "replace" ? "Create Main" : "Set up Main";
    box.child(div().font_semibold().child(selectedMain ? (legacyOnly ? earlierGoal : "New goal") : setup));
    const input = () => Textarea.new(this.goal).id("ocq-workflow-goal").accessibility_label("Goal for Main")
      .w_full().min_w(0).h(92).rounded(5).border_1().border_color(t.border).bg(t.input).p(8);
    if (!s || this.storageError) {
      if (legacyOnly) return null;
      if (selectedMain) box.child(input()).child(button("ocq-submit-goal", "Send goal", () => {}, false));
      return box.child(note(!s ? "Ask the machine owner to enable project actions." : "Could not read saved drafts. Reopen the panel."));
    }
    const key = JSON.stringify(s);
    const record = this.saved?.[this.key(s)] || this.serverDraft();
    const last = this.checkedSend;
    const checked = selectedMain && last?.scope === key && last.session === this.host.mainClient.selectedMain?.session_id &&
      (!record || last.operation === record.operation) ? last : null;
    const doNotResend = legacyOnly ? "Don’t resend this earlier goal. New goal starts a separate task." : "Don’t resend this earlier goal.";
    const showChecked = () => {
      if (checked.title !== earlierGoal || !legacyOnly) box.child(div().truncate().font_semibold().child(checked.title));
      box.child(note(checked.message));
      if (checked.attempted) box.child(note(doNotResend));
    };
    // The journal projection can still show the just-accepted draft until the
    // next refresh. Keep the exact check result visible instead of an old CTA.
    if (checked?.accepted && (legacyOnly || record && !this.saved?.[this.key(s)])) {
      showChecked();
      if (!record && this.ambiguousDraft) box.child(note("Several unsent goals are saved. Ask the project owner which one to continue."));
      return box;
    }
    if (legacyOnly && !record && !this.ambiguousDraft && !checked) return null;
    if (!record && this.ambiguousDraft) return box.child(note("Several unsent goals are saved. Ask the project owner which one to continue."));
    if (this.formContext !== key) {
      this.formContext = key; this.review = null; this.status = null; this.error = null;
      this.readContext = null;
    }
    const readKey = JSON.stringify([key, record?.operation]);
    if (this.readContext !== readKey && !this.busy && (record || !selectedMain)) {
      this.readContext = readKey;
      cx.spawn(async task => {
        if (!this.current(s)) return;
        if (record) this.readSaved(task);
        else this.run(task, scope => this.readiness(scope));
      });
    }
    if (record) {
      const earlier = this.host.mainClient.goals.find(g => g.main.operation_id === record.operation);
      const retained = this.review?.record === record && this.pinned(this.review);
      if (record.phase === "prepared" && retained) {
        box.child(div().max_h(160).overflow_y_scroll().child(this.review.text))
          .child(button("ocq-submit-goal", record.mode === "send" ? (legacyOnly ? "Send to Main" : "Send goal") : setup, task => this.submit(record.mode, task), !this.busy));
      } else {
        if (record.phase === "start_attempted") {
          if (checked && !this.busy) showChecked();
          else {
            if (earlier?.title || !legacyOnly) box.child(div().truncate().font_semibold().child(earlier?.title ||
              (record.mode === "send" ? earlierGoal : setup)));
            box.child(note(this.busy ? "Checking the saved action…" :
              record.mode === "send" ? (earlier?.work.some(w => w.job?.session_id) ?
                "This goal has linked sessions, but the original send to Main is not confirmed." :
                "The earlier send to Main is unconfirmed.") : "Main setup is unconfirmed. Don’t repeat it."));
            if (!this.busy && record.mode === "send") box.child(note(doNotResend));
          }
        } else box.child(note(this.busy ? "Reading the saved draft…" : checked?.message || "This draft is still unsent."));
        if (this.readContext === readKey)
          box.child(button("ocq-check-goal", "Check saved action", task => this.readSaved(task, true), !this.busy));
      }
    } else if (selectedMain) {
      if (checked) showChecked();
      box.child(input())
        .child(button("ocq-submit-goal", this.busy ? "Sending…" : "Send goal", task => this.submit(mode, task),
          !this.busy && Boolean(this.goal.value().trim()) && Buffer.byteLength(this.goal.value()) <= 14 << 10));
      if (Buffer.byteLength(this.goal.value()) > 14 << 10) box.child(note("Shorten this goal before sending."));
    } else {
      box.child(Input.new(this.label).id("ocq-workflow-label").accessibility_label("Project name")
        .w_full().h(36).px(8).border_1().border_color(t.border).bg(t.input).text_color(t.foreground));
      const reason = this.setupReason(s);
      box.child(button("ocq-submit-setup", this.busy ? "Checking…" : setup, task => this.submit(mode, task),
        !this.busy && !reason));
      if (!this.busy && reason) box.child(note(reason));
    }
    if (this.error) box.child(note(this.error));
    return box;
  }
}
