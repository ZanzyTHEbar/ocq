import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { createHash } from "crypto";
import { compatibility, bridgeCommand } from "./compatibility.js";

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
    return JSON.stringify([u.scope, b, u.connectionID, u.connectionGeneration, u.bindingEpoch, this.compatibility.status?.image_sha256]);
  }
  approvedDigest() {
    return this.approved && this.approved === this.review && this.approved.context === this.context()
      ? this.approved.value.binding_sha256 : null;
  }
  read(cx) {
    const context = this.context();
    if (!context || this.working) return;
    this.approved = null; this.review = null; this.working = true; this.error = null;
    cx.spawn(async cx => {
      try {
        await this.load(context);
      } catch (error) { if (context === this.context()) this.error = String(error.message || error); }
      finally { this.working = false; if (context === this.context()) cx.notify(); }
    }); cx.notify();
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
        this.review = { context, value, pages: this.host.workOperator.pagesFor(value), page: 0, seen: new Set() };
        return this.review;
  }
  render(cx, theme, button, box = v_flex().gap(6)) {
    const review = this.review, current = review && review.context === this.context();
    box.child(button("ocq-read-hc-binding", this.working ? "Reading private HC…" : "Read private HC binding", cx => this.read(cx)));
    if (this.error) box.child(div().text_color(theme.blocked).child(this.error));
    if (review && !current) box.child(div().text_color(theme.blocked).child("HC review expired. Read and review this connection again."));
    if (!current) return box;
    const b = review.value.binding;
    box.child(div().text_size(12).child(`HC ${b.core_id} · ${b.host}\nPID ${b.pid} · start ${b.start_ticks}`));
    if (this.approvedDigest()) return box.child(div().text_size(12).child("Private HC reviewed for this connection."));
    review.seen.add(review.page);
    box.child(div().child(`Private HC identity · ${review.page + 1}/${review.pages.length}`))
      .child(div().child(review.pages[review.page]))
      .child(h_flex().gap(4)
        .when(review.page > 0, e => e.child(button("ocq-read-hc-previous", "Previous identity page", cx => { review.page--; cx.notify(); })))
        .when(review.page < review.pages.length - 1, e => e.child(button("ocq-read-hc-next", "Next identity page", cx => { review.page++; cx.notify(); }))));
    if (review.seen.size === review.pages.length)
      box.child(button("ocq-read-hc-approve", "Use this exact HC binding", cx => {
        if (review === this.review && review.context === this.context() && review.seen.size === review.pages.length) this.approved = review;
        cx.notify();
      }));
    return box;
  }
}
