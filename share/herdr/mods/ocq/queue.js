import { div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, TextareaState } from "gpui-base";
import { spaces } from "herdr";
import { exec } from "./compatibility.js";
import { Buffer } from "buffer";
import { QueuedCancellation } from "./queued-cancel.js";
import { CommandResolution } from "./command-resolution.js";
import { AgentReconciliation } from "./agent-reconcile.js";
import { AgentHandoff } from "./agent-handoff.js";
import { CommandSubmission } from "./command-submission.js";
import { AgentSubmission } from "./agent-submission.js";
import { CommandDispatcher } from "./command-dispatcher.js";

// Stored views plus explicit, journaled local global acknowledgements. Never
// query live execution or retry a mutation. Responses are context/identity guarded.
export class QueueInspection {
  constructor() {
    // Native input entities must be created in init, never during the render
    // that detects a machine/space change. Reset only the observation state.
    this.input = InputState.new({ placeholder: "Exact job or event ID" });
    this.input.on("change", (_e, cx) => cx.notify());
    this.input.on("focus", (_e, cx) => { this.inputFocused = true; cx.notify(); });
    this.input.on("blur", (_e, cx) => { this.inputFocused = false; cx.notify(); });
    this.resolveInput = TextareaState.new({ placeholder: "Private evidence supporting this operator assertion (not proof of success)", rows: 4 });
    this.resolveInput.on("change", (_e, cx) => cx.notify());
    this.resolveInput.on("focus", (_e, cx) => { this.inputFocused = true; cx.notify(); });
    this.resolveInput.on("blur", (_e, cx) => { this.inputFocused = false; cx.notify(); });
    this.commandInputs = CommandSubmission.inputs(this);
    this.agentInputs = AgentSubmission.inputs(this);
    this.reset();
  }

  reset() {
    this.generation = (this.generation || 0) + 1;
    this.jobs = {}; this.events = {}; this.detail = {}; this.log = {};
    this.ack = {};
    this.cancel = new QueuedCancellation(this);
    this.resolution = new CommandResolution(this);
    this.reconciliation = new AgentReconciliation(this);
    this.handoff = new AgentHandoff(this);
    this.submission = new CommandSubmission(this, this.commandInputs);
    this.agentSubmission = new AgentSubmission(this, this.agentInputs);
    this.dispatcher = new CommandDispatcher(this);
    this.resolveInput.set_value("");
    this.inspecting = false; this.inputFocused = false;
    this.focus = 0; this.bytePage = 0; this.targets = [];
  }

  current(owner, generation) {
    const active = spaces().find((s) => s.active);
    return generation === this.generation && owner.queueMode && active && active.id === owner.spaceID && active.machine === owner.machine && active.folder === owner.folder;
  }

  read(owner, slot, args, cx, notify = true) {
    if (!this.current(owner, this.generation)) return;
    const generation = this.generation, state = { loading: true }, machine = owner.machine;
    this[slot] = state;
    if (slot === "detail") { this.log = {}; this.ack = {}; this.cancel = new QueuedCancellation(this); this.resolution = new CommandResolution(this); this.reconciliation = new AgentReconciliation(this); this.handoff = new AgentHandoff(this); this.resolveInput.set_value(""); this.inspecting = true; this.focus = 0; owner.handle.focus(); }
    cx.spawn(async (cx) => {
      try {
        const response = await exec("ocq-herdr", args, { on: machine });
        if (!this.current(owner, generation) || this[slot] !== state) return;
        if (response.code !== 0) throw new Error(response.stderr || "Stored view unavailable. Refresh to try reading again.");
        state.data = JSON.parse(response.stdout);
        if (slot === "detail" && state.data.event) this.loadAck(owner, cx);
        if (slot === "detail" && state.data.job) this.cancel.load(owner, cx);
        if (slot === "detail" && state.data.job) this.resolution.load(owner, cx);
        if (slot === "detail" && state.data.job) this.reconciliation.load(owner, cx);
        if (slot === "detail" && state.data.job) this.handoff.load(owner, cx);
        if (slot === "log") {
          const data = state.data, bytes = Buffer.from(data.bytes_base64, "base64");
          if (bytes.length > 32768 || bytes.toString("base64") !== data.bytes_base64 || typeof data.next_offset !== "string") throw new Error("Invalid bounded log response");
          state.bytes = bytes; this.bytePage = 0;
        }
      } catch (e) {
        if (this.current(owner, generation) && this[slot] === state) { state.data = null; state.error = String(e); }
      } finally {
        if (this.current(owner, generation) && this[slot] === state) { state.loading = false; cx.notify(); }
      }
    });
    if (notify) cx.notify();
  }

