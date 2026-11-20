import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { createHash } from "crypto";
import { compatibility, bridgeCommand } from "./compatibility.js";
import { technicalDetails } from "./action-review.js";

const hex = s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s);
const fields = ["protocol", "socket", "device", "inode", "uid", "pid", "start_ticks", "boot_id", "host", "core_id", "space", "folder", "queue_identity"];

// Human-reviewed endpoint identity, not discovery or permission approval.
// Memory-only: reconnect/panel/scope changes cannot inherit this review.
export class PrivateHCBinding {
  constructor(host) { this.host = host; this.compatibility = host.compatibility || compatibility; this.compatibility.hc = this; }
  context() {
    const h = this.host, u = h.updates, b = u.binding;
    if (h.closed || !h.panelCurrent() || !u.current() || !this.compatibility.machineReady() ||
        u.connection.state !== "connected" || !b) return null;
    return JSON.stringify([u.scope, b, u.connectionID, u.connectionGeneration, u.bindingEpoch,
      this.compatibility.panelMarker, this.compatibility.status?.image_sha256]);
  }
  approvedDigest() {
    return this.approved && this.approved === this.review && this.approved.context === this.context()
      ? this.approved.value.binding_sha256 : null;
  }
  read(cx, onApproved = null) {
    const context = this.context();
    if (!context) { this.error = "Verify the owning project connection before reviewing its terminal service."; cx.notify(); return false; }
    if (this.working) return false;
    this.approved = null; this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        const review = await this.load(context);
        review.onApproved = onApproved;
      } catch (error) { if (context === this.context()) this.error = String(error.message || error); }
      finally { this.working = false; if (context === this.context()) cx.notify(); }
    }, { owner: this.host.root || this.host }); cx.notify(); return true;
  }
  async load(context = this.context()) {
        if (!context) throw Error("Current native backend context required");
        const r = await bridgeCommand(["hc-binding"], { on: this.host.machine, compatibility: this.compatibility });
        if (context !== this.context()) throw Error("Native backend context changed");
        if (r.code !== 0 || r.stdout.length > 16384) throw Error("No verified private HC binding at this endpoint.");
        const value = JSON.parse(r.stdout), b = value.binding, scope = this.host.updates.scope;
        if (!b || Object.keys(b).length !== fields.length || fields.some(k => !Object.hasOwn(b, k)) ||
            b.protocol !== "ocq-private-hc-v1" || b.space !== scope.localSpace || b.folder !== scope.folder ||
            b.queue_identity !== this.host.updates.binding.queue_identity || !hex(value.binding_sha256) ||
            value.validation !== "existing_socket_peer_and_core_verified" || value.launch !== false ||
            !["device", "inode", "uid", "pid"].every(k => Number.isSafeInteger(b[k]) && b[k] >= 0) ||
            b.pid < 1 || b.inode < 1 || !/^[1-9][0-9]*$/.test(b.start_ticks) ||
            !["socket", "boot_id", "host", "core_id"].every(k => typeof b[k] === "string" && b[k]) ||
             !b.socket.startsWith("/")) throw Error("Private HC identity/context mismatch.");
        if (scope.machine !== null && b.core_id !== this.host.updates.connection.executionCore)
          throw Error("Selected-machine HC core changed.");
        // Go's binding struct has a fixed field order and JSON HTML escaping.
        const bytes = JSON.stringify(Object.fromEntries(fields.map(k => [k, b[k]])))
          .replace(/[<>&\u2028\u2029]/g, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
        if (createHash("sha256").update(bytes).digest("hex") !== value.binding_sha256) throw Error("HC binding digest mismatch.");
        this.review = { context, value, pages: this.host.workOperator.pagesFor(value), page: 0 };
        return this.review;
  }
  render(cx, theme, button, box = v_flex().w_full().min_w(0).gap(6)) {
    const context = this.context(), review = this.review, current = review && review.context === context;
    if (!context) {
      box.child(div().text_color(theme.muted).child("Verify this project's connection before reviewing its terminal service. Saved work and drafts are retained."));
      if (this.host.renewObservation) box.child(button("ocq-read-project-connection", "Verify project connection", cx => this.host.renewObservation(cx)));
      return box;
    }
    if (!current) box.child(button("ocq-read-hc-binding", this.working ? "Verifying terminal service…" : "Review terminal service", cx => this.read(cx)));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (review && !current) box.child(div().text_color(theme.muted).child("Review the terminal service for this connection again. Prior navigation approval is no longer valid."));
    if (!current) return box;
    const b = review.value.binding;
    box.child(div().text_size(12).child(`Open conversations on ${this.host.machine ?? "this computer"}\nRepository: ${b.folder}`));
    if (this.approvedDigest()) return box.child(div().text_size(12).child("Private HC reviewed for this connection."));
    box.child(div().child("Use the verified terminal service on this machine. This approves navigation only, not permissions."))
      .child(button("ocq-read-hc-approve", "Use this terminal service", cx => {
        if (review === this.review && review.context === this.context()) {
          this.approved = review;
          const next = review.onApproved; review.onApproved = null;
          next?.(cx);
        }
        cx.notify();
      }))
      .child(technicalDetails(review, button, "ocq-read-hc"));
    return box;
  }
}
