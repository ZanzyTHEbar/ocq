import { div } from "gpui-kit";
import { readableValue } from "./readable-record.js";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea } from "gpui-base";
import { spaces } from "herdr";
import { bridgeCommand, compatibility } from "./compatibility.js";
import { createHash, randomBytes } from "crypto";
import { Buffer } from "buffer";
import { composeDecode, readInputFile } from "./private-frames.js";

const hash = (s) => createHash("sha256").update(s).digest("hex");
const hex = (s) => /^[0-9a-f]{64}$/.test(s || "");
const visible = readableValue;
const same = (a, b) => a && b && ["machine", "space_id", "queue_identity", "project_id", "job_id", "sequence"].every((k) => a[k] === b[k]);

// The real core has validated these retained bytes. Do not summarize aliases,
// unknown shapes or lossy Unicode. Every field, array item and map key appears;
// Exact bytes remain internal; presentation never offers a raw-data fallback.
function reviewPages(raw, status) {
  const m = JSON.parse(raw);
  const object = (o, keys) => { if (!o || typeof o !== "object" || Array.isArray(o) || Object.keys(o).some((k) => !keys.includes(k))) throw new Error("Unsupported representation"); };
  const common = ["version", "kind", "project_id", "source", "observed_at", "claim_label"];
  if (m.kind === "result") {
    object(m, [...common, "job_id", "request_digest", "base", "worktree", "staged_status", "unstaged_status", "untracked_paths", "before_status_digest", "after_status_digest", "before_content_digest", "after_content_digest", "ignored_exclusions", "changed_files", "diff", "inline_output", "evidence_digests", "claimed_validation"]);
    for (const f of m.changed_files || []) object(f, ["path", "old_mode", "new_mode", "contents"]);
    object(m.claimed_validation, ["status", "commands", "results", "provenance", "limitations"]);
  } else if (m.kind === "review_result") {
    object(m, [...common, "review_job_id", "review_session_id", "target_job_id", "reviewed_result_digest", "verdict", "findings", "limitations"]);
    for (const f of m.findings || []) object(f, ["location", "severity", "message"]);
  } else throw new Error("Unsupported manifest kind");
  if (m.kind !== status.kind || m.source !== status.source || m.project_id !== status.target.project_id || (m.kind === "result" ? m.job_id : m.review_job_id) !== status.target.job_id || m.version !== 1 || !Object.prototype.hasOwnProperty.call(m, "observed_at")) throw new Error("Manifest interpretation mismatch");
  const pages = [];
  const add = (title, text) => {
    const chars = Array.from(text);
    for (let i = 0; i < Math.max(1, chars.length); i += 900) pages.push({ title, text: chars.slice(i, i + 900).join("") || "Empty" });
  };
  const walk = (value, title) => {
    if (typeof value === "string") { if (composeDecode(Buffer.from(value)) !== value) throw new Error("Unsupported Unicode"); add(title, visible(value)); }
    else if (value === null || typeof value === "boolean") add(title, String(value));
    else if (typeof value === "number") { if (!Number.isSafeInteger(value) || Object.is(value, -0)) throw new Error("Unsupported number"); add(title, String(value)); }
    else if (Array.isArray(value)) { if (!value.length) add(title, "None"); value.forEach((v, n) => walk(v, `${title} · ${n + 1}`)); }
    else if (value && typeof value === "object") { if (!Object.keys(value).length) add(title, "None"); for (const k of Object.keys(value)) walk(value[k], `${title} · ${k.replaceAll("_", " ")}`); }
    else throw new Error("Unsupported field");
  };
  walk(m, "Manifest");
  return { fields: pages };
}