  refresh(owner, cx, notify = true) {
    this.read(owner, "jobs", ["queue-jobs", "0"], cx, notify);
    this.read(owner, "events", ["notifications"], cx, notify);
  }

  ackScope(owner) {
    const data = this.detail.data, event = data?.event;
    if (!this.current(owner, this.generation) || owner.machine !== null || !event ||
      !/^[0-9a-f]{64}$/.test(data.queue_identity || "") || typeof event.sequence !== "string" || !/^[1-9][0-9]*$/.test(event.sequence)) return null;
    return { machine: "local", space_id: owner.spaceID, queue_identity: data.queue_identity, event_id: event.id, job_id: event.job_id, sequence: event.sequence };
  }

  ackSameScope(a, b) {
    return a && b && ["machine", "space_id", "queue_identity", "event_id", "job_id", "sequence"].every((key) => a[key] === b[key]);
  }

  ackCurrent(owner, a) {
    return this.ack === a && this.detail === a.detail && this.current(owner, a.generation) && this.ackSameScope(a.scope, this.ackScope(owner));
  }

  ackArgs(a) { const s = a.scope; return [s.space_id, s.queue_identity, s.event_id, s.job_id, s.sequence]; }

  async ackCall(a, action, id) {
    const result = await exec("ocq-herdr", [`notification-ack-${action}`, ...this.ackArgs(a), ...(id ? [id] : [])], { on: null, current: () => a.current?.() });
    if (result.code !== 0) throw new Error("Acknowledgement journal or exact event unavailable");
    const status = JSON.parse(result.stdout), observation = status.observation, s = a.scope;
    if (!this.ackSameScope(status.scope, s) || !["none", "prepared", "armed", "uncertain", "observed", "blocked"].includes(status.state)) throw new Error("Acknowledgement context mismatch");
    if (["prepared", "armed", "uncertain"].includes(status.state) && !/^[0-9a-f]{64}$/.test(status.intent_id || "")) throw new Error("Intent identity unavailable");
    if (observation && (observation.queue_identity !== s.queue_identity || observation.event?.id !== s.event_id || observation.event?.job_id !== s.job_id || observation.event?.sequence !== s.sequence)) throw new Error("Exact acknowledgement observation mismatch");
    if (status.state === "observed" && !observation?.event?.acknowledged_at) throw new Error("Acknowledgement not observed");
    return status;
  }

  loadAck(owner, cx) {
    const scope = this.ackScope(owner);
    if (!scope) return; // Remote selected-machine mutation is deliberately disabled.
    this.ack = { scope, detail: this.detail, generation: this.generation, working: false, status: null, error: null, confirm: false };
    const a = this.ack; a.current = () => this.ackCurrent(owner, a);
    this.ackAction(owner, "status", cx);
  }

  ackAction(owner, action, cx) {
    const a = this.ack;
    if (a.working || !this.ackCurrent(owner, a)) return;
    if (action === "prepare" && a.status?.state !== "none") return;
    if (action === "review" && a.status?.state !== "prepared") return;
    a.working = true; a.error = null; a.confirm = false;
    cx.spawn(async (cx) => {
      try {
        const status = await this.ackCall(a, action === "review" ? "status" : action);
        if (!this.ackCurrent(owner, a)) return;
        a.status = status;
        a.confirm = action !== "status" && status.state === "prepared";
        if (status.observation) a.detail.data = status.observation;
        if (status.state === "observed" && this.events.data?.items.some((event) => event.id === a.scope.event_id)) this.read(owner, "events", ["notifications"], cx);
      } catch (_e) {
        if (this.ackCurrent(owner, a)) { a.status = null; a.error = "Outcome unavailable. Check the exact retained event and journal; do not resend."; }
      } finally { if (this.ackCurrent(owner, a)) { a.working = false; cx.notify(); } }
    });
    cx.notify();
  }

