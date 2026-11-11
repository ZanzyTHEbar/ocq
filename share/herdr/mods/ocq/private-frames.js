import { createHash } from "crypto";
import { Buffer } from "buffer";

// The existing compose/intake mutual-auth protocol and exact UTF-8 framing.
// Payloads never use argv, bridge stdout or browser storage.
export function composeProof(capability, nonce, role) {
  const key = Buffer.from(capability, "hex"), inner = new Uint8Array(64), outer = new Uint8Array(64);
  for (let i = 0; i < 64; i++) { const b = i < key.length ? key[i] : 0; inner[i] = b ^ 0x36; outer[i] = b ^ 0x5c; }
  const digest = createHash("sha256").update(inner).update(`ocq-compose-v1:${role}:${nonce}`).digest();
  return createHash("sha256").update(outer).update(digest).digest("hex");
}
export function composeEqual(a, b) {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return difference === 0;
}
export function composeDecode(bytes) {
  const text = Buffer.from(bytes).toString("utf8"), encoded = Buffer.from(text, "utf8");
  if (encoded.length !== bytes.length) throw new Error("Invalid UTF-8 frame");
  for (let i = 0; i < bytes.length; i++) if (encoded[i] !== bytes[i]) throw new Error("Invalid UTF-8 frame");
  return text;
}
export function socketFrames(socket, maximum = 131072) {
  if (!Number.isInteger(maximum) || maximum < 1 || maximum > 1048576) throw new Error("Invalid frame bound");
  let pending = [];
  async function more() {
    const chunk = await socket.read(4096);
    if (chunk === null || chunk.length === 0) throw new Error("Connection closed");
    for (const byte of chunk) pending.push(byte);
    if (pending.length > maximum + 4096) throw new Error("Frame too large");
  }
  return {
    async line(limit = 131072) {
      for (;;) {
        const end = pending.indexOf(10);
        if (end >= 0) { if (end > limit) throw new Error("Frame too large"); const frame = pending.slice(0, end); pending = pending.slice(end + 1); return composeDecode(frame); }
        if (pending.length > limit) throw new Error("Frame too large");
        await more();
      }
    },
    async bytes(count) {
      if (!Number.isInteger(count) || count < 0 || count > maximum) throw new Error("Invalid frame size");
      while (pending.length < count) await more();
      const frame = pending.slice(0, count); pending = pending.slice(count); return frame;
    },
  };
}
