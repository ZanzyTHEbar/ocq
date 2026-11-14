// Private commands invoke product gates/callbacks; boundary responses are fault
// injected by the real process proxy, never by replacing the queue or API.
import { exec as rawExec, spaces, panes, terminals, machines } from "herdr";
import { compatibility, ACTIONS, exec as bridgeExec } from "./compatibility.js";
import { div } from "gpui-kit";

// Installed-copy scheduling hook only: real sockets, receiver and product
// guards are unchanged. No payload enters the hook or its command stream.
export async function transferCheckpoint(file, step) {
  await rawExec("ocq-compat-gate", ["transfer-checkpoint", file, String(step)], { on: null, timeout: 35000 });
}
export async function peerStartCheckpoint() { const r = await rawExec("ocq-compat-gate", ["peer-start"], { on: null, timeout: 35000 }); if (r.code !== 0) throw Error("Peer start gate failed"); }
export async function peerStartSettled() { await rawExec("ocq-compat-gate", ["peer-start-settled"], { on: null }); }

export async function nativeAction(owner, command, cx) {
  let error = null, denied = [];
  try {
    if (command === "compat-transfer") {
      const config = JSON.parse((await rawExec("ocq-compat-gate", ["transfer-config"], { on: null })).stdout);
      cx.spawn(async cx => {
        let refused = false;
        try {
          if (config.Family === "compose") await owner.composeExchange("compose-receive", null, config.Raw, () => owner.machine === null && spaces().some(s => s.active && s.id === owner.spaceID), cx);
          else if (config.Family === "intake") {
            const context = Object.freeze({ machine: owner.machine, space: owner.spaceID, folder: owner.folder, selection: owner.selected });
            owner.intake.context = context; owner.intake.screen = true;
            await owner.intakeExchange("receive", "registration", null, config.Raw, context, cx);
          } else if (config.Family === "publication" || config.Family === "capture") {
            const p = owner.publication;
            if (config.Family === "capture") p.captureID = config.CaptureID;
            await p.exchange(owner, p.context, "receive", config.Digest || null, null, config.Raw, cx);
          } else if (config.Family === "resolution") {
            const r = owner.queue.resolution;
            await r.exchange(owner, "receive", r.t, null, config.Raw, cx);
          } else {
            const s = owner.queue.submission;
            await s.exchange(owner, "draft", null, JSON.stringify({key:"blocked-transfer",executable:"/bin/true",arguments:[],cwd:owner.folder,memory_bytes:"0"}), cx);
          }
        } catch (_) { refused = true; }
        await rawExec("ocq-compat-gate", ["transfer-done", JSON.stringify({ refused })], { on: null }); cx.notify();
      });
    }
    else if (command === "compat-boundary-open") {
      const config = JSON.parse((await rawExec("ocq-compat-gate", ["transfer-config"], { on: null })).stdout);
      if (config.Family === "resolution") {
        owner.publication.reset(); owner.queueMode = true;
        owner.queue.read(owner, "detail", ["queue-job", config.Job], cx);
      } else {
        owner.queueMode = false; owner.select(config.Project, cx);
        owner.publication.open(owner, config.Job, cx, config.Family === "capture");
      }
    }
    else if (command === "compat-command-open") { owner.queueMode = true; owner.queue.submission.open(owner, cx); }
    else if (command === "compat-agent-open") { owner.queueMode = true; owner.queue.agentSubmission.open(owner, cx); }
    else if (command === "compat-agent-mode-cancel") {
      let a = owner.queue.agentSubmission;
      for (const mode of ["import", "guided", "import"]) {
        const old = a;
        a.modeControls(owner, cx, (id, _label, click) => { if (id === `ocq-agent-${mode}`) click(cx); return div(); });
        a = owner.queue.agentSubmission;
        if (old.current(owner) || !a.boundCurrent?.()) throw Error("Agent replacement scope not bound");
      }
      const config = JSON.parse((await rawExec("ocq-compat-gate", ["transfer-config"], { on: null })).stdout);
      const draft = await a.exchange(owner, "import", null, config.Import, cx);
      const prepared = await a.exchange(owner, "receive", null, draft.review.raw, cx);
      a.status = prepared.status;
      a.status = await a.call("arm", a.status.id);
      if (a.status.state !== "armed") throw Error("Agent replacement arm failed");
      a.status = await a.call("cancel", a.status.id);
      if (a.status.state !== "cancelled") throw Error("Agent replacement cancel failed");
    }
    else if (command === "compat-peer-descriptor") {
      // Native IDs are machine-qualified; the bridge runs inside that core and
      // consumes its local space ID. Strip only the exact observed prefix.
      const prefix = `${owner.machine}/`;
      if (!owner.machine || !owner.spaceID.startsWith(prefix)) throw Error("Exact foreign space qualification unavailable");
      const localSpace = owner.spaceID.slice(prefix.length);
      if (!/^sp[0-9a-f]{32}$/.test(localSpace)) throw Error("Invalid actual peer-local space ID");
      const r = await rawExec("ocq-herdr", ["compatibility", localSpace, owner.folder], { on: owner.machine });
      if (r.code !== 0) throw Error("Actual selected-machine descriptor unavailable");
      await rawExec("ocq-compat-gate", ["peer-descriptor", r.stdout], { on: null });
    }
    else if (command === "compat-foreign-open") {
      const r = await rawExec("ocq-compat-gate", ["peer-target"], { on: null });
      owner.queueMode = true; owner.queue.read(owner, "detail", ["queue-job", JSON.parse(r.stdout).job], cx);
    }
    else if (command === "compat-check") await compatibility.check();
    else if (command === "compat-delay") cx.spawn(async cx => { try { await compatibility.check(); } catch (_) {} await rawExec("ocq-compat-gate", ["delay-settled"], { on: null }); cx.notify(); });
    else if (command === "compat-audit") {
      const before = spaces().find(s => s.active);
      for (const action of Object.keys(ACTIONS)) {
        try { await bridgeExec("ocq-herdr", [action, before.id, compatibility.status?.queue_identity || "0".repeat(64)], { on: null, current: () => spaces().some(s => s.active && s.id === before.id && s.machine === before.machine && s.folder === before.folder) }); throw Error(`Mutation crossed boundary: ${action}`); }
        catch (e) { if (String(e).includes("crossed boundary")) throw e; denied.push(action); }
      }
    } else if (command === "compat-open") {
      const r = await rawExec("ocq-compat-gate", ["target"], { on: null });
      owner.queueMode = true;
      owner.queue.read(owner, "detail", ["queue-job", JSON.parse(r.stdout).job], cx);
    } else if (command === "compat-prepare") owner.queue.cancel.action(owner, "prepare", cx);
    else if (command === "compat-cancel") owner.queue.cancel.send(owner, cx);
    else if (command === "compat-dispatch-open") owner.queue.dispatcher.open(owner, cx);
    else if (command === "compat-dispatch-prepare") {
      const d = owner.queue.dispatcher;
      d.t = d.candidates()[0];
      if (!d.t) throw Error("Missing exact fixture anchor");
      d.act(owner, "prepare", cx);
    } else if (command === "compat-dispatch-start") {
      const d = owner.queue.dispatcher;
      d.confirm = "start"; d.confirmStart(owner, cx);
    } else if (command !== "compat-snapshot") throw Error("Unsupported fixture command");
  } catch (e) { error = String(e); }
  cx.notify();
  const q = owner.queue, d = q.dispatcher;
  await rawExec("ocq-compat-gate", ["snapshot", JSON.stringify({ error, denied, ready: compatibility.ready(), compatibility: compatibility.status, reason: compatibility.reason, pending: !!compatibility.pending, cancel: q.cancel.status, cancelWorking: !!q.cancel.working, dispatcher: d.status, dispatchWorking: !!d.working, detail: q.detail.data, detailError: q.detail.error, detailLoading: !!q.detail.loading, submission: q.submission.status, agentSubmission: q.agentSubmission.status, publicationScope: owner.publication.scope, publicationWorking: !!owner.publication.working, resolution: q.resolution.status, machines: machines(), spaces: spaces(), panes: panes(), terminals: terminals() })], { on: null });
}