  confirmAck(owner, cx) {
    const a = this.ack, id = a.status?.intent_id;
    if (a.working || !a.confirm || a.status?.state !== "prepared" || !/^[0-9a-f]{64}$/.test(id || "") || !this.ackCurrent(owner, a)) return;
    a.working = true; a.error = null;
    // Persisted arm must be acknowledged before one mutation process is launched.
    // Recovery of armed/uncertain states only checks; it never dispatches them.
    a.status = { state: "uncertain", scope: a.scope, intent_id: id };
    cx.spawn(async (cx) => {
      try {
        const claim = await this.ackCall(a, "arm", id);
        if (!this.ackCurrent(owner, a)) return;
        if (claim.state === "observed") return; // Another caller already acknowledged; read back only.
        if (claim.state !== "armed" || claim.intent_id !== id) throw new Error("Claim not acknowledged");
        // A context change after claim leaves a retained claim, not permission
        // to dispatch from another view. A call already launched may finish.
        if (!this.ackCurrent(owner, a)) return;
        await this.ackCall(a, "attempt", id); // Exactly one call; no mutation retry.
      } catch (_e) {
        if (this.ackCurrent(owner, a)) a.error = "Claim or acknowledgement response unavailable. Checking only; no resend.";
      } finally {
        if (this.ackCurrent(owner, a)) {
          try {
            const status = await this.ackCall(a, "status");
            if (!this.ackCurrent(owner, a)) return;
            a.status = status;
            if (a.status.observation) a.detail.data = a.status.observation;
            if (a.status.state === "observed") { a.error = null; this.read(owner, "events", ["notifications"], cx); }
          } catch (_e) { if (this.ackCurrent(owner, a)) { a.status = null; a.error = "Exact read-back unavailable. Outcome remains uncertain; use Check only."; } }
          if (this.ackCurrent(owner, a)) { a.working = false; a.confirm = false; cx.notify(); }
        }
      }
    });
    cx.notify();
  }

