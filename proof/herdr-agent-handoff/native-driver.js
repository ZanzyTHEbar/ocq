// Private native fixture commands only. Invoke the actual rendered callbacks.
import { exec, spaces, panes, terminals } from "herdr";

let retainedCopy;
let retainedHandoff;
export async function nativeAction(owner, command, cx) {
  let error = null;
  try {
    const q = owner.queue;
    if (command === "race-handoff-open") {
      const r = await exec("ocq-dispatch-race-gate", ["target"], { on: null });
      if (r.code !== 0) throw Error("Exact fixture target absent");
      owner.queueMode = true;
      q.read(owner, "detail", ["queue-job", JSON.parse(r.stdout).job_id], cx);
    } else if (command === "race-handoff-copy") {
      const fn = q.nativeActions?.["ocq-agent-handoff-copy"];
      if (!fn) throw Error("Actual rendered copy callback absent");
      retainedCopy = fn; retainedHandoff = q.handoff;
      fn(cx);
    } else if (command === "race-handoff-verify") {
      const fn = q.nativeActions?.["ocq-agent-handoff-verify"];
      if (!fn) throw Error("Actual rendered verification callback absent");
      fn(cx);
    } else if (command === "race-handoff-stored") {
      const fn = q.nativeActions?.["ocq-agent-handoff-stored"];
      if (!fn) throw Error("Actual stored callback absent");
      fn(cx);
    } else if (command === "race-handoff-stale-copy") {
      if (!retainedCopy) throw Error("No retained callback");
      cx.write_to_clipboard("owned stale-copy sentinel (not a session)");
      retainedCopy(cx);
    } else if (command !== "race-handoff-snapshot") throw Error("Unknown native fixture command");
  } catch (e) { error = String(e); }
  cx.notify();
  const h = owner.queue.handoff;
  await exec("ocq-dispatch-race-gate", ["snapshot", JSON.stringify({ error, working: !!h.working, status: h.status, current: !!h.current(owner, h.t), clipboard: cx.read_from_clipboard(), retainedStatus: retainedHandoff?.status, retainedWorking: !!retainedHandoff?.working, spaces: spaces(), panes: panes(), terminals: terminals() })], { on: null });
}