export class ResultPublication {
  constructor() {
    this.input = TextareaState.new({ placeholder: "Absolute path to a retained result manifest; for capture, one exclusion per line", rows: 4 });
    this.supersedes = InputState.new({ placeholder: "Supersedes association ID (optional, exact lineage)" });
    for (const input of [this.input, this.supersedes]) {
      input.on("change", (_e, cx) => cx.notify());
      input.on("focus", (_e, cx) => { this.inputFocused = true; cx.notify(); });
      input.on("blur", (_e, cx) => { this.inputFocused = false; cx.notify(); });
    }
    this.reset();
  }
  reset() { this.generation = (this.generation || 0) + 1; this.screen = false; this.context = null; this.scope = null; this.status = null; this.digest = null; this.captureID = null; this.entries = []; this.preview = null; this.confirm = false; this.working = false; this.error = null; this.uncertain = false; this.inputFocused = false; this.input.set_value(""); this.supersedes.set_value(""); }
  current(owner, c = this.context) {
    const a = spaces().find((s) => s.active);
    return this.screen && this.context === c && c && this.generation === c.generation && owner.selected === c.project && owner.spaceID === c.space && owner.machine === c.machine && owner.folder === c.folder && a?.id === c.space && a?.machine === c.machine && a?.folder === c.folder && !owner.queueMode;
  }
  open(owner, job, cx, capture = false) {
    this.reset(); this.context = Object.freeze({ project: owner.selected, job, space: owner.spaceID, machine: owner.machine, folder: owner.folder, generation: this.generation, capture }); this.screen = true; owner.handle.focus();
    this.boundCurrent = () => this.current(owner);
    if (owner.machine === null) this.load(owner, cx); else cx.notify();
  }
  args(t) { return [t.space_id, t.queue_identity, t.project_id, t.job_id, t.sequence]; }
  validate(s, t, digest, capture = false, captureID = null) {
    if (!same(s.target, t) || (capture ? s.capture_id !== captureID || (s.payload_sha256 && !hex(s.manifest_sha256)) : s.manifest_sha256 !== digest) || !["none", "prepared", "armed", "uncertain", "blocked", "changed", "recorded_observed", ...(capture ? ["preparing"] : [])].includes(s.state)) throw new Error("Publication context mismatch");
    if (s.payload_sha256 && (!hex(s.payload_sha256) || !hex(s.ownership_sha256) || !["result", "review_result"].includes(s.kind) || !(capture ? s.kind === "result" && s.source === "machine_capture" : ["operator_import", "author_import"].includes(s.source)))) throw new Error("Publication metadata mismatch");
    if (capture) digest = s.manifest_sha256;
    const a = s.association;
    if (s.state === "recorded_observed" && (!a || a.project_id !== t.project_id || a.job_id !== t.job_id || a.manifest_sha256 !== digest || a.kind !== s.kind || (a.supersedes_id || "") !== s.supersedes_id || a.verification !== (capture ? "machine_verified_snapshot" : "structure_hash_linkage_only") || a.source !== s.source || !a.association_id || !a.recorded_at || !/^[1-9][0-9]*$/.test(a.sequence))) throw new Error("Exact association not established");
    return s;
  }
  async call(action, t, digest, payload) {
    const capture = this.context?.capture, captureID = this.captureID, pointer = capture ? captureID : digest;
    const context = this.context, status = this.status;
    const r = await bridgeCommand([`${capture ? "capture" : "result-publish"}-${action}`, ...this.args(t), ...(action !== "list" && pointer ? [pointer] : []), ...(payload ? [payload] : [])], { on: null, current: () => this.boundCurrent?.() && this.context === context && this.status === status });
    if (r.code !== 0) throw new Error("Publication command unavailable");
    const out = JSON.parse(r.stdout);
    if (payload && out.payload_sha256 !== payload) throw new Error("Claim payload mismatch");
    return action === "list" ? out.map((s) => this.validate(s, t, s.manifest_sha256, capture, s.capture_id)) : this.validate(out, t, digest, capture, captureID);
  }
  load(owner, cx) {
    const c = this.context, digest = this.digest;
    if (this.working || !this.current(owner, c) || c.machine !== null) return;
    this.working = true; this.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, c)) return;
        const r = await bridgeCommand([c.capture ? "capture-context" : "result-publish-context", c.space, c.project, c.job], { on: null });
        if (!this.current(owner, c)) return;
        if (r.code !== 0) throw new Error("Context unavailable");
        const scope = JSON.parse(r.stdout), t = scope.target;
        if (t.machine !== "local" || t.space_id !== c.space || t.project_id !== c.project || t.job_id !== c.job || !hex(t.queue_identity) || !/^[1-9][0-9]*$/.test(t.sequence) || !hex(scope.ownership_sha256)) throw new Error("Context mismatch");
        const entries = await this.call("list", t);
        if (!this.current(owner, c)) return;
        this.scope = scope; this.entries = entries;
        if (digest || this.captureID) { const status = await this.call("status", t, digest); if (this.current(owner, c)) { this.status = status; this.digest = status.manifest_sha256 || null; } }
      } catch (_e) { if (this.current(owner, c)) this.error = "Stored publication context/receipt unavailable. Do not resend an uncertain operation."; }
      finally { if (this.current(owner, c)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  async exchange(owner, c, mode, digest, payloadDigest, raw, cx) {
    if (!this.current(owner, c)) throw new Error("Context changed");
    const current = compatibility.transfer(() => this.current(owner, c));
    const t = Object.freeze({ ...this.scope.target }), launch = randomBytes(32).toString("hex"), captureID = this.captureID, prefix = c.capture ? "capture" : "result-publish";
    if (mode === "receive") {
      const b = Buffer.from(raw);
      if (!b.length || b.length > (c.capture && mode === "receive" ? 131072 : 1048576) || composeDecode(b) !== raw) throw Error("Unsupported encoding");
    }
    const out = await compatibility.exchange(`${prefix}-${c.capture && mode === "receive" ? "prepare" : mode}`,
      [...this.args(t), ...(mode === "preview" ? [c.capture ? captureID : digest, payloadDigest] : c.capture ? [captureID] : []), launch], raw, current);
    const status = this.validate(out.status, t, digest, c.capture, captureID), exact = mode === "preview" ? out.exact : null;
    if (mode === "preview" && (typeof exact !== "string" || hash(exact) !== payloadDigest)) throw Error("Retained payload changed");
    if (!(c.capture && mode === "receive") && status.payload_sha256 !== (mode === "receive" ? hash(raw) : payloadDigest)) throw Error("Payload receipt mismatch");
    return { status, exact };
  }
  prepare(owner, cx) {
    const c = this.context;
    if (this.working || this.uncertain || this.digest || !this.scope || !this.current(owner, c) || c.machine !== null) return;
    if (c.capture) { this.prepareCapture(owner, cx); return; }
    const path = this.input.value(), supersedes = this.supersedes.value(), scope = this.scope;
    if (supersedes && !/^assoc-[0-9a-f]{32}$/.test(supersedes)) { this.error = "Enter an exact association ID, or leave it empty."; cx.notify(); return; }
    this.working = true; this.error = null;
    cx.spawn(async (cx) => {
      try {
        const manifest = await readInputFile(path, 262144);
        if (!this.current(owner, c) || this.scope !== scope || this.input.value() !== path || this.supersedes.value() !== supersedes) return;
        const digest = hash(manifest), raw = JSON.stringify({ target: scope.target, ownership_sha256: scope.ownership_sha256, manifest, supersedes_id: supersedes });
        this.digest = digest; this.uncertain = true;
        const out = await this.exchange(owner, c, "receive", digest, null, raw, cx);
        if (this.current(owner, c)) { this.status = out.status; this.uncertain = false; this.input.set_value(""); this.supersedes.set_value(""); }
      } catch (_e) { if (this.current(owner, c)) this.error = this.uncertain ? "Preparation unavailable or uncertain. Check retained intents only; do not resend. No publication is implied." : "Could not read the selected local manifest file (16 KiB maximum). Nothing was prepared."; }
      finally { if (this.current(owner, c)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  prepareCapture(owner, cx) {
    const c = this.context, scope = this.scope, supersedes = this.supersedes.value();
    let exclusions;
    try { exclusions = this.input.value() === "" ? [] : this.input.value().split("\n"); if (exclusions.some((s) => !s.trim() || s.includes("\0")) || supersedes && !/^assoc-[0-9a-f]{32}$/.test(supersedes)) throw new Error(); }
    catch (_e) { this.error = "Enter one exclusion description per line and an optional supersedes ID. No snapshot captured."; cx.notify(); return; }
    const raw = JSON.stringify({ context: scope, ignored_exclusions: exclusions, supersedes_id: supersedes });
    this.captureID = randomBytes(32).toString("hex"); this.working = true; this.uncertain = true; this.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, c)) return;
        const out = await this.exchange(owner, c, "receive", null, null, raw, cx);
        if (this.current(owner, c)) { this.status = out.status; this.digest = out.status.manifest_sha256; this.input.set_value(""); this.supersedes.set_value(""); }
      } catch (_e) { if (this.current(owner, c)) this.error = "Capture preparation unavailable or uncertain. Read retained capture pointers only; NEVER silently recapture."; }
      finally { if (this.current(owner, c)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  check(owner, digest, cx) {
    const c = this.context;
    if (this.working || !this.scope || !this.current(owner, c)) return;
    const target = Object.freeze({ ...this.scope.target });
    if (c.capture) this.captureID = digest;
    else this.digest = digest;
    this.preview = null; this.confirm = false; this.working = true; this.error = null;
    cx.spawn(async (cx) => {
      try { if (!this.current(owner, c)) return; const out = await this.call("status", target, digest); if (this.current(owner, c)) { this.status = out; this.digest = out.manifest_sha256 || null; } }
      catch (_e) { if (this.current(owner, c)) { this.status = null; this.error = "Exact publication read-back unavailable. Check only; never automatically resend."; } }
      finally { if (this.current(owner, c)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  review(owner, cx) {
    const c = this.context, s = this.status;
    if (this.working || !hex(s?.payload_sha256) || !this.current(owner, c)) return;
    const target = Object.freeze({ ...this.scope.target }), digest = this.digest, payloadDigest = s.payload_sha256;
    this.working = true; this.preview = null; this.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, c)) return;
        const out = await this.exchange(owner, c, "preview", digest, payloadDigest, null, cx);
        if (!this.current(owner, c)) return;
        const p = JSON.parse(out.exact);
        if (!same(p.target, target) || p.ownership_sha256 !== out.status.ownership_sha256 || hash(p.manifest) !== digest || p.supersedes_id !== out.status.supersedes_id) throw new Error("Private review mismatch");
        const pages = reviewPages(p.manifest, out.status);
        this.status = out.status; this.preview = { digest: payloadDigest, payload: p, fields: pages.fields, raw: pages.raw, mode: "fields", page: 0, openedThrough: 0 }; owner.handle.focus();
      } catch (_e) { if (this.current(owner, c)) this.error = "Retained private review unavailable or representation unsupported. Publication disabled; no fields are silently omitted."; }
      finally { if (this.current(owner, c)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  send(owner, cx) {
    const c = this.context, s = this.status, preview = this.preview;
    if (this.working || !this.confirm || !preview || preview.digest !== s?.payload_sha256 || s.state !== "prepared" || !this.current(owner, c)) return;
    const target = Object.freeze({ ...this.scope.target }), digest = this.digest, payloadDigest = preview.digest;
    this.working = true; this.uncertain = true;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, c) || this.preview !== preview) return;
        const claim = await this.call("arm", target, digest, payloadDigest);
        if (!this.current(owner, c) || this.preview !== preview) return;
        if (claim.state === "armed") {
          if (!this.current(owner, c)) return;
          await this.call("attempt", target, digest, payloadDigest);
        } else if (claim.state !== "recorded_observed") throw new Error("Claim not acknowledged");
      } catch (_e) { if (this.current(owner, c)) this.error = "Claim/publication response unavailable. Check exact retained association only; never resend."; }
      finally {
        if (this.current(owner, c)) {
          try { const out = await this.call("status", target, digest); if (this.current(owner, c)) { this.status = out; this.error = null; } }
          catch (_e) { if (this.current(owner, c)) { this.status = null; this.error = "Exact association read-back unavailable. Outcome uncertain; job terminal state is not publication evidence."; } }
          // The read-back itself yields. Leaving/reopening can install a new
          // publication while it is pending; never clear that screen's state.
          if (this.current(owner, c)) { this.working = false; this.preview = null; this.confirm = false; cx.notify(); }
        }
      }
    }); cx.notify();
  }
  render(owner, cx, t, button) {
    const c = this.context, s = this.status, p = this.preview;
    const move = (delta, cx) => { p.page = Math.max(0, Math.min(p[p.mode].length - 1, p.page + delta)); if (p.mode === "fields") p.openedThrough = Math.max(p.openedThrough, p.page); cx.notify(); };
    const root = v_flex().id("ocq-result-publication").size_full().min_h(0).p(14).gap(8).bg(t.background).text_color(t.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => {
        if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
        if (e.key === "escape") { if (this.inputFocused) owner.handle.focus(); else if (this.confirm) this.confirm = false; else if (this.preview) this.preview = null; else this.reset(); cx.notify(); }
        else if (p && !this.confirm && !this.inputFocused && e.key === "left") move(-1, cx);
        else if (p && !this.confirm && !this.inputFocused && e.key === "right") move(1, cx);
        else return;
        cx.stop_propagation();
      });
    if (this.confirm && p) return root
      .child(div().font_semibold().child(c.capture ? "Publish this exact machine snapshot?" : "Publish this exact imported manifest?"))
      .child(v_flex().flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child(c.capture ? "Publishes the exact retained point-in-time machine_capture bytes with machine_verified_snapshot verification. The checkout may have changed since preparation. Does NOT recapture, assert a current match, run tests (validation=not_run), execute/reconcile an agent or authorize integration/commits. Records the association, seals the owned lane and sends work.submitted mail atomically." : "Imports claims with structure_hash_linkage_only verification. Does NOT run tests, machine-capture files, execute/reconcile an agent or authorize integration/commits. Records an immutable association, creates/seals the owned lane and automatically sends work.submitted or review.submitted mail in the same transaction."))
        .child(div().child(`Machine: local only\nSpace: ${c.space}\nProject: ${c.project}\nJob: ${c.job}\nKind/source: ${s.kind} / ${s.source}\nManifest SHA-256: ${this.digest}\nPayload SHA-256: ${s.payload_sha256}\nSupersedes association: ${s.supersedes_id || "none (no supersession)"}`))
        .child(div().text_size(12).text_color(t.muted).child("Exact supersedes lineage is immutable. One durable claim and one attempt; an already-launched call may finish after leaving. Missing read-back means uncertain, never permission to resend. Opening review pages records navigation, not comprehension.")))
      .when(!this.working && s?.state === "prepared", (e) => e.child(div().id("ocq-result-publish-once").role("button").py(8).px(8).cursor_pointer().hover((s) => s.bg(t.hover)).on_mouse_down("left", (_e, cx) => this.send(owner, cx)).child("Press to publish once")))
      .when(this.working, (e) => e.child(div().child("Claiming once and reading exact stored association…")))
      .when(!this.working, (e) => e.child(button("ocq-result-confirm-back", "Back without publishing", (cx) => { this.confirm = false; cx.notify(); })));
    if (p) {
      const pages = p[p.mode], page = pages[p.page];
      return root.child(div().font_semibold().child(`Private manifest review · ${p.mode === "fields" ? "field" : "raw"} ${p.page + 1}/${pages.length}`))
        .child(button("ocq-result-review-close", "Close private review", (cx) => { this.preview = null; cx.notify(); }))
        .child(v_flex().id(`ocq-result-page-${p.mode}-${p.page}`).flex_1().min_h(0).overflow_y_scroll().gap(10)
          .child(div().text_size(12).text_color(t.muted).child(c.capture ? "Controls/invisible Unicode are escaped. Retained cooperative point-in-time snapshot, NOT a current-tree match or test validation. Exclusions describe ignored/external inputs, not newly applied filesystem filters." : "Controls and invisible Unicode are escaped. All fields are untrusted imported claims, not verified tests or machine state."))
          .child(div().font_semibold().child(page.title)).child(div().child(page.text)))
        .child(h_flex().gap(8)
          .child(button("ocq-result-review-previous", "Previous · Left", (cx) => move(-1, cx)))
          .child(button("ocq-result-review-next", "Next · Right", (cx) => move(1, cx))))
        .when(p.mode === "fields" && p.page === pages.length - 1 && p.openedThrough === pages.length - 1 && s?.state === "prepared", (e) => e.child(button("ocq-result-review-effects", "Review publication effects…", (cx) => { this.confirm = true; cx.notify(); })));
    }
    const labels = { preparing: "Capture started, but no complete durable snapshot observed. Read-only recovery only; never automatically recapture.", prepared: c.capture ? "Exact machine snapshot retained, not published. Review paths/content/diff, exclusions and limitations. Later checkout changes will NOT replace these bytes." : "Private intent retained, not confirmed. Review every field before publishing.", armed: "Claim retained; an attempt may be delayed. Check only, never resend.", uncertain: "Publication outcome uncertain. Check exact association/content/lineage only; never resend.", changed: "Ownership or stored context changed. Publication disabled; inspect the exact stored state.", blocked: "Retained intent is corrupt or belongs to another context. No adoption or resend.", recorded_observed: c.capture ? "Exact retained machine_capture association, bytes and supersedes observed. Point-in-time machine_verified_snapshot only, not a current-tree match. validation=not_run; no test success or integration authorization." : "Exact association, manifest bytes and supersedes linkage observed. Caller causality is not established. Imported claims remain unverified; no test success or integration authorization." };
    return root.child(div().font_semibold().child(c.capture ? "Prepare / publish machine snapshot" : "Import / publish result manifest"))
      .child(button("ocq-result-back", "Back to stored job", (cx) => { this.reset(); cx.notify(); }))
      .child(v_flex().id("ocq-result-import-content").flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().text_size(12).text_color(t.muted).child(`Local project: ${c.project}\nSelected job: ${c.job}\n${c.capture ? "Preparation captures ONCE and retains privately, without publication. Explicit confirmation publishes only those point-in-time bytes, even if the checkout changes. No tests or integration authorization." : "Preparing/reviewing does not publish. No machine capture, test execution or integration authorization."}`))
        .when(c.capture && Boolean(this.scope), (e) => e.child(div().text_size(12).child(`Owned lane: ${this.scope?.lane_id}\nWorktree: ${this.scope?.worktree}\nPinned base: ${this.scope?.base}`)))
        .when(c.capture && Boolean(this.captureID), (e) => e.child(div().text_size(12).child(`Capture pointer: ${this.captureID}`)))
        .when(c.machine !== null, (e) => e.child(div().child("Remote publication is disabled. Use that machine's local UI.")))
        .when(this.working, (e) => e.child(div().child("Reading / retaining exact publication…")))
        .when(Boolean(this.error), (e) => e.child(div().text_color(t.ansi1).child(this.error)))
        .when(Boolean(this.digest), (e) => e.child(div().text_size(12).child(`Selected manifest SHA-256: ${this.digest}`)))
        .when(Boolean(s), (e) => e.child(div().child(labels[s?.state] || "No retained intent observed. This is not proof that a delayed preparation cannot finish.")))
        .when(Boolean(s?.association), (e) => e.child(div().text_size(12).child(`Association: ${s?.association?.association_id}\nManifest SHA-256: ${s?.association?.manifest_sha256}\nSupersedes: ${s?.association?.supersedes_id || "none"}\nRecorded: ${s?.association?.recorded_at}\nVerification: ${s?.association?.verification}`)))
        .when(c.machine === null && Boolean(this.scope) && !this.working && !this.digest && !this.uncertain && !this.captureID, (e) => e
          .when(c.capture, (e) => e.child(div().child("Ignored/external exclusions: one description per line; empty means none. These document existing capture exclusions, not filesystem filters.")))
          .child(Textarea.new(this.input).h(c.capture ? 150 : 60).id("ocq-result-input").accessibility_label(c.capture ? "Ignored exclusion descriptions, one per line" : "Path to retained result manifest file").px(8).border_1().border_color(t.border))
          .child(Input.new(this.supersedes).id("ocq-result-supersedes").accessibility_label("Exact supersedes association ID").px(8).border_1().border_color(t.border))
          .child(button("ocq-result-prepare", c.capture ? "Capture once and retain for private review…" : "Retain selected file for private review…", (cx) => this.prepare(owner, cx))))
        .when(!this.working && hex(s?.payload_sha256), (e) => e.child(button("ocq-result-review", "Review private retained manifest…", (cx) => this.review(owner, cx))))
        .when(!this.working && Boolean(c.capture ? this.captureID : this.digest) && Boolean(this.scope), (e) => e.child(button("ocq-result-check", "Check exact stored publication (read-only)", (cx) => this.check(owner, c.capture ? this.captureID : this.digest, cx))))
        .when(!this.working && c.machine === null, (e) => e.child(button("ocq-result-list", "Read retained publication intents", (cx) => this.load(owner, cx))))
        .children(this.entries.map((entry) => button(`ocq-result-retained-${c.capture ? entry.capture_id : entry.manifest_sha256}`, `Last listed: ${entry.state} · ${c.capture ? entry.capture_id : entry.manifest_sha256} · select to recheck`, (cx) => this.check(owner, c.capture ? entry.capture_id : entry.manifest_sha256, cx))))
        .when(!this.working && s?.state === "recorded_observed", (e) => e.child(button("ocq-result-new", c.capture ? "Prepare a new capture (new explicit intent)" : "Enter a different manifest (new explicit intent)", (cx) => { const job = c.job; this.open(owner, job, cx, c.capture); }))));
  }
}
