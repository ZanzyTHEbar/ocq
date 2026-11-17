import { Buffer } from "buffer";

// Decode exact UTF-8 bytes; reject lossy replacement without normalizing text.
export function composeDecode(bytes) {
  const text = Buffer.from(bytes).toString("utf8"), encoded = Buffer.from(text, "utf8");
  if (encoded.length !== bytes.length) throw new Error("Invalid UTF-8 frame");
  for (let i = 0; i < bytes.length; i++) if (encoded[i] !== bytes[i]) throw new Error("Invalid UTF-8 frame");
  return text;
}
