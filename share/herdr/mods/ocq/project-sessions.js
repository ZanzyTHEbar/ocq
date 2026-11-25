import { decimal } from "./observation-version.js";

const hex = s => typeof s === "string" && /^[a-f0-9]{64}$/.test(s);
const worker = item => ["implementation", "review"].includes(item.kind);
const validSession = s => typeof s === "string" && /^ses_[A-Za-z0-9_-]{1,128}$/.test(s);

// Read-only join of the existing journals. An exact Work request, admitted job
// and session must agree. A label is never used to locate or adopt a session.
export class ProjectSessions {
  constructor(host) { this.host = host; this.rows = []; this.generation = 0; }
  scope() {
    const h = this.host, b = h.updates.binding, space = h.localSpace();
    if (!h.selected || !hex(b?.queue_identity) || !/^sp[0-9a-f]{32}$/.test(space || "")) return null;
    return { project: h.selected, machine: h.machine, space, folder: h.folder, queue: b.queue_identity,
      source: h.sourceKey, store: b.store_context, bridge: b.bridge_context, epoch: h.panelEpoch, owner: h.generation };
  }
  current(scope) {
    return !!scope && this.host.panelCurrent() && this.host.source.online() && JSON.stringify(scope) === JSON.stringify(this.scope());
  }
  reset() {
    this.generation++;
    this.rows = []; this.bound = null; this.readAt = null;
    this.host.mainClient.sync(null);
  }
  observation() {
    const scope = this.scope();
    if (!scope) return null;
    this.bound = scope;
    return { scope, generation: this.generation };
  }
  apply(capture, value, cx, main, clients) {
    if (!capture || capture.generation !== this.generation || !this.current(capture.scope)) return;
    const project = capture.scope.project;
    if (value.protocol !== "ocq-work-v1" || value.project_id !== project ||
        value.journal?.project_id !== project || value.journal.consistency !== "filesystem_non_atomic" ||
        !hex(value.journal.fingerprint) || typeof value.journal.read_at !== "string" ||
        !Array.isArray(value.items) || value.items.length > 128) throw Error("Invalid project session observation");
    const items = new Map(value.items.map(item => [item.request_id, item]));
    if (items.size !== value.items.length) throw Error("Duplicate work pointers");
    this.host.mainClient.apply(capture.scope, main, clients, cx);
    const rows = new Map(), conflicts = new Set();
    for (const goal of this.host.mainClient.goals) {
      for (const linked of goal.work) {
        const item = items.get(linked.request_id), job = item?.job, session = job?.session_id;
        if (!item || !worker(item) || !validSession(session) || item.project_id !== project ||
            item.intent_sha256 !== linked.intent_sha256 || linked.job?.job_id !== job.job_id ||
            linked.job.session_id !== session || !decimal(job.sequence) ||
            !item.receipt || item.receipt.project_id !== project || item.receipt.job_id !== job.job_id ||
            typeof job.terminal_proven !== "boolean" || conflicts.has(session)) continue;
        if (job.session_state === "deleted") continue;
        if (job.session_state !== undefined && job.session_state !== "uncertain") throw Error("Invalid session deletion state");
        const prior = rows.get(session);
        if (prior?.request_id === item.request_id && prior.job.job_id === job.job_id) continue;
        if (prior || session === this.host.mainClient.selectedMain?.session_id) {
          rows.delete(session); conflicts.add(session); continue;
        }
        rows.set(session, { ...item, goalTitle: goal.title || "Project goal",
          title: `${item.kind === "review" ? "Review" : "Implementation"} #${job.sequence}` });
      }
    }
    for (const item of items.values()) {
      const task = item.task_goal, job = item.job;
      if (!task) continue;
      if (item.project_id !== project || item.kind !== "implementation" || task.intent_sha256 !== item.intent_sha256 ||
          !hex(task.goal_sha256) || !/^[a-f0-9]{40}$/.test(task.base_commit || "") ||
          typeof task.title !== "string" || task.title.length > 144) throw Error("Invalid standalone task identity");
      if (!job || !item.receipt || item.receipt.project_id !== project || item.receipt.job_id !== job.job_id ||
          !decimal(job.sequence) || job.session_state === "deleted") continue;
      const lifecycle = item.task_lifecycle;
      if (lifecycle && (!/^lane-[A-Za-z0-9_-]{1,123}$/.test(lifecycle.lane_id || "") ||
          lifecycle.work_finished !== ["finished", "cancelled", "resolved"].includes(job.state) ||
          lifecycle.main_handled !== "unverified" || lifecycle.session_archived !== "unverified"))
        throw Error("Invalid standalone task lifecycle");
      const session = job.session_id;
      if (session && (!validSession(session) || conflicts.has(session))) continue;
      const key = session || item.request_id;
      if (rows.has(key) || session && session === this.host.mainClient.selectedMain?.session_id) {
        rows.delete(key); conflicts.add(key); continue;
      }
      rows.set(key, { ...item, title: task.title || "Task goal", goalTitle: `Task #${job.sequence}` });
    }
    this.rows = [...rows.values()].sort((a, b) => a.job.sequence.length === b.job.sequence.length ?
      (a.job.sequence < b.job.sequence ? 1 : a.job.sequence > b.job.sequence ? -1 : 0) : b.job.sequence.length - a.job.sequence.length);
    this.readAt = value.journal.read_at;
  }
}
