import { bridgeCommand, compatibility } from "./compatibility.js";
import { readableValue } from "./readable-record.js";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { ComposeForm } from "./compose-form.js";
import { composeDecode } from "./private-frames.js";

function visibleString(value) {
  return readableValue(value);
}

export class ComposeController {
  constructor(owner) {
    this.owner = owner;
    this.compose = { screen: false, state: "loading", id: null, preview: null, working: false, error: null };
  }

  get handle() { return this.owner.handle; }

  clearInspection() {
    this.compose = { ...this.compose, screen: false, preview: null, context: null, form: null, options: null, confirm: false, working: false };
  }

  composeContext(lane) {
    const owner = this.owner;
    if (!owner.composeLocal() || !owner.selected || !lane?.lane_id || !lane?.owner_job_id) return null;
    return Object.freeze({ space: owner.spaceID, folder: owner.folder, project: owner.selected, lane: lane.lane_id, job: lane.owner_job_id });
  }

  composeContextCurrent(context) {
    const owner = this.owner;
    return Boolean(context && owner.composeLocal() && context.space === owner.spaceID && context.folder === owner.folder && context.project === owner.selected &&
      (owner.board?.project?.lanes ?? []).some((lane) => lane.lane_id === context.lane && lane.owner_job_id === context.job));
  }

  composeCurrent(c, context) {
    return !c.authoritySuspended && this.compose === c && c.screen && c.context === context && this.composeContextCurrent(context);
  }

  composeQuoted(value) { return visibleString(value); }

  async composeStoredContext(context, manifest = "", reply = "") {
    const result = await bridgeCommand(["compose-context", context.project, context.lane, manifest, reply], { on: null });
    if (result.code !== 0) throw new Error("stored compose links unavailable");
    const info = JSON.parse(result.stdout);
    if (info.project_id !== context.project || info.lane_id !== context.lane || info.job_id !== context.job || info.manifest !== manifest || info.reply_to !== reply || typeof info.lane_path !== "string" || !Array.isArray(info.integrator_senders) || !Array.isArray(info.recipient_lanes)) throw new Error("stored owner changed");
    return info;
  }

  composeRoute(request, info = null, recovered = true) {
    if (request.Kind === "lane.blocked") return [`lane:${request.LaneID}`, `job:${request.JobID}`].includes(request.From) && request.To === `integrator:${request.ProjectID}`;
    return request.Kind === "lane.assigned" && typeof request.From === "string" && request.From.startsWith("integrator:") && typeof request.To === "string" && request.To.startsWith("lane:") &&
      (!info || info.recipient_lanes.some((lane) => request.To === `lane:${lane}`)) && (recovered || info?.integrator_senders.includes(request.From));
  }

