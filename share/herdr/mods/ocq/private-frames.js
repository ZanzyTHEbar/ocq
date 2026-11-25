import { Buffer } from "buffer";
import { readFile } from "fs/promises";

// Explicit local-file intake only. The original UTF-8 bytes still go through
// the existing core decoder, duplicate-key checks and retained-byte review.
export async function readInputFile(path, limit = 131072) {
  if (typeof path !== "string" || !path.startsWith("/") || /[\r\n\0]/.test(path))
    throw Error("Select an absolute local file path.");
  const bytes = await readFile(path);
  if (!bytes.length || bytes.length > limit) throw Error("The selected file is empty or exceeds this intake's size limit.");
  return composeDecode(bytes);
}

// Decode exact UTF-8 bytes; reject lossy replacement without normalizing text.
export function composeDecode(bytes) {
  const text = Buffer.from(bytes).toString("utf8"), encoded = Buffer.from(text, "utf8");
  if (encoded.length !== bytes.length) throw new Error("Invalid UTF-8 frame");
  for (let i = 0; i < bytes.length; i++) if (encoded[i] !== bytes[i]) throw new Error("Invalid UTF-8 frame");
  return text;
}
