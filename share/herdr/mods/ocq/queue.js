import { div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, TextareaState } from "gpui-base";
import { spaces } from "herdr";
import { exec, enabledButton } from "./compatibility.js";
import { Buffer } from "buffer";
import { QueuedCancellation } from "./queued-cancel.js";
import { CommandResolution } from "./command-resolution.js";
import { AgentReconciliation } from "./agent-reconcile.js";
import { AgentHandoff } from "./agent-handoff.js";
import { CommandSubmission } from "./command-submission.js";
import { AgentSubmission } from "./agent-submission.js";
import { CommandDispatcher } from "./command-dispatcher.js";
import { acceptObservationVersion, compareRevision, decimal } from "./stored-updates.js";

// Bridge timestamps are UTC RFC3339Nano. Compare all nine fractional digits,
// never Date's millisecond truncation or variable-width fraction strings.
function readOrder(value) {
  return (value || "").replace(/(?:\.(\d+))?Z$/, (_match, fraction) => `.${(fraction || "").padEnd(9, "0")}Z`);
}

// Both stored list DTOs have flat scalar rows. Own immutable copies so pending
// requests or a controller's retained response cannot change an observed page.
function frozenList(data) {
  return Object.freeze({ ...data, items: Object.freeze(data.items.map(item => Object.freeze({ ...item }))) });
}

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
    this.bytePage = 0; this.targets = [];
    this.detailTab = "summary"; this.listTab = "jobs"; this.diagnostics = false;
    this.executionDetails = false; this.jobActions = false;
  }

  current(owner, generation) {
    const active = spaces().find((s) => s.active);
    return !owner.closed && generation === this.generation && owner.queueMode && active && active.id === owner.spaceID && active.machine === owner.machine && active.folder === owner.folder;
  }

  read(owner, slot, args, cx, notify = true) {
    if (!this.current(owner, this.generation)) return;
    if ((slot === "jobs" || slot === "events") && this[slot].loading) return;
    const generation = this.generation, machine = owner.machine;
    const request = Object.freeze({ args: Object.freeze(args.slice()), generation, space: owner.spaceID, machine, folder: owner.folder, project: owner.selected });
    const previous = this[slot];
    if (slot === "log") {
      const same = this.log.cursor?.job_id === args[1] && this.log.cursor?.stream === args[2] && args[3] !== "0";
      this.log = same ? { ...this.log, bytes: null, data: null, needsRead: true, follow: false, capped: false } : { needsRead: true, follow: true, cursor: { job_id: args[1], stream: args[2], offset: "0", file_identity: "", observed_size: "0", modified_ns: "0" } };
      this.bytePage = 0; cx.notify(); return;
    }
    const state = { loading: true, pending: request, ...((slot === "jobs" || slot === "events") ? { data: previous.data, snapshot: previous.snapshot } : {}) };
    this[slot] = state;
    if (slot === "detail") { this.detailTab = "summary"; this.log = {}; this.ack = {}; this.cancel = new QueuedCancellation(this); this.resolution = new CommandResolution(this); this.reconciliation = new AgentReconciliation(this); this.handoff = new AgentHandoff(this); this.resolveInput.set_value(""); this.inspecting = true; owner.handle.focus(); }
    if (slot === "detail") state.initializeHelpers = true;
    if (notify) cx.notify();
  }

  refresh(owner, cx, notify = true) {
    owner.updates.readAgain(cx);
    if (notify) cx.notify();
  }

  observationSelectors(owner) {
    const descriptors = [], generation = this.generation;
    const add = (slot, state, selector, args) => {
      const expectedSnapshot = state.snapshot, pending = state.pending;
      descriptors.push({ selector, target: state, dirty: slot === "detail" && Boolean(state.dirty), apply: (result, meta, readAt, cx) => {
        if (!this.current(owner, generation) || this[slot] !== state || state.pending !== pending) return false;
        if (!acceptObservationVersion(state.observationVersion, meta)) return false;
        if (result.status !== "ready") {
          state.data = null; state.snapshot = null; state.loading = false; state.pending = null;
          state.observationVersion = meta; state.error = `${selector.kind}: ${result.status}`; return true;
        }
        const data = selector.kind === "queue-jobs" ? { ...result.data, queue_identity: meta.queue_identity }
          : selector.kind === "notifications" ? { items: result.data, has_more: result.has_more, cursor: result.cursor || args[1], read_at: readAt, queue_identity: meta.queue_identity }
            : { [selector.kind === "queue-job" ? "job" : "event"]: result.data, queue_identity: meta.queue_identity, read_at: readAt };
        if (slot !== "detail" && expectedSnapshot && !pending) return this.applyObservation(owner, generation, slot, expectedSnapshot, data, cx, meta);
        if (slot === "detail" && state.data?.job && !pending) return this.applyDetailObservation(owner, generation, state, data, cx, meta);
        if (slot === "detail" && state.data?.event && !pending && JSON.stringify(state.data.event) !== JSON.stringify(data.event)) this.blockDetailActions(readAt, meta);
        state.data = slot === "detail" ? data : frozenList(data);
        state.snapshot = Object.freeze({ ...(pending || { args: Object.freeze(args), generation, space: owner.spaceID, machine: owner.machine, folder: owner.folder }), cursor: data.cursor, data: state.data });
        state.observationVersion = meta; state.loading = false; state.pending = null; state.error = null; state.dirty = false;
        if (slot === "detail") state.observedReadAt = readAt;
        if (slot === "jobs") this.observeSelectedJob(data, cx, meta);
        // Journal/status helpers load ONLY for an explicit user inspection, not
        // in response to revisions, reconnect, wait expiry or automatic updates.
        if (slot === "detail" && state.initializeHelpers) {
          state.initializeHelpers = false;
          if (data.event) this.loadAck(owner, cx);
          if (data.job) { this.cancel.load(owner, cx); this.resolution.load(owner, cx); this.reconciliation.load(owner, cx); this.handoff.load(owner, cx); }
        }
        return true;
      } });
    };
    if (!this.inspecting) {
      const slot = this.listTab === "events" ? "events" : "jobs", state = this[slot];
      const args = state.pending?.args || state.snapshot?.args || [slot === "jobs" ? "queue-jobs" : "notifications", "0"];
      add(slot, state, { slot: `queue-${slot}`, kind: slot === "jobs" ? "queue-jobs" : "notifications", cursor: args[1] || "0" }, args);
    } else {
      const state = this.detail, args = state.pending?.args || state.snapshot?.args;
      if (args) add("detail", state, { slot: "queue-selected", kind: args[0] === "queue-job" ? "queue-job" : "notification", ...(args[0] === "queue-job" ? { job_id: args[1] } : { event_id: args[1] }) }, args);
    }
    return descriptors;
  }

  resetObservationVersions() {
    this.blockDetailActions(undefined, null, "Observation context changed; retained private evidence has no action authority.");
    this.retainedPrivate = this.retainedPrivate || [];
    const previous = this.retainedPrivate[this.retainedPrivate.length - 1];
    if (!previous || previous.ack !== this.ack || previous.cancel !== this.cancel || previous.submission !== this.submission || previous.agentSubmission !== this.agentSubmission || previous.dispatcher !== this.dispatcher) this.retainedPrivate.push({ ack: this.ack, cancel: this.cancel, resolution: this.resolution, reconciliation: this.reconciliation, handoff: this.handoff, submission: this.submission, agentSubmission: this.agentSubmission, dispatcher: this.dispatcher });
    this.generation++;
    for (const slot of ["jobs", "events", "detail"]) {
      // Replace targets: a late callback cannot repopulate a new incarnation.
      this[slot] = slot === "detail" ? { actionsBlocked: true, actionBlockReason: "Context changed; inspect afresh. Private journals retained separately." } : {};
    }
    this.log = {}; this.bytePage = 0; this.inspecting = false;
  }

  observationLog(owner) {
    if (!owner.queueMode || !this.inspecting || this.detailTab !== "logs" || !this.log.cursor || this.log.reset || this.log.capped) return null;
    return { target: this.log, cursor: this.log.cursor, needsRead: Boolean(this.log.needsRead), watch: Boolean(this.log.follow) };
  }
  observationLogHint(capture, output, reset, cx) {
    if (!capture || this.log !== capture.target) return;
    if (reset || output?.status === "reset") { this.log.reset = output?.reset_reason || "Log identity changed"; this.log.follow = false; }
    else if (this.log.follow) this.log.needsRead = true;
    cx.notify();
  }
  applyLogObservation(capture, output, cx) {
    if (this.log !== capture.target || this.log.cursor !== capture.cursor) return false;
    const log = this.log;
    if (output.non_atomic_with_database !== true || !output.observed_at) throw new Error("Invalid separate log observation");
    log.observedAt = output.observed_at; log.needsRead = false;
    if (output.status === "reset") { log.reset = output.reset_reason || "Log replaced/truncated"; log.follow = false; cx.notify(); return true; }
    if (output.status !== "ready") { log.error = `Selected output: ${output.status}`; cx.notify(); return true; }
    const bytes = Buffer.from(output.bytes_base64 || "", "base64");
    if (bytes.length > 32768 || bytes.toString("base64") !== (output.bytes_base64 || "") || !decimal(output.next_offset) || output.offset !== capture.cursor.offset || output.cursor?.job_id !== capture.cursor.job_id || output.cursor?.stream !== capture.cursor.stream || output.cursor.offset !== output.next_offset || !/^[0-9a-f]{64}$/.test(output.cursor.file_identity || "") || !decimal(output.cursor.observed_size) || !decimal(output.cursor.modified_ns)) throw new Error("Invalid bounded log bytes/cursor");
    const prior = log.bytes || Buffer.from([]);
    if (prior.length + bytes.length > 65536) { log.capped = true; log.follow = false; cx.notify(); return true; }
    const following = log.follow;
    log.bytes = Buffer.concat([prior, bytes]); log.cursor = Object.freeze(output.cursor); log.error = null;
    log.data = { job_id: output.cursor.job_id, stream: output.cursor.stream, offset: log.data?.offset || output.offset, next_offset: output.next_offset, observed_size: output.cursor.observed_size, eof: output.eof, read_at: output.observed_at };
    if (following) this.bytePage = Math.max(0, Math.ceil(log.bytes.length / 1024) - 1);
    if (!output.eof && following) log.needsRead = true;
    if (log.bytes.length >= 65536) { log.capped = true; log.follow = false; log.needsRead = false; }
    cx.notify(); return true;
  }

  ackScope(owner) {
    const data = this.detail.data, event = data?.event;
    if (!this.current(owner, this.generation) || owner.machine !== null || !event ||
      !/^[0-9a-f]{64}$/.test(data.queue_identity || "") || typeof event.sequence !== "string" || !/^[1-9][0-9]*$/.test(event.sequence)) return null;
    return { machine: "local", space_id: owner.spaceID, queue_identity: data.queue_identity, event_id: event.id, job_id: event.job_id, sequence: event.sequence };
  }

  // Capture the exact immutable snapshot before a controller read. Binding to
  // that object also rejects an old A-page response after navigating A→B→A.
  // Equal timestamps (duplicates or conflicts) and older observations do not
  // replace data. The controller must provide a strictly newer stored read.
  applyObservation(owner, generation, slot, expectedSnapshot, data, cx, meta = null) {
    if (!this.current(owner, generation) || !["jobs", "events"].includes(slot) || !Array.isArray(data?.items) || !data.read_at) return false;
    const state = this[slot];
    if (!state.snapshot || state.snapshot !== expectedSnapshot || typeof data.has_more !== "boolean" || (slot === "jobs" && typeof data.cursor !== "string")) return false;
    if (state.loading || (state.data?.queue_identity && state.data.queue_identity !== data.queue_identity)) return false;
    if (meta ? !acceptObservationVersion(state.observationVersion, meta) : readOrder(state.data?.read_at) >= readOrder(data.read_at)) return false;
    data = frozenList(data);
    state.snapshot = Object.freeze({ ...state.snapshot, cursor: data.cursor, data });
    state.data = data; state.error = null; if (meta) state.observationVersion = meta;
    if (slot === "jobs") this.observeSelectedJob(data, cx, meta);
    cx.notify(); return true;
  }

  // A list can invalidate a detail, but is not a detail snapshot. Do not merge
  // partial rows into the retained full job or claim a new detail read time.
  observeSelectedJob(page, cx, meta = null) {
    const detail = this.detail, job = detail.data?.job;
    if (!job || (meta ? (detail.observationVersion && compareRevision(meta.revision, detail.observationVersion.revision) < 0) : readOrder(page.read_at) <= readOrder(detail.observedReadAt))) return;
    // Existing queue-jobs pages have no queue identity. They may conservatively
    // invalidate a matching receipt, never establish a full detail's freshness.
    if (page.queue_identity && page.queue_identity !== detail.data.queue_identity) {
      this.blockDetailActions(page.read_at, meta); detail.dirty = true; cx.notify(); return;
    }
    const observed = page.items.find(item => item.id === job.id && item.sequence === job.sequence);
    if (!observed) return;
    if (["kind", "state", "outcome", "reason", "validation", "recorded_session_id", "recorded_worktree", "completion_event_id"].some(key => JSON.stringify(observed[key]) !== JSON.stringify(job[key]))) {
      this.blockDetailActions(page.read_at, meta);
      detail.dirty = true;
      cx.notify();
    }
  }

  blockDetailActions(readAt, meta = null, reason = "Stored observations differ; a new review is required.") {
    const detail = this.detail;
    detail.actionBlockReason = reason;
    if (!detail.actionsBlocked) {
      detail.actionsBlocked = true;
      // Tighten each existing original guard; retain the helper, status, private
      // review and armed/uncertain intent. An already-launched call may finish.
      for (const helper of [this.cancel, this.resolution, this.reconciliation]) {
        const original = helper.boundCurrent;
        if (original) helper.boundCurrent = () => !detail.actionsBlocked && original();
      }
      const acknowledgementCurrent = this.ack.current;
      if (acknowledgementCurrent) this.ack.current = () => !detail.actionsBlocked && acknowledgementCurrent();
    }
    if (readOrder(readAt) > readOrder(detail.changedReadAt)) detail.changedReadAt = readAt;
    if (meta && (!detail.changedVersion || compareRevision(meta.revision, detail.changedVersion.revision) >= 0)) detail.changedVersion = meta;
  }

  detailControlAllowed(id) {
    if (!this.detail.actionsBlocked) return true;
    if (!["ocq-cancel-", "ocq-resolve-", "ocq-agent-reconcile-", "ocq-global-ack-"].some(prefix => id.startsWith(prefix))) return true;
    return ["-check", "-back", "-close-private"].some(suffix => id.endsWith(suffix));
  }

  // Controller captures this exact state object before issuing queue-job. A
  // reselection, context/generation/queue/receipt change or older/equal read is
  // refused. Observation never revives old confirmations: the action latch stays
  // blocked until an explicit new inspection loads the journal in a fresh scope.
  applyDetailObservation(owner, generation, expectedDetail, data, cx, meta = null) {
    const detail = this.detail, before = detail.data, job = data?.job;
    if (!this.current(owner, generation) || detail !== expectedDetail || detail.loading || !before?.job || !job || !data.read_at) return false;
    if (!/^[0-9a-f]{64}$/.test(data.queue_identity || "") || before.queue_identity !== data.queue_identity || before.job.id !== job.id || before.job.sequence !== job.sequence) return false;
    if (meta ? (!acceptObservationVersion(detail.observationVersion, meta) || (detail.changedVersion && compareRevision(meta.revision, detail.changedVersion.revision) < 0)) : (readOrder(data.read_at) <= readOrder(detail.observedReadAt) || readOrder(data.read_at) < readOrder(detail.changedReadAt))) return false;
    if (JSON.stringify(before.job) !== JSON.stringify(job)) this.blockDetailActions(data.read_at, meta);
    detail.data = data; detail.snapshot = Object.freeze({ ...detail.snapshot, data });
    detail.observedReadAt = data.read_at; detail.dirty = false; detail.error = null; if (meta) detail.observationVersion = meta;
    cx.notify(); return true;
  }

  // These journal DTOs have no job-observation read_at. Receipt recorded_at is
  // operation evidence, not a timestamp for their current job snapshot. Keep all
  // helper job snapshots outside the selected projection, regardless of arrival
  // order. expectedDetail is the helper's object captured at load, before await.
  noteHelperObservation(owner, generation, expectedDetail, observation, cx) {
    const detail = this.detail, before = detail.data, job = observation?.job;
    if (!this.current(owner, generation) || detail !== expectedDetail || !before?.job || !job) return false;
    if (observation.queue_identity !== before.queue_identity || job.id !== before.job.id || job.sequence !== before.job.sequence) return false;
    detail.dirty = true;
    if (["kind", "state", "outcome", "reason", "validation", "recorded_session_id", "recorded_worktree", "completion_event_id"].some(key => JSON.stringify(job[key]) !== JSON.stringify(before.job[key]))) {
      // There is no comparable time: latch safely, but never advance either
      // the selected projection's observedReadAt or the ordered change watermark.
      this.blockDetailActions();
    }
    cx.notify(); return true;
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
        if (status.observation) a.detail.dirty = true; // Receipt evidence is not a revision-bound event projection.
        if (status.state === "observed" && this.events.data?.items.some((event) => event.id === a.scope.event_id)) this.read(owner, "events", this.events.snapshot?.args || ["notifications", "0"], cx);
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
            if (a.status.observation) a.detail.dirty = true;
            if (a.status.state === "observed") { a.error = null; this.read(owner, "events", this.events.snapshot?.args || ["notifications", "0"], cx); }
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
      .when(Boolean(this.detail.actionsBlocked), e => e.child(div().text_color(t.blocked).child("Stored observations changed. This retained confirmation is unavailable; go back and check the exact journal.")))
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
    if (this.dispatcher.screen && this.dispatcher.current(owner)) { this.dispatcher.key(event, owner, cx); return; }
    if (this.submission.screen && this.submission.current(owner)) { this.submission.key(event, owner, cx); return; }
    if (this.agentSubmission.screen && this.agentSubmission.current(owner)) { this.agentSubmission.key(event, owner, cx); return; }
    const key = event.key;
    if (event.modifiers.control || event.modifiers.alt || event.modifiers.platform) return;
    if (this.inputFocused) { if (key === "escape") { owner.handle.focus(); cx.stop_propagation(); } return; }
    if (this.reconciliation.confirm && this.reconciliation.current(owner)) {
      if (!this.reconciliation.working && key === "escape") { this.reconciliation.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "o" && this.detailControlAllowed("ocq-agent-reconcile-once")) this.reconciliation.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.resolution.confirm && this.resolution.current(owner)) {
      if (!this.resolution.working && key === "escape") { this.resolution.confirm = false; this.resolution.preview = null; cx.notify(); }
      else if (!event.is_held && key === "r" && this.detailControlAllowed("ocq-resolve-once")) this.resolution.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.ack.confirm && this.ackCurrent(owner, this.ack)) {
      if (!this.ack.working && key === "escape") { this.ack.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "a" && this.detailControlAllowed("ocq-global-ack-confirm-once")) this.confirmAck(owner, cx);
      cx.stop_propagation(); return;
    }
    if (this.cancel.confirm && this.cancel.current(owner)) {
      if (!this.cancel.working && key === "escape") { this.cancel.confirm = false; cx.notify(); }
      else if (!event.is_held && key === "c" && this.detailControlAllowed("ocq-cancel-once")) this.cancel.send(owner, cx);
      cx.stop_propagation(); return;
    }
    if (key === "escape") {
      if (this.inspecting) { this.inspecting = false; this.detail = {}; this.log = {}; }
      else owner.leaveQueue(cx);
      cx.notify();
    }
    const targets = [...(owner.headerTargets || []), ...(owner.compatibilityTargets || []), ...this.targets];
    let index = targets.findIndex(target => target.id === owner.navigationTarget);
    if (index < 0) index = 0;
    if ((key === "down" || key === "up") && targets.length) {
      index = (index + (key === "down" ? 1 : targets.length - 1)) % targets.length;
      owner.navigationTarget = targets[index].id; owner.navigationLabel = targets[index].label;
      cx.notify();
    }
    if (key === "enter" && !event.is_held) targets[index]?.run(cx);
    if (["escape", "up", "down", "enter"].includes(key)) cx.stop_propagation();
  }

  render(owner, cx, t, button) {
    if (this.dispatcher.screen && this.dispatcher.current(owner)) return this.dispatcher.render(owner, cx, t, button);
    if (this.submission.screen && this.submission.current(owner)) return this.submission.render(owner, cx, t, button);
    if (this.agentSubmission.screen && this.agentSubmission.current(owner)) return this.agentSubmission.render(owner, cx, t, button);
    const retainedReview = this.reconciliation.confirm && this.reconciliation.current(owner) ? this.reconciliation
      : this.resolution.confirm && this.resolution.current(owner) ? this.resolution
        : this.cancel.confirm && this.cancel.current(owner) ? this.cancel : null;
    if (retainedReview) return v_flex().size_full().min_h(0)
      .when(Boolean(this.detail.actionsBlocked), e => e.child(div().flex_none().p(10).text_color(t.blocked).child("Action scope unavailable. Retained intent/review is unchanged; go back and check the exact journal, never resend.")))
      .child(div().flex_1().min_h(0).child(retainedReview.confirmation(owner, cx, t, button)));
    if (this.ack.confirm && this.ackCurrent(owner, this.ack)) return this.renderAckConfirmation(owner, cx, t, button);
    this.targets = [];
    const action = (id, label, fn) => {
      const run = cx => { if (enabledButton(id) && this.detailControlAllowed(id)) fn(cx); };
      if (enabledButton(id) && this.detailControlAllowed(id)) owner.registerAction(id, label, run, this.targets);
      return button(id, label, cx => { owner.navigationTarget = id; owner.navigationLabel = label; run(cx); })
        .when(id === owner.navigationTarget, (e) => e.bg(t.hover));
    };
    const status = (state, slot) => v_flex().gap(4)
      .when(Boolean(state.loading), (e) => e.child(div().child("Reading stored data…")))
      .when(Boolean(state.error), (e) => e
        .child(div().text_color(t.ansi1).child(`Cannot read this stored view on ${owner.machine ?? "this computer"}. Check the selected machine and bridge.${state.data ? " Displayed data may be stale." : " No snapshot available."}`))
        .child(action(`ocq-queue-job-diagnostics-${slot}`, `${this.diagnostics ? "Hide diagnostics" : "Diagnostics"} · ${slot}`, cx => { this.diagnostics = !this.diagnostics; cx.notify(); }))
        .when(this.diagnostics, e => e.child(div().child(state.error)).child(action(`ocq-queue-job-diagnostics-copy-${slot}`, `Copy diagnostics · ${slot}`, cx => cx.write_to_clipboard(state.error)))))
      .when(Boolean(state.data?.read_at), (e) => e.child(div().text_size(12).text_color(t.muted).child(`${slot === "detail" ? "Detail read started" : "Read started"}: ${state.observedReadAt || state.data.read_at}`)));
    const jobs = this.jobs.data, events = this.events.data, detail = this.detail.data, log = this.log.data;
    // Keep natural content height in the root scroll viewport, like the native
    // forms; nested flex scrolling can strand command output below the pane.
    const content = v_flex().id(this.inspecting ? "ocq-queue-inspection" : "ocq-queue-content").flex_none().gap(8);
    if (!this.inspecting) {
      content.child(div().text_size(12).text_color(t.muted).child("Stored reads only. Opening never acknowledges events or changes jobs."))
      .when(owner.machine === null, e => e.child(action("ocq-queue-job-actions", this.executionDetails ? "Hide local execution actions" : "Local execution actions…", cx => { this.executionDetails = !this.executionDetails; cx.notify(); })))
      .when(owner.machine === null && this.executionDetails, e => e
        .child(action("ocq-queue-submit-command", "Submit general command / recover admission…", (cx) => this.submission.open(owner, cx)))
        .child(action("ocq-queue-submit-agent", "Submit standalone agent / recover admission…", (cx) => this.agentSubmission.open(owner, cx)))
        .child(action("ocq-queue-command-dispatcher", "COMMANDS-ONLY dispatcher · start / status / drain…", (cx) => this.dispatcher.open(owner, cx))))
      .when(owner.machine !== null, e => e.child(div().text_size(12).text_color(t.muted).child("Execution and acknowledgement actions require this machine’s local UI.")))
      .child(h_flex().gap(4)
        .child(action("ocq-queue-job-tab-jobs", this.listTab === "jobs" ? "Selected: Jobs" : "Jobs", cx => { this.listTab = "jobs"; cx.notify(); }))
        .child(action("ocq-queue-job-tab-notifications", this.listTab === "events" ? "Selected: Notifications" : "Notifications", cx => { this.listTab = "events"; cx.notify(); })));
    if (this.listTab === "jobs") {
      content.child(div().font_semibold().child("Queue jobs · receipt order"))
      .child(status(this.jobs, "jobs"));
    if (jobs) {
      if (!jobs.items.length) content.child(div().text_color(t.muted).child(this.jobs.snapshot?.args[1] === "0" ? "No jobs in this queue at this read." : "No jobs on this page."));
      for (const job of jobs.items) content.child(action(`ocq-queue-job-${job.id}`, `${job.sequence} · ${job.kind} · ${job.state}\n${job.id}`, (cx) => this.read(owner, "detail", ["queue-job", job.id], cx)));
      if (jobs.has_more) content.child(action("ocq-queue-next", "Next 20 jobs", (cx) => this.read(owner, "jobs", ["queue-jobs", jobs.cursor], cx)));
      content.child(action("ocq-queue-first", "First page", (cx) => this.read(owner, "jobs", ["queue-jobs", "0"], cx)));
    }
    }
    if (this.listTab === "events") {
    content.child(div().mt(8).font_semibold().child(`Global notifications · pending page after ${this.events.snapshot?.args[1] || "0"} (not project mail)` )).child(status(this.events, "notifications"));
    if (events) {
      if (!events.items.length) content.child(div().text_color(t.muted).child("No pending events at this read."));
      for (const event of events.items) content.child(action(`ocq-queue-event-${event.id}`, `${event.sequence} · ${event.state} · ${event.outcome}\n${event.id}`, (cx) => this.read(owner, "detail", ["notification", event.id], cx)));
      if (events.has_more) content.child(action("ocq-queue-event-next", "Next 20 pending notifications", cx => this.read(owner, "events", ["notifications", events.cursor], cx)));
      content.child(action("ocq-queue-event-first", "First pending notification page", cx => this.read(owner, "events", ["notifications", "0"], cx)));
    }
    }
    content.child(div().mt(8).font_semibold().child("Exact retained lookup"))
      .child(Input.new(this.input).id("ocq-queue-exact-id").accessibility_label("Exact retained job or event ID").h(34).px(8).border_1().border_color(t.border).text_color(t.foreground))
      .child(action("ocq-queue-lookup-job", "Read job ID", (cx) => this.read(owner, "detail", ["queue-job", this.input.value()], cx)))
      .child(action("ocq-queue-lookup-event", "Read event ID (including acknowledged)", (cx) => this.read(owner, "detail", ["notification", this.input.value()], cx)));
    } else {
      content.child(action("ocq-queue-back", "Back to queue", (cx) => { this.inspecting = false; this.detail = {}; this.log = {}; cx.notify(); }))
        .child(status(this.detail, "detail"));
    }
    if (detail) {
      content.child(div().font_semibold().child(detail.job ? "Stored job detail" : "Frozen terminal event"));
      if (detail.job && this.detailTab !== "summary") content.child(div().text_size(12).text_color(t.muted).child(`${detail.job.kind} · ${detail.job.state}\n${detail.job.id}`));
      if (this.detail.actionsBlocked) content.child(div().text_size(12).text_color(t.blocked).child(this.detailTab === "logs" ? "Action scope paused · log observation remains read-only." : `${this.detail.actionBlockReason || "Action scope paused."} Retained intents unchanged.`));
      if (this.detail.dirty) content.child(div().text_color(t.muted).child("Selected detail needs a newer stored read; the displayed summary is from its own timestamp above."));
      content.child(h_flex().gap(4).children(["summary", ...(detail.job ? ["logs"] : []), "fields"].map(tab => action(`ocq-queue-job-tab-${tab}`, `${this.detailTab === tab ? "Selected: " : ""}${tab === "fields" ? "All fields" : tab[0].toUpperCase() + tab.slice(1)}`, cx => { this.detailTab = tab; cx.notify(); }))));
      if (this.detailTab === "summary") {
      const item = detail.job || detail.event;
      content.child(div().child(`${item.kind || "Completion event"}${item.role ? ` / ${item.role}` : ""} · ${item.state}\nOutcome: ${item.outcome || "not recorded"}\nValidation: ${typeof item.validation === "object" ? item.validation?.status || "not recorded" : item.validation || "not recorded"}\nID: ${item.id}\nLast reason: ${item.reason || "not recorded"}`));
      if (detail.job) content.child(div().text_size(12).text_color(t.muted).child(`${detail.job.id}\nRecorded session and worktree are not live availability proof.`));
      if (detail.job && owner.machine === null) content.child(action("ocq-queue-job-journals", this.jobActions ? "Hide job actions and journals" : "Job actions and journal checks…", cx => { this.jobActions = !this.jobActions; cx.notify(); }));
      if (detail.job && owner.machine === null && this.jobActions) this.handoff.render(content, owner, cx, t, action);
      if (detail.job && owner.machine === null && this.jobActions) this.cancel.render(content, owner, cx, t, action);
      if (detail.job && owner.machine === null && this.jobActions) this.resolution.render(content, owner, cx, t, action);
      if (detail.job && owner.machine === null && this.jobActions) this.reconciliation.render(content, owner, cx, t, action);
      if (owner.machine !== null) content.child(div().text_color(t.muted).child("Execution and acknowledgement actions require this machine’s local UI."));
      if (detail.job?.completion_event_id) content.child(action("ocq-queue-job-event", "Read retained global event / acknowledgement", (cx) => this.read(owner, "detail", ["notification", detail.job.completion_event_id], cx)));
      if (detail.event) content.child(action("ocq-queue-event-job", "Read event's job", (cx) => this.read(owner, "detail", ["queue-job", detail.event.job_id], cx)));
      if (detail.event && owner.machine === null) {
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
      }
      if (detail.job && this.detailTab === "logs") for (const stream of ["stdout", "stderr"]) {
        content.child(action(`ocq-log-stream-${stream}`, `Read ${stream} from byte 0`, (cx) => this.read(owner, "log", ["queue-log", detail.job.id, stream, "0"], cx)));
      }
      if (this.detailTab === "fields") content.child(div().child(JSON.stringify(detail.job || detail.event, null, 2)));
    }
    if (this.detailTab === "logs") {
    content.child(status(this.log, "logs"));
    if (this.log.reset) content.child(div().text_color(t.blocked).child(`Selected output reset: ${this.log.reset}. No reset cursor or bytes were adopted.`))
      .child(action("ocq-log-reset-explicit", "Discard displayed stream and read from byte 0", cx => this.read(owner, "log", ["queue-log", this.log.cursor.job_id, this.log.cursor.stream, "0"], cx)));
    if (this.log.capped) content.child(div().text_color(t.muted).child("Automatic output follow paused at the 64 KiB display cap. Read the next bounded chunk explicitly."));
    if (log && this.log.bytes) {
      const bytes = this.log.bytes, start = this.bytePage * 1024;
      // Reversible byte rendering: never interpret terminal escapes or discard
      // invalid UTF-8. Backslashes and every non-printable/non-ASCII byte escape.
      const text = Array.from(bytes.slice(start, start + 1024), (b) => b >= 32 && b <= 126 && b !== 92 ? String.fromCharCode(b) : `\\x${b.toString(16).padStart(2, "0")}`).join("");
      content.child(div().font_semibold().child(`${log.stream} · byte observation`))
        .child(div().text_size(12).text_color(t.muted).child(`Chunk [${log.offset}, ${log.next_offset}); observed size ${log.observed_size}. EOF at read: ${log.eof}. Not completion.\nEscaped bytes · display page ${this.bytePage + 1}/${Math.max(1, Math.ceil(bytes.length / 1024))}`));
      if (this.bytePage > 0) content.child(action("ocq-log-display-prev", "Previous displayed bytes · pause follow", (cx) => { this.log.follow = false; this.bytePage--; cx.notify(); }));
      if (start + 1024 < bytes.length) content.child(action("ocq-log-display-next", "Next displayed bytes", (cx) => { this.log.follow = false; this.bytePage++; cx.notify(); }));
      if (!this.log.reset && !this.log.capped) content.child(action("ocq-log-follow", this.log.follow ? "Pause automatic output follow" : "Follow latest displayed bytes", cx => { this.log.follow = !this.log.follow; if (this.log.follow) { this.bytePage = Math.max(0, Math.ceil(bytes.length / 1024) - 1); this.log.needsRead = true; } cx.notify(); }));
      if (!this.log.reset) content.child(action("ocq-log-read-next", "Read next bounded chunk explicitly", (cx) => this.read(owner, "log", ["queue-log", log.job_id, log.stream, log.next_offset], cx)));
      content.child(div().child(text || "No bytes at this offset."));
    }
    }
    return v_flex().id("ocq-queue").size_full().min_h(0).overflow_y_scroll().p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => this.key(e, owner, cx))
      .child(div().flex_none().text_size(12).text_color(t.muted).child(`Up/Down select, Enter opens · Esc back\nSelected: ${owner.navigationLabel || "Workspace navigation"}`))
      .child(content);
  }
}
