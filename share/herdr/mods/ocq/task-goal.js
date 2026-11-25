import { div } from "gpui-kit";
import { Textarea, v_flex } from "gpui-base";
import { Buffer } from "buffer";
import { randomBytes, createHash } from "crypto";
import { bridgeCommand } from "./compatibility.js";

const storage = "standalone-goals-v1";
const action = "work-task-submit-v1";
const hash = value => createHash("sha256").update(value).digest("hex");

export class TaskGoalComposer {
  constructor(host) {
    this.host = host;
    try {
      this.records = host.root.taskGoals ?? JSON.parse(localStorage.getItem(storage) || "{}");
      if (!this.records || typeof this.records !== "object" || Array.isArray(this.records)) throw Error();
      host.root.taskGoals = this.records;
    } catch (_) { this.storageError = true; }
  }
  supported() {
    return this.host.selected && this.host.compatibility.status?.actions?.[action] === "ocq-task-goal-v1/submit";
  }
  key(s) { return JSON.stringify([s.source, s.space, s.queue, s.store, s.project]); }
  async persist() {
    localStorage.setItem(storage, JSON.stringify(this.records));
    await localStorage.flush();
  }
  async settle(s, record, value, text) {
    if (value?.protocol !== "ocq-work-v1" || value.project_id !== s.project || value.request_id !== record.id ||
        value.task_goal?.intent_sha256 !== value.intent_sha256 || value.task_goal?.goal_sha256 !== record.digest ||
        value.state !== "recorded_observed" || value.receipt?.project_id !== s.project || !value.receipt.job_id) return false;
    const key = this.key(s);
    if (this.records[key] !== record || !this.host.sessions.current(s)) return false;
    delete this.records[key];
    try { await this.persist(); }
    catch (_) { this.records[key] = record; this.storageError = true; throw Error(); }
    if (this.host.sessions.current(s) && text === this.host.workflow.draftText.goal) {
      this.host.workflow.draftText.goal = "";
      this.host.workflow.goal.set_value("");
    }
    this.notice = "Goal queued.";
    return true;
  }
  run(cx, check = false) {
    const h = this.host, s = h.sessions.bound;
    if (this.busy || this.storageError || !this.supported() || !h.sessions.current(s) || !h.compatibility.machineReady()) return;
    const key = this.key(s);
    const text = h.workflow.draftText.goal;
    let record = this.records[key];
    if (check ? !record : record || !text.trim() || Buffer.byteLength(text) > 14 << 10) return;
    this.busy = true; this.notice = null;
    const current = () => h.sessions.current(s) && h.compatibility.machineReady();
    cx.spawn(async task => {
      try {
        if (!check) {
          const title = Array.from(text.trim().split(/\r?\n/, 1)[0].trim()).slice(0, 72).join("");
          record = { id: `task-${randomBytes(16).toString("hex")}`, digest: hash(text), title };
          this.records[key] = record;
          await this.persist();
        }
        if (!current()) return;
        const base = [s.space, s.queue, s.project, record.id];
        const args = check ? ["work-status", "--", ...base] :
          [action, ...base, s.machine ?? "local", s.folder, s.store, h.compatibility.status.image_sha256];
        const reply = await bridgeCommand(args, { on: s.machine, compatibility: h.compatibility, current,
          ...(check ? {} : { payload: JSON.stringify({ goal: text }) }) });
        if (!current()) return;
        if (reply.code !== 0 || Buffer.byteLength(reply.stdout || "") > 1048576 ||
            !await this.settle(s, record, JSON.parse(reply.stdout), hash(text) === record.digest ? text : undefined))
          this.notice = "No queue receipt confirmed for this goal. It was not repeated.";
        h.source.subscribe(true);
      } catch (_) { if (current()) this.notice = "Could not confirm this goal was queued. Don’t submit it again."; }
      finally { this.busy = false; task.notify(); }
    });
    cx.notify();
  }
  render(cx, t, button) {
    const note = text => div().text_size(12).text_color(t.muted).child(text);
    const box = v_flex().id("ocq-task-goal").w_full().min_w(0).gap(6).child(div().font_semibold().child("New goal"));
    const s = this.host.sessions.bound;
    const record = s && this.records?.[this.key(s)];
    const ready = !this.storageError && this.host.compatibility.machineReady() && this.host.sessions.current(s);
    const { goal, draftText } = this.host.workflow;
    if (record) {
      box.child(div().truncate().child(record.title || "Saved goal"));
      if (!this.notice) box.child(note(this.busy ? "Checking goal admission…" : "Queue receipt unconfirmed. Don’t submit it again."));
      box.child(button("ocq-task-check", "Check goal", task => this.run(task, true), ready && !this.busy));
    } else {
      box.child(Textarea.new(goal).id("ocq-task-goal-input").accessibility_label("Standalone goal")
        .w_full().min_w(0).h(92).rounded(5).border_1().border_color(t.border).bg(t.input).p(8));
      box.child(note("Starts a task session from this project’s saved base. Does not send to Main."));
      box.child(button("ocq-task-submit", this.busy ? "Queuing…" : "Start goal", task => this.run(task),
        ready && !this.busy && Boolean(draftText.goal.trim()) && Buffer.byteLength(draftText.goal) <= 14 << 10));
    }
    if (this.notice) box.child(note(this.notice));
    if (this.storageError) box.child(note("Saved goal storage is unavailable. No further task is allowed here."));
    return box;
  }
}
