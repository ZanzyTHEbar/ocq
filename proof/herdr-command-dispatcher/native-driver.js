// Installed only into a fresh private proof mod. These explicit native commands
// invoke the same product callbacks as buttons; they never replace core effects.
import { exec, spaces, panes, terminals, focused } from "herdr";

export async function nativeAction(owner, command, cx) {
  let error = null;
  try {
    const d = owner.queue.dispatcher;
    if (command === "race-open") {
      owner.queueMode = true;
      d.open(owner, cx);
    } else if (command === "race-select") {
      const r = await exec("ocq-dispatch-race-gate", ["target"], { on: null });
      if (r.code !== 0) throw Error("Missing explicit proof target");
      const target = JSON.parse(r.stdout);
      if (!d.current(owner) || d.working) throw Error("View changed");
      const candidate = d.candidates().find(t => t.pane === target.pane && t.terminal === target.terminal && t.tab === target.tab && t.scope.space_id === target.space);
      if (!candidate) throw Error("Exact supplied native target not available");
      d.t = candidate; owner.handle.focus();
    } else if (command === "race-prepare") d.act(owner, "prepare", cx);
    else if (command === "race-start") { d.confirm = "start"; owner.handle.focus(); d.confirmStart(owner, cx); }
    else if (command === "race-drain-prepare") d.act(owner, "drain-prepare", cx);
    else if (command === "race-drain") { d.confirm = "drain"; owner.handle.focus(); d.confirmDrain(owner, cx); }
    else if (command === "race-read") d.act(owner, "read", cx);
    else if (command === "race-retire") { d.confirm = "retire"; d.act(owner, "retire", cx); }
    else if (command !== "race-snapshot") throw Error("Unsupported proof command");
  } catch (e) { error = String(e); }
  cx.notify();
  const d = owner.queue.dispatcher;
  await exec("ocq-dispatch-race-gate", ["snapshot", JSON.stringify({ command, error, working: !!d.working, current: !!d.current(owner), status: d.status, uiError: d.error, selected: d.t, candidates: d.candidates(), focused: focused(), spaces: spaces(), panes: panes(), terminals: terminals() })], { on: null });
}
