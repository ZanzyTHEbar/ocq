import { exec, focused, panes, run, spaces, terminals } from "herdr";
import { decimal } from "./observation-version.js";

const hex = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9_-]{1,128}$/.test(value);
const object = value => value && typeof value === "object" && !Array.isArray(value);
const clientID = value => typeof value === "string" && /^[0-9a-f]{32}$/.test(value);

function openingSpace(scope) {
  return scope?.machine === null && spaces().find(s => s.active && s.id === scope.space && s.machine === null);
}

function existingTerminals(space) {
  const tabs = new Map(space.tabs.map(t => [t.id, t]));
  const visible = panes().filter(p => tabs.has(p.tab) && p.terminal && p.pane);
  const live = terminals();
  return visible.flatMap(p => {
    if (!/^t[1-9][0-9bcdfghjkmnpqrstvwxyz]{0,30}$/.test(p.terminal) ||
        visible.filter(v => v.terminal === p.terminal).length !== 1) return [];
    const matches = live.filter(t => t.address === p.terminal && t.state === "live");
    const front = matches[0]?.foreground;
    if (matches.length !== 1 || front?.name !== "opencode" || !Number.isSafeInteger(front.pid) || front.pid < 2 ||
        typeof front.cwd !== "string" || !front.cwd.startsWith("/")) return [];
    return [{ tab: p.tab, pane: p.pane, terminal: p.terminal, pid: front.pid, cwd: front.cwd,
      label: `${tabs.get(p.tab).name || p.tab} · ${p.title || p.terminal} (${p.terminal})` }];
  });
}

function sameTerminal(a, b) {
  return a.tab === b.tab && a.pane === b.pane && a.terminal === b.terminal && a.pid === b.pid && a.cwd === b.cwd;
}

async function contact(mode, viewer, target) {
  try {
    const result = await exec("ocq-herdr", ["tui-open", "--", mode, String(viewer.pid), viewer.terminal,
      viewer.cwd, viewer.client || "-", target], { timeout: 6500 });
    const r = result.code === 0 ? JSON.parse(result.stdout) : null;
    if (r?.terminal === viewer.terminal && clientID(r.client) &&
        r.session === JSON.parse(target).session && (mode === "check" && r.outcome === "ready" ||
          mode === "open" && r.outcome === "routed" && r.client === viewer.client)) return r.client;
  } catch (_) { /* unsupported, moved, or no exact receipt; never retry */ }
}

function mainStatus(value, project) {
  if (!object(value) || value.protocol !== "ocq-main-v1" || value.project_id !== project || !id(value.operation_id) ||
    !["create", "adopt", "replace", "refresh", "role-refresh", "send", "reopen"].includes(value.mode) ||
    !sessionID(value.session_id) || !id(value.setup_id) || !hex(value.intent_sha256) || !decimal(value.binding_revision) ||
    !hex(value.ownership_sha256) || !hex(value.profile_sha256) ||
    ["state", "reason", "repo", "main_branch", "head", "commit_anchor", "opencode_project_id", "ownership_mode", "read_at"].some(key => typeof value[key] !== "string") ||
    typeof value.dirty !== "boolean" || typeof value.head_advanced !== "boolean") throw Error("Invalid exact Main status");
  return value;
}

export class MainClientControls {
  constructor(host) { this.host = host; this.sync(null); }
  sync(scope) {
    const key = JSON.stringify(scope);
    if (key === this.scopeKey) return;
    this.scopeKey = key;
    this.mains = []; this.goals = []; this.selectedMain = null; this.retirement = null;
    this.opening = null;
  }
  apply(scope, main, cx) {
    this.sync(scope);
    if (main?.protocol !== "ocq-main-v1" || main.project_id !== scope.project || !Array.isArray(main.items) ||
      main.items.length > 128) throw Error("Invalid Main observation");
    this.mains = main.items.map(item => mainStatus(item, scope.project));
    this.goals = (main.goals || []).map(goal => {
      if (goal.protocol !== "ocq-goal-lineage-v1" || goal.integration_authorized !== false ||
        !Array.isArray(goal.links) || !Array.isArray(goal.work) || goal.work.length !== goal.links.length ||
        goal.main?.project_id !== scope.project || goal.main.mode !== "send" ||
        goal.links.some((link, i) => link.protocol !== goal.protocol || link.operation_id !== goal.main.operation_id ||
          link.prompt_id !== goal.main.prompt_id || link.main_intent_sha256 !== goal.main.intent_sha256 ||
          link.request_id !== goal.work[i]?.request_id || link.work_intent_sha256 !== goal.work[i]?.intent_sha256))
        throw Error("Exact goal lineage unavailable");
      return goal;
    });
    this.selectedMain = main.selected ? mainStatus(main.selected, scope.project) : null;
    if (main.retirement !== undefined && (!["deleted", "uncertain"].includes(main.retirement) || this.selectedMain))
      throw Error("Invalid Main retirement state");
    this.retirement = main.retirement ?? null;
    if (new Set(this.mains.map(item => item.operation_id)).size !== this.mains.length) throw Error("Duplicate Main records");
    cx.notify();
  }
  targetSession(kind, work) {
    if (kind === "integrator" && !work) return this.selectedMain?.session_id;
    if (kind === "worker") return this.host.sessions.rows.find(v => v.request_id === work && !v.job.session_state)?.job.session_id;
  }
  openingStatus(scope, kind, work, session) {
    if (!this.host.sessions.current(scope) || JSON.stringify(scope) !== this.scopeKey || this.targetSession(kind, work) !== session ||
        !session || this.opening?.key !== JSON.stringify([kind, work, session])) return;
    return this.opening;
  }