  composePreviewPages(request, info = null, raw = "") {
    const pages = [];
    const path = request.FsforkPath ?? "", implicit = path === "";
    for (const [label, value] of [
      ["Machine", "this computer"], ["Project", request.ProjectID], ["Lane", request.LaneID], ["Job", request.JobID],
      ["From", request.From], ["To", request.To], ["Kind", request.Kind],
      ["Identity meaning", "Stored routing metadata only; not OS identity, authentication, permission or execution approval."],
      [`Manifest (${request.Manifest === undefined ? "omitted → none" : request.Manifest === "" ? "explicit empty → none" : "retained attachment"})`, request.Manifest ?? ""],
      [`Reply to (${request.ReplyTo === undefined ? "omitted → no reply" : request.ReplyTo === "" ? "explicit empty → no reply" : "retained same-project message"})`, request.ReplyTo ?? ""],
      [`Requested FsforkPath (${request.FsforkPath === undefined ? "omitted → empty/default" : implicit ? "explicit empty → lane default" : "nonempty literal override"})`, path],
      ["Reviewed stored lane default (empty remains empty)", info ? info.lane_path : "UNAVAILABLE — cannot confirm"],
      ["Effective path to recheck/pin at claim", implicit ? info ? info.lane_path : "UNAVAILABLE — cannot confirm" : path],
      ["Path meaning", implicit ? "Use the stored lane default, even if that default is empty. A changed default requires a new review." : "Exact literal metadata override, even if its text says lane default. No path is opened or created."],
      ["Retained link lookup", info ? "Manifest/reply IDs checked in this project's stored SQLite snapshot; send rechecks in its transaction." : "Unavailable; raw fields may be read/cancelled, never confirmed."],
      ["Subject", request.Subject], [request.Body === undefined ? "Message (omitted → empty)" : "Message", request.Body ?? ""],
      ["Exact retained request SHA-256", createHash("sha256").update(raw).digest("hex")],
    ]) {
      const tokens = visibleString(value).match(/\\(?:u[0-9a-fA-F]{4}|["\\/bfnrt])|[\s\S]/gu) || [];
      for (let start = 0; start < tokens.length;) {
        let end = start, length = 0;
        while (end < tokens.length && length + tokens[end].length <= 160) length += tokens[end++].length;
        pages.push(`${label}${start ? " (continued)" : ""}:\n${tokens.slice(start, end).join("")}`);
        start = end;
      }
    }
    return pages;
  }

  advanceComposePage(delta, cx) {
    const c = this.compose, p = c.preview;
    if (!p || c.working || c.confirm || !this.composeCurrent(c, c.context)) return;
    const next = Math.max(0, Math.min(p.pages.length - 1, c.page + delta));
    if (delta === 1 && c.page === p.openedThrough && next === c.page + 1) p.openedThrough = next;
    c.page = next; cx.notify();
  }

  canReviewCompose() {
    const c = this.compose, p = c.preview;
    return Boolean(!c.working && c.state === "prepared" && p?.state === "prepared" && p.id === c.id && p.info && this.composeCurrent(c, c.context) && c.page === p.pages.length - 1 && p.openedThrough === p.pages.length - 1);
  }

  refreshCompose(cx) {
    const owner = this.owner;
    if (owner.queueMode) return;
    const c = this.compose;
    if (!owner.composeLocal() || c.working) return;
    const scope = { space: owner.spaceID, folder: owner.folder, project: owner.selected, screen: c.screen };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen === scope.screen && owner.composeLocal() && scope.space === owner.spaceID && scope.folder === owner.folder && scope.project === owner.selected;
    c.working = true;
    cx.spawn(async (cx) => {
      try {
        const result = await bridgeCommand(["compose-status"], { on: null });
        if (result.code !== 0 || !current()) throw new Error("status unavailable");
        const status = JSON.parse(result.stdout);
        if (!["idle", "busy", "prepared", "armed", "dispatch_intent", "completed", "cancelled", "blocked"].includes(status.state)) throw new Error("invalid status");
        if (c.state === "uncertain") {
          if (status.id === c.id && ["prepared", "armed"].includes(status.state)) {
            if (c.recoveryState !== status.state) c.preview = null;
            c.recoveryState = status.state;
            c.error = "The exact intent is unattempted now. A delayed claim or send may still race; preview and explicitly cancel under the journal lock. Do not resend.";
            return;
          }
          c.recoveryState = null; c.preview = null;
          if (status.id !== c.id || !["dispatch_intent", "completed", "cancelled"].includes(status.state)) {
            c.error = "Send outcome remains uncertain. A busy, unavailable or different journal does not authorize a retry.";
            return;
          }
        }
        if (c.id !== (status.id ?? null) || c.state !== status.state) c.observation = null;
        c.state = status.state; c.id = status.id ?? null; c.observation = status.observation ?? null; c.recoveryState = null;
        if (c.preview && (!["prepared", "armed"].includes(status.state) || c.preview.id !== status.id || c.preview.state !== status.state)) c.preview = null;
        c.error = null;
        if (c.state === "idle" && this.composeCurrent(c, c.context)) {
          const context = c.context;
          const info = await this.composeStoredContext(context);
          if (!current() || !this.composeCurrent(c, context)) throw new Error("context changed");
          c.options = info;
        }
      } catch (_e) { if (current()) { if (c.state !== "uncertain") c.state = "blocked"; c.preview = null; c.error = "Compose status/options unavailable. Do not send; check the local store."; } }
      finally { if (current()) { c.working = false; cx.notify(); } }
    });
  }

  openCompose(lane, cx) {
    const previous = this.compose;
    if (previous.working && previous.screen && this.composeContextCurrent(previous.context)) return;
    const c = this.compose = { ...previous, authoritySuspended: false, working: false, preview: null, form: null, options: null, context: null };
    c.screen = true; c.confirm = false; c.error = null;
    if (lane && ["idle", "loading"].includes(c.state)) {
      c.context = this.composeContext(lane);
      if (c.context) c.form = new ComposeForm(this, c.context);
    }
    this.handle.focus();
    this.owner.keyboardMode = false;
    this.refreshCompose(cx);
    cx.notify();
  }

  composeMode(kind, cx) {
    const c = this.compose;
    if (c.working || c.state !== "idle" || !this.composeCurrent(c, c.context) || !["lane.blocked", "lane.assigned"].includes(kind)) return;
    const context = Object.freeze({ ...c.context });
    this.compose = { ...c, context, form: new ComposeForm(this, context, kind), preview: null, confirm: false, error: null };
    this.handle.focus(); cx.notify();
  }

  closeCompose(cx) {
    // Keep recovery identity/state, not the form, confirmation or callback scope.
    this.compose = { ...this.compose, screen: false, working: false, context: null, form: null, options: null, preview: null, confirm: false };
    cx.notify();
  }

  async composeExchange(command, id, payload, current, cx) {
    current = compatibility.transfer(current);
    if (!current()) throw new Error("context changed");
    const launch = randomBytes(32).toString("hex");
    const value = await compatibility.exchange(command, command === "compose-preview" ? [id, launch] : [launch], payload, current);
    if (command === "compose-preview") {
      if (value.status?.id !== id || !["prepared", "armed"].includes(value.status.state) || typeof value.response !== "string") throw Error("preview status mismatch");
      return value;
    }
    if (!/^PREPARED [0-9a-f]{64}$/.test(value.response)) throw Error("preparation uncertain");
    return value.response;
  }

  prepareCompose(cx) {
    const c = this.compose, context = c.context, form = c.form;
    if (c.working || c.state !== "idle" || !form || !c.options || !this.composeCurrent(c, context)) return;
    const snapshot = form.snapshot(), current = () => this.composeCurrent(c, context);
    let request, payload;
    try {
      request = form.request(context, c.options);
      for (const value of Object.values(request)) if (typeof value !== "string" || composeDecode(Buffer.from(value, "utf8")) !== value) throw new Error("invalid text");
      if (!request.Subject || Buffer.from(request.Subject).length > 512 || Buffer.from(request.Body).length > 16384 || request.Subject.includes("\0") || request.Body.includes("\0")) throw new Error("invalid subject/body");
      payload = JSON.stringify(request);
      if (Buffer.from(payload).length + 1 > 131072) throw new Error("message frame too large");
    } catch (_e) { c.error = "Select a legal stored route and valid retained link IDs; enter subject/body within 512/16384 UTF-8 bytes. A literal path override must be nonempty. Nothing was prepared."; cx.notify(); return; }
    c.working = true; c.error = null; c.observation = null;
    cx.spawn(async (cx) => {
      let launched = false;
      try {
        const options = await this.composeStoredContext(context, request.Manifest, request.ReplyTo);
        if (!current() || c.form !== form || form.snapshot() !== snapshot || !this.composeRoute(request, options, false)) throw new Error("form/route changed");
        launched = true;
        const reply = await this.composeExchange("compose-receive", null, payload, current, cx);
        if (current()) { c.id = reply.slice(9); c.state = "prepared"; c.preview = null; }
      } catch (_e) { if (current()) { if (launched) c.state = "blocked"; c.error = launched ? "Preparation outcome unknown. Refresh journal status before doing anything else." : "Stored route, attachment or reply unavailable/changed. Nothing was prepared."; } }
      finally { if (current()) { c.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  previewCompose(cx) {
    const owner = this.owner;
    const c = this.compose, id = c.id, initialState = c.state, initialRecovery = c.recoveryState;
    if (c.working || !(["prepared", "armed"].includes(c.state) || (c.state === "uncertain" && ["prepared", "armed"].includes(c.recoveryState))) || !id || !owner.composeLocal() || !owner.selected) return;
    // The selected project must be explicit; the returned lane/job is checked
    // against the current stored board before it can be confirmed.
    const context = { space: owner.spaceID, folder: owner.folder, project: owner.selected };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen && c.id === id && c.state === initialState && c.recoveryState === initialRecovery && owner.composeLocal() && context.space === owner.spaceID && context.folder === owner.folder && context.project === owner.selected;
    c.working = true; c.error = null; c.preview = null; c.confirm = false;
    cx.spawn(async (cx) => {
      try {
        const { response, status } = await this.composeExchange("compose-preview", id, null, current, cx);
        if (!current() || ((initialState === "armed" || initialRecovery === "armed") && status.state !== "armed")) throw new Error("preview state changed");
        const request = JSON.parse(response);
        const allowed = ["ProjectID", "From", "To", "LaneID", "JobID", "Kind", "Subject", "Body", "Manifest", "ReplyTo", "FsforkPath"];
        if (!request || Array.isArray(request) || Object.keys(request).some((key) => !allowed.includes(key)) || Object.values(request).some((value) => typeof value !== "string" || composeDecode(Buffer.from(value)) !== value)) throw new Error("unsupported fields");
        const actual = Object.freeze({ ...context, lane: request.LaneID, job: request.JobID });
        if (!current() || !this.composeContextCurrent(actual) || request.ProjectID !== context.project || !this.composeRoute(request)) throw new Error("context mismatch");
        let info = null;
        try { info = await this.composeStoredContext(actual, request.Manifest ?? "", request.ReplyTo ?? ""); } catch (_e) { /* Allow exact recovery/cancel, never confirmation with unavailable links. */ }
        if (!current() || (info && !this.composeRoute(request, info))) throw new Error("route/context changed");
        if (initialState === "uncertain") c.recoveryState = status.state;
        else c.state = status.state;
        c.context = actual; c.preview = { id, state: status.state, request, raw: response, info, pages: this.composePreviewPages(request, info, response), openedThrough: 0 }; c.page = 0; c.confirm = false;
        if (!info) c.error = "Retained links/default unavailable. Exact fields remain readable and cancellable, but cannot be confirmed.";
      } catch (_e) {
        if (current()) { if (c.state !== "uncertain") c.state = "blocked"; c.error = "Preview unavailable or belongs to another project/lane. Refresh journal status; nothing was sent."; }
      }
      finally { if (this.compose === c && c.screen && owner.composeLocal() && context.space === owner.spaceID && context.folder === owner.folder && context.project === owner.selected) { c.working = false; this.handle.focus(); cx.notify(); } }
    });
    cx.notify();
  }

  composeAction(action, cx) {
    const owner = this.owner;
    const c = this.compose, id = c.id, preview = c.preview, wasUncertain = c.state === "uncertain", context = c.context;
    if (c.working || !id || !owner.composeLocal()) return;
    if (action === "dispatch" && (!c.confirm || !this.canReviewCompose())) return;
    if (action === "cancel" && !((["prepared", "armed"].includes(c.state) || (wasUncertain && ["prepared", "armed"].includes(c.recoveryState))) && preview?.id === id && this.composeCurrent(c, context))) return;
    if (action === "reconcile" && !["dispatch_intent", "uncertain", "completed"].includes(c.state)) return;
    if (action === "retire" && !["completed", "cancelled"].includes(c.state)) return;
    if (!["dispatch", "cancel", "reconcile", "retire"].includes(action)) return;
    const scope = { space: owner.spaceID, folder: owner.folder, project: owner.selected };
    const current = () => !c.authoritySuspended && this.compose === c && c.screen && owner.composeLocal() && scope.space === owner.spaceID && scope.folder === owner.folder && scope.project === owner.selected;
    c.working = true; c.confirm = false; c.error = null;
    cx.spawn(async (cx) => {
      let claimLaunched = false, claimKnown = false, sendLaunched = false;
      try {
        if (action === "dispatch") {
          const exact = await this.composeExchange("compose-preview", id, null, () => current() && this.composeCurrent(c, context) && c.preview === preview, cx);
          const info = await this.composeStoredContext(context, preview.request.Manifest ?? "", preview.request.ReplyTo ?? "");
          if (!current() || !this.composeCurrent(c, context) || c.preview !== preview || exact.response !== preview.raw || exact.status.state !== "prepared" || info.lane_path !== preview.info.lane_path || !this.composeRoute(preview.request, info)) throw new Error("retained fields/default changed");
          // The send process is never launched until this separate, durable
          // claim has been acknowledged. A delayed claim cannot send by itself.
          c.state = "uncertain"; c.recoveryState = null; c.preview = null; claimLaunched = true;
          const claim = await bridgeCommand(["compose-arm", id], { on: null, current: () => current() && c.id === id });
          if (claim.code !== 0) throw new Error("claim outcome unavailable");
          const armed = JSON.parse(claim.stdout);
          if (armed.id !== id || armed.state !== "armed") throw new Error("claim identity mismatch");
          claimKnown = true;
          if (!current() || !this.composeCurrent(c, context)) return;
          const afterClaim = await this.composeStoredContext(context, preview.request.Manifest ?? "", preview.request.ReplyTo ?? "");
          if (!current() || !this.composeCurrent(c, context)) return;
          if (afterClaim.lane_path !== preview.info.lane_path || !this.composeRoute(preview.request, afterClaim)) throw new Error("claimed context changed");
        }
        if (!current() || c.id !== id || action === "cancel" && (c.preview !== preview || !this.composeCurrent(c, context))) throw new Error("context changed");
        if (action === "dispatch") sendLaunched = true;
        const result = await bridgeCommand([`compose-${action}`, id], { on: null, current: () => current() && c.id === id });
        if (result.code !== 0 || !current()) throw new Error("action unavailable");
        const status = JSON.parse(result.stdout);
        if (status.id !== id) throw new Error("identity mismatch");
        if (action === "retire" && status.state === "retired") { c.state = "idle"; c.id = null; c.preview = null; c.context = null; c.form = null; c.options = null; c.observation = null; }
        else if (["dispatch_intent", "completed", "cancelled"].includes(status.state)) { c.state = status.state; c.observation = status.observation; c.preview = null; c.recoveryState = null; }
        else throw new Error("unexpected state");
      } catch (_e) { if (current()) {
        if (action === "dispatch" && !claimLaunched) { c.preview = null; c.error = "Exact retained fields, links or lane default changed/unavailable. Nothing was claimed. Preview again."; }
        else if (action === "dispatch" && claimKnown && !sendLaunched) { c.state = "armed"; c.preview = null; c.error = "Claim retained, but route/default changed or became unavailable. No send launched; refresh, preview and cancel."; }
        else { c.error = action === "dispatch" || action === "reconcile" || wasUncertain ? "Outcome uncertain. Refresh and check the stored intent; do not resend." : "Action outcome unavailable. Refresh journal status."; if (wasUncertain || action === "dispatch") { c.state = "uncertain"; c.recoveryState = null; c.preview = null; } else if (action !== "reconcile") c.state = "blocked"; }
      } }
      finally { if (current()) { c.working = false; cx.notify(); } }
    });
    cx.notify();
  }
}
