import { createHash } from "crypto";
import { bridgeCommand } from "./compatibility.js";

const hex = s => typeof s === "string" && /^[a-f0-9]{64}$/.test(s);
const fields = ["protocol", "socket", "device", "inode", "uid", "pid", "start_ticks", "boot_id", "host", "core_id", "space", "folder", "queue_identity"];

// Memory-only; the explicit Open intent can approve this native route for
// navigation, not execution, permissions, or reuse across connection changes.
export class PrivateHCBinding {
  constructor(host) { this.host = host; this.compatibility = host.compatibility; this.compatibility.hc = this; }
  context() {
    const h = this.host, u = h.updates, b = u.binding;
    if (h.closed || !h.panelCurrent() || !u.current() || !this.compatibility.machineReady() ||
        u.connection.state !== "connected" || !b) return null;
    return JSON.stringify([u.scope, b, u.connectionID, u.connectionGeneration, u.bindingEpoch,
      this.compatibility.panelMarker, this.compatibility.status?.image_sha256]);
  }
  approvedDigest(purpose) {
    return purpose === "navigation" && this.approved && this.approved === this.review &&
      this.approved.context === this.context() ? this.approved.value.binding_sha256 : null;
  }
  async load(context = this.context()) {
    if (!context) throw Error("Current terminal connection required");
    const r = await bridgeCommand(["hc-binding"], { on: this.host.machine, compatibility: this.compatibility });
    if (context !== this.context()) throw Error("Terminal connection changed");
    if (r.code !== 0 || r.stdout.length > 16384) throw Error("Terminal connection unavailable");
    const value = JSON.parse(r.stdout), b = value.binding, scope = this.host.updates.scope;
    if (!b || Object.keys(b).length !== fields.length || fields.some(k => !Object.hasOwn(b, k)) ||
        b.protocol !== "ocq-private-hc-v1" || b.space !== scope.localSpace || b.folder !== scope.folder ||
        b.queue_identity !== this.host.updates.binding.queue_identity || !hex(value.binding_sha256) ||
        value.validation !== "existing_socket_peer_and_core_verified" || value.launch !== false ||
        !["device", "inode", "uid", "pid"].every(k => Number.isSafeInteger(b[k]) && b[k] >= 0) ||
        b.pid < 1 || b.inode < 1 || !/^[1-9][0-9]*$/.test(b.start_ticks) ||
        !["socket", "boot_id", "host", "core_id"].every(k => typeof b[k] === "string" && b[k]) ||
        !b.socket.startsWith("/") || b.core_id !== this.host.updates.connection.executionCore)
      throw Error("Terminal identity changed");
    // Go's binding struct has a fixed field order and JSON HTML escaping.
    const bytes = JSON.stringify(Object.fromEntries(fields.map(k => [k, b[k]])))
      .replace(/[<>&\u2028\u2029]/g, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
    if (createHash("sha256").update(bytes).digest("hex") !== value.binding_sha256) throw Error("Terminal identity digest mismatch");
    this.review = { context, value };
    return this.review;
  }
}