  async openConversation(kind, work, cx, selected = null) {
    const sessions = this.host.sessions, scope = sessions.bound;
    const session = this.targetSession(kind, work);
    const previous = this.openingStatus(scope, kind, work, session);
    if (this.opening?.busy || selected && !previous?.choices?.includes(selected)) return;
    if (!sessions.current(scope) || !sessionID(session) || JSON.stringify(scope) !== this.scopeKey) return;
    const key = JSON.stringify([kind, work, session]);
    const operation = { key, busy: true, message: "Checking existing OpenCode tabs…" };
    this.opening = operation;
    cx.notify();
    const current = () => this.opening === operation && this.openingStatus(scope, kind, work, session) === operation;
    const target = JSON.stringify({ protocol: "ocq-tui-open-v1", queue: scope.queue, store: scope.store,
      project: scope.project, kind, work, session });
    try {
      if (scope.machine !== null) {
        operation.message = "Opening an existing OpenCode tab is available only on this computer.";
        return;
      }
      const space = openingSpace(scope);
      if (!space) {
        operation.message = "Select this project’s existing Herdr space first.";
        return;
      }
      if (!selected) {
        const viewers = existingTerminals(space);
        if (viewers.length > 24) {
          operation.message = "There are too many terminals to check safely in this space.";
          return;
        }
        const checked = await Promise.all(viewers.map(async v => {
          const client = await contact("check", v, target);
          return client ? { ...v, client } : null;
        }));
        if (!current()) return;
        const here = openingSpace(scope);
        const live = here ? existingTerminals(here) : [];
        if (!here || viewers.length !== live.length || viewers.some(v => !live.some(p => sameTerminal(v, p)))) {
          operation.message = "The terminals in this space changed. Click Open again.";
          return;
        }
        const choices = checked.filter(Boolean);
        if (choices.length !== 1) {
          operation.choices = choices;
          operation.message = choices.length ? "Choose the existing OpenCode tab or pane to open." :
            "No matching OpenCode tab. Open OpenCode normally in this space with the OCQ Open plugin enabled.";
          return;
        }
        selected = choices[0];
      }
      const stillHere = () => {
        const here = openingSpace(scope);
        return current() && here && existingTerminals(here).some(v => sameTerminal(v, selected));
      };
      operation.message = "Opening in the selected OpenCode tab…";
      cx.notify();
      if (!stillHere()) { operation.message = "That OpenCode tab changed. Nothing was sent."; return; }
      if (!await contact("open", selected, target)) {
        operation.message = "Could not confirm the exact OpenCode route. Check the existing tab; nothing was relaunched.";
        return;
      }
      if (!stillHere()) {
        operation.message = "OpenCode confirmed the route, but the Herdr pane changed and was not focused.";
        return;
      }
      run("pane.focus", { pane: selected.pane });
      // Herdr applies a cross-tab focus on the foreground executor after
      // this script call yields. Observe the resulting model, not run().
      for (let i = 0; i < 10 && stillHere(); i++) {
        const focus = focused();
        if (focus?.space === scope.space && focus.tab === selected.tab && focus.pane === selected.pane &&
            focus.terminal === selected.terminal && !focus.shown) {
          operation.message = "Opened in the existing OpenCode tab.";
          return;
        }
        await new Promise(resolve => cx.timer.after(50, () => resolve()));
      }
      operation.message = "OpenCode confirmed the route, but Herdr did not confirm focus.";
    } catch (_) {
      operation.message = "Could not confirm the exact OpenCode tab. Nothing was relaunched.";
    } finally {
      operation.busy = false;
      if (current()) cx.notify();
    }
  }
}
