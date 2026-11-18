import { div } from "gpui-kit";
import { v_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { randomBytes } from "crypto";
import { Buffer } from "buffer";
import { bridgeCommand, compatibility } from "./compatibility.js";

const protocol = "ocq-operator-workflow-v1";
const hex = value => /^[0-9a-f]{64}$/.test(value || "");
const token = prefix => `${prefix}-${randomBytes(16).toString("hex")}`;
const pin = b => JSON.stringify([b?.image_sha256, b?.bridge_context, b?.queue_identity, b?.observation_context, b?.observation_store_context]);

// Authoring is separate from project observations so first-project setup does
// not require the binding it is about to create. Core journals remain authority.
export class OperatorWorkflow {
  constructor(host) {
    this.host = host; this.choices = {};
    this.label = InputState.new({ placeholder: "Project name" });
    this.goal = TextareaState.new({ placeholder: "What should Main accomplish?", rows: 4 });
    for (const field of [this.label, this.goal]) field.on("change", (_e, cx) => { this.review = null; cx.notify(); });
    for (const field of [this.label, this.goal]) {
      field.on("focus", (_e, cx) => { this.focused = true; cx.notify(); });
      field.on("blur", (_e, cx) => { this.focused = false; cx.notify(); });
    }
    try {
      this.saved = JSON.parse(localStorage.getItem("operator-workflow-pointers-v1") || "{}");
      if (!this.saved || typeof this.saved !== "object" || Array.isArray(this.saved)) throw Error("Invalid pointers");
    }
    catch (_) { this.storageError = true; }
  }
  scope() {
    const h = this.host, b = compatibility.status;
    return !h.closed && h.panelCurrent() && h.machine === null && b && {
      folder: h.folder, space: b.space_id, queue: b.queue_identity, store: b.observation_store_context,
      bridge: b.observation_context, image: b.image_sha256, epoch: h.panelEpoch, owner: h.generation,
      project: h.selected || null,
    };
  }
  current(scope) { return !!scope && JSON.stringify(scope) === JSON.stringify(this.scope()) && this.host.workOperator.visible(); }
  key(s) { return JSON.stringify([s.space, s.queue, s.folder, s.project]); }
  args(s, project, operation) { return [s.space, s.queue, project, operation, "local", s.folder, s.store, s.image]; }
  async read(action, args) {
    const r = await bridgeCommand([action, "--", ...args], { on: null });
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
    cx.spawn(async cx => {
      try { await action(scope, cx); }
      catch (_) { if (this.current(scope)) this.error = "Operation unavailable or uncertain. Check the retained operation; do not resend."; }
      finally { this.busy = false; cx.notify(); }
    }); cx.notify();
  }
  async readiness(s) {
    const value = await this.read("workflow-context", [s.space, s.queue]);
    if (!this.current(s) || value.protocol !== protocol || typeof value.ready !== "boolean" || !Array.isArray(value.selections)) throw Error("Workflow context changed");
    this.capability = value; this.capabilityScope = JSON.stringify(s);
    return value;
  }
  open(cx) { this.run(cx, async s => { await this.readiness(s); this.opened = true; }); }
  prepare(mode, cx) {
    this.run(cx, async s => {
      const ready = await this.readiness(s);
      if (!ready.ready) return;
      const key = this.key(s);
      if (this.saved?.[key]) throw Error("Recover retained operation first");
      const project = s.project || token("proj");
      const operation = token(mode);
      const binding = this.host.board?.project?.current_binding;
      const input = { protocol, mode };
      if (mode === "create" || mode === "adopt") Object.assign(input, {
        label: this.label.value(), expected_revision: mode === "create" ? "0" : String(binding?.revision),
        main_selection: this.choices.main, implementation_selection: this.choices.implementation, review_selection: this.choices.review,
      });
      if (mode === "send") input.goal = this.goal.value();
      const record = { project, operation, mode, phase: "author_uncertain" };
      this.status = null; this.review = null;
      this.saved[key] = record; await this.persist();
      if (!this.current(s)) return;
      const b = await compatibility.check(() => this.current(s), true, true);
      const epoch = compatibility.epoch, serial = compatibility.serial, pins = pin(b);
      const current = () => this.current(s) && compatibility.epoch === epoch && compatibility.serial === serial && pin(compatibility.status) === pins;
      const response = await bridgeCommand(["main-author-v1", ...this.args(s, project, operation)], {
        on: null, current, payload: JSON.stringify(input),
      });
      if (!current() || response.code !== 0) throw Error("Retained author response unavailable");
      const status = JSON.parse(response.stdout);
      if (status.operation_id !== operation || status.project_id !== project || status.protocol !== "ocq-main-v1") throw Error("Different operation");
      record.phase = "prepared"; await this.persist();
      await this.preview(s, record);
    });
  }
  async preview(s, record) {
    const b = await compatibility.check(() => this.current(s), true, true);
    const epoch = compatibility.epoch, serial = compatibility.serial, pins = pin(b);
    const response = await bridgeCommand(["main-preview", "--work-observation-context", s.bridge, "--", ...this.args(s, record.project, record.operation)], { on: null });
    const value = response.code === 0 && JSON.parse(response.stdout), c = value?.context;
    if (!this.current(s) || epoch !== compatibility.epoch || serial !== compatibility.serial || pin(compatibility.status) !== pins ||
      value.protocol !== "ocq-main-v1" || !hex(value.preview_sha256) || value.status?.project_id !== record.project ||
      value.status.operation_id !== record.operation || value.status.mode !== record.mode || value.status.repo !== s.folder ||
      c?.protocol !== "ocq-main-v1" || c.action !== "start" || c.image_role !== "bridge" || c.peer !== "local" ||
      c.space !== s.space || c.folder !== s.folder || c.queue_identity !== s.queue || c.store_context !== s.store || c.image_sha256 !== s.image ||
      value.role?.session_id !== value.status.session_id || value.role.project_id !== record.project ||
      value.role.repo !== s.folder || value.role.queue_identity !== s.queue || value.role.store_context !== s.store ||
      !hex(value.role.sha256) || typeof value.role.instructions !== "string" ||
      ["input_json", "session_json", "registration_json", "prompt_json"].some(key => typeof value[key] !== "string"))
      throw Error("Exact Main preview/context mismatch");
    const hc = await this.host.nativeHC.load();
    if (!this.current(s) || epoch !== compatibility.epoch || serial !== compatibility.serial || pin(compatibility.status) !== pins) throw Error("Native review context changed");
    this.review = { scope: s, record, value, epoch, serial, pins, hc,
      pages: this.host.workOperator.pagesFor({ main: value, native_backend: hc.value }), page: 0, seen: new Set() };
  }
  pinned(r) {
    return r && this.review === r && this.current(r.scope) && compatibility.workReady() &&
      r.hc === this.host.nativeHC.review && r.hc.context === this.host.nativeHC.context() &&
      compatibility.epoch === r.epoch && compatibility.serial === r.serial && pin(compatibility.status) === r.pins;
  }
  start(cx) {
    const review = this.review;
    if (!this.pinned(review) || review.seen.size !== review.pages.length || review.record.phase !== "prepared") return;
    this.run(cx, async s => {
      const record = review.record;
      // Server rechecks actual capabilities too. No stale readiness can authorize
      // a new prompt after its agent dispatcher or pinned native backend is lost.
      const ready = await this.readiness(s);
      if (!ready.ready || !this.pinned(review)) return;
      if (review.hc !== this.host.nativeHC.review || review.hc.context !== this.host.nativeHC.context()) throw Error("Reviewed native backend changed");
      // The explicit Create/Start confirms the backend bytes in the SAME full
      // retained review. This approves navigation identity, never permissions.
      this.host.nativeHC.approved = review.hc;
      record.phase = "start_attempted"; await this.persist();
      if (!this.pinned(review)) {
        record.phase = "prepared"; await this.persist(); return;
      }
      const response = await bridgeCommand(["main-start", ...this.args(s, record.project, record.operation), review.value.preview_sha256],
        { on: null, current: () => this.pinned(review) });
      this.review = null;
      if (!this.current(s) || response.code !== 0) throw Error("Start outcome unknown");
      const value = JSON.parse(response.stdout);
      if (value.project_id !== record.project || value.operation_id !== record.operation) throw Error("Different start receipt");
      this.status = value; this.host.workOperator.dirty = true;
      this.host.refresh(cx);
    });
  }
  recover(cx) {
    this.run(cx, async s => {
      const record = this.saved?.[this.key(s)];
      if (!record) return;
      const value = await this.read(record.phase === "start_attempted" ? "main-recover" : "main-status", [s.space, s.queue, record.project, record.operation]);
      if (!this.current(s) || value.protocol !== "ocq-main-v1" || value.operation_id !== record.operation ||
        value.project_id !== record.project || value.mode !== record.mode) throw Error("Different recovered operation");
      this.status = value;
      if (record.phase === "author_uncertain" && value.state === "prepared") {
        record.phase = "prepared"; await this.persist();
      }
      if (record.phase === "prepared") await this.preview(s, record);
    });
  }
  render(cx, t, button, selectedMain = null) {
    const s = this.scope();
    const box = v_flex().id("ocq-workflow").gap(6);
    if (!s) return box.child(div().text_color(t.muted).child("Project setup needs the verified local workflow connection."));
    const formContext = JSON.stringify([s.folder, s.space, s.queue, s.store, s.project]);
    if (this.formContext !== formContext) {
      this.formContext = formContext; this.opened = false; this.capability = null; this.review = null;
      this.choices = {}; this.status = null;
    }
    const record = this.saved?.[this.key(s)];
    const status = record && this.status?.project_id === record.project && this.status?.operation_id === record.operation ? this.status : null;
    const ready = this.capabilityScope === JSON.stringify(s) && this.capability;
    if (this.error) box.child(div().text_color(t.blocked).child(this.error));
    if (this.storageError) return box.child(div().child("Private operation pointers unavailable; authoring disabled."));
    if (!record && (!this.opened || !ready)) return box.child(button("ocq-main-workflow-setup", selectedMain ? "Write a goal" : "Set up project", cx => this.open(cx)));
    if (!record && !ready.ready) return box.child(div().text_color(t.blocked).child(ready.reason))
      .child(button("ocq-main-workflow-check", "Check setup", cx => this.open(cx)));
    if (record) {
      box.child(div().child(status ? `${status.mode} · ${status.state}` : "Retained operation"));
      if (!this.busy) box.child(button("ocq-main-workflow-recover", "Check retained operation", cx => this.recover(cx)));
      if (status && ["ready", "busy", "prompt_pending"].includes(status.state) && record.phase === "start_attempted") {
        box.child(button("ocq-main-workflow-return", "Return to Main", cx => {
          delete this.saved[this.key(s)]; this.persist().catch(() => { this.storageError = true; });
          this.status = null;
          this.review = null; this.host.select(record.project, cx);
        }));
      }
    } else if (!selectedMain) {
      box.child(div().font_semibold().child("Set up Main"))
        .child(div().text_size(12).child(`${ready.repo}\n${ready.branch}`))
        .child(Input.new(this.label).id("ocq-workflow-label").accessibility_label("Project name").h(36));
      for (const role of ["main", "implementation", "review"]) {
        box.child(div().child({ main: "Main", implementation: "Implementation", review: "Independent review" }[role]));
        for (const item of ready.selections.filter(v => v.role === role)) box.child(button(`ocq-main-choice-${item.id}`,
          `${this.choices[role] === item.id ? "Selected · " : ""}${item.label}`, cx => { this.choices[role] = item.id; cx.notify(); }));
      }
      if (!this.busy && this.label.value() && ["main", "implementation", "review"].every(role => this.choices[role]))
        box.child(button("ocq-main-workflow-review", "Review setup", cx => this.prepare(s.project ? "adopt" : "create", cx)));
    } else {
      box.child(Textarea.new(this.goal).id("ocq-workflow-goal").accessibility_label("Goal for Main").h(110));
      if (!this.busy && this.goal.value().trim()) box.child(button("ocq-main-goal-review", "Review goal", cx => this.prepare("send", cx)));
    }
    if (this.busy) box.child(div().text_color(t.muted).child("Checking retained operation…"));
    const review = this.review;
    if (review && this.pinned(review)) {
      review.seen.add(review.page);
      box.child(div().font_semibold().child(`Review ${review.record.mode === "send" ? "goal" : "setup"} · ${review.page + 1}/${review.pages.length}`))
        .child(div().child(review.pages[review.page]));
      if (!this.busy && review.page > 0) box.child(button("ocq-main-workflow-previous", "Previous", cx => { review.page--; cx.notify(); }));
      if (!this.busy && review.page < review.pages.length - 1) box.child(button("ocq-main-workflow-next", "Next", cx => { review.page++; cx.notify(); }));
      if (!this.busy && review.seen.size === review.pages.length && record?.phase === "prepared")
        box.child(div().text_size(12).text_color(t.muted).child("One retained operation. Human permissions stay in OpenCode; integration is not authorized."))
          .child(button("ocq-main-workflow-start", review.record.mode === "send" ? "Start goal" : review.record.mode === "adopt" ? "Adopt existing Main once" : "Create Main once", cx => this.start(cx)));
    }
    return box;
  }
}