  renderAckConfirmation(owner, cx, t, button) {
    const a = this.ack, s = a.scope, event = this.detail.data.event;
    return v_flex().id("ocq-global-ack-confirmation").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx))
      .child(div().font_semibold().child("Acknowledge global notification?"))
      .child(v_flex().flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child("This removes the event from the global pending-notification list. It is NOT a project inbox acknowledgement. It does not release capacity, change the job, resend work or answer permissions."))
        .child(div().child(`Machine: this computer (local only)\nSpace: ${s.space_id}\nEvent: ${s.event_id}\nJob: ${s.job_id}\nExact sequence: ${s.sequence}\nState: ${event.state}\nOutcome: ${event.outcome}\nValidation: ${event.validation}\nRecorded: ${event.recorded_at}\nQueue identity: ${s.queue_identity}`))
        .child(div().text_size(12).text_color(t.muted).child("The intent is retained on this queue. Confirmation durably claims it before one attempt. A confirmed call may finish after you leave; another view cannot redirect or resend it. Missing/unavailable read-back stays uncertain.")))
      .child(v_flex().flex_none().gap(4)
        .when(a.working, (e) => e.child(div().child("Claiming once and checking the exact event…")))
        .when(!a.working, (e) => e.child(button("ocq-global-ack-confirm-once", "Acknowledge once · A", (cx) => this.confirmAck(owner, cx))))
        .when(!a.working, (e) => e.child(button("ocq-global-ack-confirm-back", "Back without acknowledging", (cx) => { a.confirm = false; cx.notify(); }))));
  }

  key(event, owner, cx) {
    if (this.dispatcher.screen) { this.dispatcher.key(event, owner, cx); return; }
    if (this.submission.screen) { this.submission.key(event, owner, cx); return; }
    if (this.agentSubmission.screen) { this.agentSubmission.key(event, owner, cx); return; }
    const key = event.key;
    if (event.modifiers.control || event.modifiers.alt || event.modifiers.platform) return;
    if (this.inputFocused) { if (key === "escape") { owner.handle.focus(); cx.stop_propagation(); } return; }
    if (this.reconciliation.confirm && this.reconciliation.current(owner)) {
      if (!this.reconciliation.working && key === "escape") { this.reconciliation.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "o") this.reconciliation.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.resolution.confirm && this.resolution.current(owner)) {
      if (!this.resolution.working && key === "escape") { this.resolution.confirm = false; this.resolution.preview = null; cx.notify(); }
      else if (!event.is_held && key === "r") this.resolution.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.ack.confirm) {
      if (!this.ack.working && key === "escape") { this.ack.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "a") this.confirmAck(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.cancel.confirm && this.cancel.current(owner)) {
      if (!this.cancel.working && key === "escape") { this.cancel.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "c") this.cancel.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (key === "escape") {
      if (this.inspecting) { this.inspecting = false; this.detail = {}; this.log = {}; this.focus = 0; }
      else owner.leaveQueue(cx);
      cx.notify();
    }
    if (key === "down" || key === "up") { this.focus = Math.max(0, Math.min(this.targets.length - 1, this.focus + (key === "down" ? 1 : -1))); cx.notify(); }
    if (key === "enter" && !event.is_held) this.targets[this.focus]?.(cx);
    if (["escape", "up", "down", "enter"].includes(key)) cx.stop_propagation();
  }

  render(owner, cx, t, button) {
    if (this.dispatcher.screen) return this.dispatcher.render(owner, cx, t, button);
    if (this.submission.screen) return this.submission.render(owner, cx, t, button);
    if (this.agentSubmission.screen) return this.agentSubmission.render(owner, cx, t, button);
    if (this.reconciliation.confirm && this.reconciliation.current(owner)) return this.reconciliation.confirmation(owner, cx, t, button);
    if (this.resolution.confirm && this.resolution.current(owner)) return this.resolution.confirmation(owner, cx, t, button);
    if (this.cancel.confirm && this.cancel.current(owner)) return this.cancel.confirmation(owner, cx, t, button);
    if (this.ack.confirm && this.ackCurrent(owner, this.ack)) return this.renderAckConfirmation(owner, cx, t, button);
    this.targets = [];
    const action = (id, label, fn) => {
      const index = this.targets.length; this.targets.push(fn);
      if (index === this.focus) this.focusLabel = label;
      return button(id, label, fn).when(index === this.focus, (e) => e.bg(t.hover));
    };
    const status = (state) => v_flex().gap(4)
      .when(Boolean(state.loading), (e) => e.child(div().child("Reading stored data…")))
      .when(Boolean(state.error), (e) => e.child(div().text_color(t.ansi1).child(state.error)))
      .when(Boolean(state.data?.read_at), (e) => e.child(div().text_size(12).text_color(t.muted).child(`Read started: ${state.data.read_at}`)));
    const jobs = this.jobs.data, events = this.events.data, detail = this.detail.data, log = this.log.data;
    // Keep natural content height in the root scroll viewport, like the native
    // forms; nested flex scrolling can strand command output below the pane.
    const content = v_flex().id(this.inspecting ? "ocq-queue-inspection" : "ocq-queue-content").flex_none().gap(8);
    if (!this.inspecting) {
      content.child(div().text_size(12).text_color(t.muted).child("Viewing and refreshing never acknowledge events or change jobs. Global acknowledgement requires explicit local confirmation. Recorded sessions and worktrees are not live availability proof."))
      .child(action("ocq-queue-refresh", "Refresh queue and pending events", (cx) => this.refresh(owner, cx)))
      .child(owner.machine === null ? action("ocq-queue-submit-command", "Submit general command / recover admission…", (cx) => this.submission.open(owner, cx)) : div().text_color(t.muted).child("Command submission disabled on remote machines; nothing redirects locally."))
      .child(owner.machine === null ? action("ocq-queue-submit-agent", "Submit standalone agent / recover admission…", (cx) => this.agentSubmission.open(owner, cx)) : div().text_color(t.muted).child("Agent submission disabled on remote machines; nothing redirects locally."))
      .child(owner.machine === null ? action("ocq-queue-command-dispatcher", "COMMANDS-ONLY dispatcher · start / status / drain…", (cx) => this.dispatcher.open(owner, cx)) : div().text_color(t.muted).child("Dispatcher controls disabled on remote machines."))
      .child(div().font_semibold().child("Queue jobs · receipt order"))
      .child(status(this.jobs));
    if (jobs) {
      if (!jobs.items.length) content.child(div().text_color(t.muted).child("No jobs on this page."));
      for (const job of jobs.items) content.child(action(`ocq-queue-job-${job.id}`, `${job.sequence} · ${job.kind} · ${job.state}\n${job.id}`, (cx) => this.read(owner, "detail", ["queue-job", job.id], cx)));
      if (jobs.has_more) content.child(action("ocq-queue-next", "Next 20 jobs", (cx) => this.read(owner, "jobs", ["queue-jobs", jobs.cursor], cx)));
      content.child(action("ocq-queue-first", "First page", (cx) => this.read(owner, "jobs", ["queue-jobs", "0"], cx)));
    }
    content.child(div().mt(8).font_semibold().child("Pending global notifications" )).child(status(this.events));
    if (events) {
      if (!events.items.length) content.child(div().text_color(t.muted).child("No pending events at this read."));
      for (const event of events.items) content.child(action(`ocq-queue-event-${event.id}`, `${event.sequence} · ${event.state} · ${event.outcome}\n${event.id}`, (cx) => this.read(owner, "detail", ["notification", event.id], cx)));
      if (events.has_more) content.child(div().text_color(t.muted).child("Showing first 20 pending events. Exact lookup below also finds acknowledged events."));
    }
    content.child(div().mt(8).font_semibold().child("Exact retained lookup"))
      .child(Input.new(this.input).id("ocq-queue-exact-id").accessibility_label("Exact retained job or event ID").h(34).px(8).border_1().border_color(t.border).text_color(t.foreground))
      .child(action("ocq-queue-lookup-job", "Read job ID", (cx) => this.read(owner, "detail", ["queue-job", this.input.value()], cx)))
      .child(action("ocq-queue-lookup-event", "Read event ID (including acknowledged)", (cx) => this.read(owner, "detail", ["notification", this.input.value()], cx)));
    } else {
      content.child(action("ocq-queue-back", "Back to queue", (cx) => { this.inspecting = false; this.detail = {}; this.log = {}; this.focus = 0; cx.notify(); }))
        .child(status(this.detail));
    }
    if (detail) {
      content.child(div().font_semibold().child(detail.job ? "Stored job detail" : "Frozen terminal event"));
      if (detail.job) content.child(div().text_size(12).text_color(t.muted).child(`${detail.job.id}\nRecorded session and worktree are not live availability proof.`));
      if (detail.job) this.handoff.render(content, owner, cx, t, action);
      if (detail.job) this.cancel.render(content, owner, cx, t, action);
      if (detail.job) this.resolution.render(content, owner, cx, t, action);
      if (detail.job) this.reconciliation.render(content, owner, cx, t, action);
      if (detail.job?.completion_event_id) content.child(action("ocq-queue-job-event", "Read retained global event / acknowledgement", (cx) => this.read(owner, "detail", ["notification", detail.job.completion_event_id], cx)));
      if (detail.event) content.child(action("ocq-queue-event-job", "Read event's job", (cx) => this.read(owner, "detail", ["queue-job", detail.event.job_id], cx)));
      if (detail.event) {
        const a = this.ack, state = a.status?.state;
        content.child(div().font_semibold().child("Global notification acknowledgement"));
        if (owner.machine !== null) content.child(div().text_color(t.muted).child("Acknowledgement is local-only and disabled on this selected machine. Stored reads remain available. Use that machine's local Herdr UI to acknowledge there."));
        else if (!a.scope) content.child(div().text_color(t.ansi1).child("Queue identity unavailable. Update/check the local bridge; acknowledgement is disabled."));
        else {
          const labels = { none: "Not acknowledged. Viewing does not acknowledge.", prepared: "Retained intent, not confirmed. Review before acknowledging.", armed: "Confirmed claim retained. A call may be delayed; check only, never resend.", uncertain: "No acknowledgement established. Outcome uncertain; check only, never resend.", observed: `Acknowledgement observed: ${a.status?.observation?.event?.acknowledged_at || "unknown"}. This is a stored observation, not proof of which caller caused it.`, blocked: "Journal blocked or belongs to another space. No action is permitted here; return to the original context and investigate." };
          content.child(div().text_size(12).text_color(t.muted).child(a.working ? "Checking retained acknowledgement…" : labels[state] || "Observation unavailable; acknowledgement is disabled."));
          if (a.error) content.child(div().text_color(t.ansi1).child(a.error));
          if (!a.working && state === "none" && !detail.event.acknowledged_at) content.child(action("ocq-global-ack-prepare", "Review global acknowledgement…", (cx) => this.ackAction(owner, "prepare", cx)));
          if (!a.working && state === "prepared") content.child(action("ocq-global-ack-review", "Review retained acknowledgement…", (cx) => this.ackAction(owner, "review", cx)));
          if (!a.working) content.child(action("ocq-global-ack-check", "Check exact event and journal (read-only)", (cx) => this.ackAction(owner, "status", cx)));
        }
      }
      if (detail.job) for (const stream of ["stdout", "stderr"]) {
        content.child(action(`ocq-queue-log-${stream}`, `Read ${stream} from byte 0`, (cx) => this.read(owner, "log", ["queue-log", detail.job.id, stream, "0"], cx)));
      }
      if (!log && !this.log.loading) content.child(div().child(JSON.stringify(detail.job || detail.event, null, 2)));
      else content.child(action("ocq-queue-show-summary", "Show stored summary", (cx) => { this.log = {}; cx.notify(); }));
    }
    content.child(status(this.log));
    if (log && this.log.bytes) {
      const bytes = this.log.bytes, start = this.bytePage * 1024;
      // Reversible byte rendering: never interpret terminal escapes or discard
      // invalid UTF-8. Backslashes and every non-printable/non-ASCII byte escape.
      const text = Array.from(bytes.slice(start, start + 1024), (b) => b >= 32 && b <= 126 && b !== 92 ? String.fromCharCode(b) : `\\x${b.toString(16).padStart(2, "0")}`).join("");
      content.child(div().font_semibold().child(`${log.stream} · byte observation`))
        .child(div().text_size(12).text_color(t.muted).child(`Chunk [${log.offset}, ${log.next_offset}); observed size ${log.observed_size}. EOF at read: ${log.eof}. Not completion.\nEscaped bytes · display page ${this.bytePage + 1}/${Math.max(1, Math.ceil(bytes.length / 1024))}`));
      if (this.bytePage > 0) content.child(action("ocq-log-display-prev", "Previous displayed bytes", (cx) => { this.bytePage--; cx.notify(); }));
      if (start + 1024 < bytes.length) content.child(action("ocq-log-display-next", "Next displayed bytes", (cx) => { this.bytePage++; cx.notify(); }));
      content.child(action("ocq-log-read-next", log.eof ? "Check for appended bytes" : "Read next chunk (up to 32 KiB)", (cx) => this.read(owner, "log", ["queue-log", log.job_id, log.stream, log.next_offset], cx)));
      content.child(div().child(text || "No bytes at this offset."));
    }
    return v_flex().id("ocq-queue").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx))
      .child(h_flex().flex_none().gap(8).child(div().font_semibold().child("OCQ · Queue"))
        .child(div().flex_1()).child(button("ocq-queue-projects", "Projects", (cx) => owner.leaveQueue(cx))))
      .child(div().flex_none().text_size(12).text_color(t.muted).child(`Machine: ${owner.machine ?? "this computer"}\nStored views · global ack is local-only\nUp/Down select, Enter opens · Esc back\nSelected: ${this.focusLabel || "Refresh"}`))
      .child(content);
  }
}
