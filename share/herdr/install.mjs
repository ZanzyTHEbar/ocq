// One-shot local installer, not a service or an alternate queue authority.
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

process.umask(0o077);
const [home, app, mode] = process.argv.slice(2), here = path.dirname(fileURLToPath(import.meta.url));
const hash = b => createHash("sha256").update(b).digest("hex");
const check = (v, message) => { if (!v) throw Error(message); };
const read = p => fs.readFileSync(p), json = p => JSON.parse(read(p));
const call = (...args) => execFileSync(app, args, { env: { ...process.env, HERDR_APP_HOME: home },
  encoding: "utf8", timeout: 30000, maxBuffer: 4 * 1024 * 1024 });
const api = (...args) => JSON.parse(call(...args));
const sync = p => { const fd = fs.openSync(p, "r"); try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); } };
const owned = (p, directory = false) => {
  const st = fs.lstatSync(p);
  check(!st.isSymbolicLink() && st.uid === process.getuid() && (directory ? st.isDirectory() : st.isFile()), `Unowned or unsafe path: ${p}`);
  return st;
};
const storeSnapshot = (p, expected = null) => {
  const fd = fs.openSync(p, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const st = fs.fstatSync(fd), raw = fs.readFileSync(fd);
    check(st.isFile() && st.uid === process.getuid() && (st.mode & 511) === 384, "Pointer store must be private and owned");
    const pin = { path: p, device: st.dev, inode: st.ino, uid: st.uid, mode: st.mode & 511, size: st.size, file_sha256: hash(raw) };
    if (expected) check(Object.keys(pin).every(k => pin[k] === expected[k]), "Pointer store identity/content changed; HOLD");
    return { pin, raw };
  } finally { fs.closeSync(fd); }
};
const refs = value => {
  if (!value || typeof value !== "object") return [];
  return Object.entries(value).flatMap(([k, v]) => k === "mod" && typeof v === "string" ? [v] : refs(v));
};
const find = (v, id) => v?.id === id ? v : (v?.children || []).map(c => find(c, id)).find(Boolean);
const tree = root => {
  owned(root, true);
  return fs.readdirSync(root).sort().flatMap(n => {
    const p = path.join(root, n), s = fs.lstatSync(p);
    return s.isDirectory() ? tree(p) : (owned(p), [{ path: p, sha256: hash(read(p)) }]);
  });
};
const navigation = s => [s.layout, s.space, s.tab, s.focused_pane, s.agent,
  (s.overlays || []).filter(o => o !== "toast"), s.floats];
const paneIDs = ls => ls.spaces.flatMap(s => s.tabs.flatMap(t => t.terminals.map(p => [s.id, t.id, p.pane, p.terminal])));
check(Number(process.versions.node.split(".")[0]) >= 18, "Node.js 18+ is required");
check(["--plan", "--activate"].includes(mode), "Expected --plan or --activate");
for (const p of [home, home + "/mods", home + "/layouts", home + "/data"]) owned(p, true);
const version = call("--version").trim();
const image = version === "herdr-app 0.1.18"
  ? "660a93c5b503619f9843de0ce54bda0e9050dd8a68ce0de4bb04af4df02b7b3d"
  : null;
const verifyImage = () => {
  if (image) check(hash(read(app)) === image, "Herdr 0.1.18 executable differs from the validated image");
};
check(image || /^herdr-app 0\.1\.(14|15|16)$/.test(version), "Unsupported Herdr version");
verifyImage();
const before = api("status"), beforeLS = api("ls");
check((before.overlays || []).every(o => o === "toast") && Object.keys(before.floats || {}).length === 0,
  "User overlay/float is open; preserve it and hold installation");

// Names alone never authorize retirement: require the OCQ source graph or its
// licensed chrome, and later refuse references from any retained layout/pane.
const modules = new Map();
for (const name of fs.readdirSync(home + "/mods").sort()) {
  if (!/^ocq(?:-|$)/.test(name)) continue;
  const dir = home + "/mods/" + name;
  owned(dir, true);
  if (!fs.existsSync(dir + "/mod.json") || !fs.existsSync(dir + "/main.js")) continue;
  const main = read(dir + "/main.js").toString();
  const runtime = fs.existsSync(dir + "/runtime-sources.js") ? read(dir + "/runtime-sources.js").toString() : "";
  const notice = fs.existsSync(dir + "/NOTICE") ? read(dir + "/NOTICE").toString() : "";
  const panel = runtime.includes('"ocq-runtime-v1"') && runtime.includes('"ocq-observe-v1"') ||
    main.includes('"./queue.js"') && main.includes("OCQ");
  const chrome = main.includes("toggleOCQ") && main.includes('"ocq-panel"') && notice.includes("herdrdev/herdr-app");
  if (panel || chrome) modules.set(name, { dir, files: tree(dir) });
}
// Historical inspection-only copies may intentionally lack the product graph.
// Retire those only with an explicit operator-supplied name + digest receipt.
const retirementReceipt = process.env.OCQ_RETIRE_MOD_RECEIPT;
if (retirementReceipt) {
  const entries = json(retirementReceipt);
  check(Array.isArray(entries), "Retirement receipt must be an array");
  for (const r of entries) {
    check(/^ocq-[A-Za-z0-9_-]+$/.test(r.name || "") && /^[0-9a-f]{64}$/.test(r.main_sha256 || ""), "Invalid retirement receipt");
    const dir = home + "/mods/" + r.name;
    owned(dir, true); owned(dir + "/main.js");
    check(hash(read(dir + "/main.js")) === r.main_sha256, `Retirement receipt differs: ${r.name}`);
    modules.set(r.name, { dir, files: tree(dir) });
  }
}
const layouts = [], retained = [];
for (const name of fs.readdirSync(home + "/layouts").sort().filter(n => n.endsWith(".json"))) {
  const file = home + "/layouts/" + name;
  owned(file);
  const bytes = read(file), value = JSON.parse(bytes);
  const panel = find(value, "ocq-panel"), tabs = find(value, "tabs");
  const entry = { name, file, sha256: hash(bytes), refs: refs(value) };
  if (panel && modules.has(panel.content?.mod) && modules.has(tabs?.content?.mod)) layouts.push(entry);
  else retained.push(entry);
}
check(!layouts.some(l => l.name === before.layout + ".json"), "Active OCQ layout: select a non-OCQ layout before upgrading; no active namespace is overwritten");
const live = JSON.stringify([beforeLS, before]);
for (const name of modules.keys())
  check(!retained.some(l => l.refs.includes(name)) && !live.includes(JSON.stringify(name)) &&
    !(before.overlays || []).includes(name), `OCQ module still referenced outside retired layouts: ${name}`);
for (const n of ["ocq", "ocq-chrome"])
  check(!fs.existsSync(home + "/mods/" + n) || modules.has(n), `Canonical namespace is not a recognized OCQ installation: ${n}`);
check(!retained.some(l => l.name === "ocq-workspace.json"), "Canonical layout name belongs to an unrecognized layout");
if (mode === "--plan") {
  fs.writeFileSync(1, JSON.stringify({ read_only: true, before, retire_layouts: layouts,
    retire_mods: [...modules].map(([name, m]) => ({ name, files: m.files })),
    preserve_layouts: retained, canonical_layout: "ocq-workspace", canonical_mods: ["ocq", "ocq-chrome"] }, null, 2) + "\n");
  process.exit(0);
}

// A private, durable backup is outside Herdr's visible layout/mod directories.
const backups = home + "/ocq-install-backups";
if (!fs.existsSync(backups)) fs.mkdirSync(backups, { mode: 0o700 });
owned(backups, true);
check((fs.statSync(backups).mode & 511) === 448, "Backup directory must be private (0700)");
const backup = fs.mkdtempSync(backups + "/install-");
check(["mods", "layouts"].every(n => fs.statSync(home + "/" + n).dev === fs.statSync(backup).dev),
  "UI assets/layouts and their backup must share a filesystem for exclusive retirement");
const save = (n, v) => { fs.writeFileSync(backup + "/" + n, JSON.stringify(v, null, 2) + "\n", { flag: "wx", mode: 0o600 }); sync(backup + "/" + n); sync(backup); };
save("intent.json", { before, layouts, modules: [...modules].map(([name, m]) => ({ name, files: m.files })),
  retained_layouts: retained, canonical: "ocq-workspace", no_queue_changes: true });
for (const dir of ["layouts", "mods", "new", "retired-layouts", "retired-mods"]) fs.mkdirSync(backup + "/" + dir, { mode: 0o700 });
for (const l of layouts) { fs.copyFileSync(l.file, backup + "/layouts/" + l.name, fs.constants.COPYFILE_EXCL); sync(backup + "/layouts/" + l.name); }
for (const [name, m] of modules) {
  fs.cpSync(m.dir, backup + "/mods/" + name, { recursive: true, errorOnExist: true, force: false });
  for (const f of m.files) { const p = backup + "/mods/" + name + f.path.slice(m.dir.length); check(hash(read(p)) === f.sha256, "Backup differs"); sync(p); }
  sync(backup + "/mods/" + name);
}
sync(backup + "/layouts"); sync(backup + "/mods"); sync(backup); sync(backups);

// The optional exact source is explicit, never a guessed "latest" namespace.
const pointerPin = process.env.OCQ_POINTER_PIN ? json(process.env.OCQ_POINTER_PIN) : null;
let pointer, targetPin;
const pointerKey = "operator-workflow-pointers-v1";
const verifyPointers = () => {
  if (!pointer) return;
  storeSnapshot(pointer.path, pointer);
  const target = storeSnapshot(targetPin.path, targetPin);
  check(JSON.parse(target.raw)[pointerKey] === pointer.value, "Canonical pointer readback differs");
};
if (pointerPin) {
  const sourceName = path.basename(path.dirname(pointerPin.path));
  check(/^[A-Za-z0-9_-]+$/.test(sourceName) && pointerPin.path === path.join(home, "data", sourceName, "store.json") &&
    /^[0-9a-f]{64}$/.test(pointerPin.value_sha256 || ""), "Invalid exact pointer pin");
  owned(home + "/data/" + sourceName, true);
  const { raw } = storeSnapshot(pointerPin.path, pointerPin), value = JSON.parse(raw)[pointerKey];
  check(typeof value === "string" && hash(value) === pointerPin.value_sha256, "Source pointer changed; HOLD without overwriting");
  pointer = { ...pointerPin, value };
  fs.writeFileSync(backup + "/pointer-source.snapshot.json", raw, { flag: "wx", mode: 0o600 }); sync(backup + "/pointer-source.snapshot.json");
  const target = home + "/data/ocq/store.json";
  if (fs.existsSync(home + "/data/ocq")) owned(home + "/data/ocq", true);
  if (fs.existsSync(target)) {
    const snapshot = storeSnapshot(target);
    targetPin = snapshot.pin;
    check(JSON.parse(snapshot.raw)[pointerKey] === value, "Canonical store differs; preserve it and HOLD");
  }
}

// Complete closed source graphs and actual native checks BEFORE retiring any
// visible entry. Only the two placement manifests differ from supplied assets.
const staged = new Map();
for (const name of ["ocq", "ocq-chrome"]) {
  const to = backup + "/new/" + name;
  fs.mkdirSync(to, { mode: 0o700 });
  for (const f of tree(here + "/mods/" + name)) {
    const relative = path.relative(here + "/mods/" + name, f.path);
    if (["gpui-kit.d.ts", "jsconfig.json"].includes(relative)) continue;
    fs.mkdirSync(path.dirname(to + "/" + relative), { recursive: true, mode: 0o700 });
    fs.copyFileSync(f.path, to + "/" + relative, fs.constants.COPYFILE_EXCL);
  }
  if (name === "ocq") fs.copyFileSync(path.resolve(here, "../../LICENSE"), to + "/LICENSE", fs.constants.COPYFILE_EXCL);
  fs.writeFileSync(to + "/mod.json", JSON.stringify({ ...json(to + "/mod.json"), place: "none" }, null, 2) + "\n");
  for (const f of tree(to).filter(f => f.path.endsWith(".js")))
    for (const m of read(f.path).toString().matchAll(/\bfrom\s+["'](\.[^"']+)["']/g))
      owned(path.resolve(path.dirname(f.path), m[1]));
  save(name + "-staged-check.json", { output: call("check", to) });
  staged.set(name, { path: to, files: tree(to).filter(f => !/\/(mod\.json|gpui-kit\.d\.ts|jsconfig\.json)$/.test(f.path)) });
}
const layoutBytes = read(here + "/layouts/ocq-workspace.json");
check(find(JSON.parse(layoutBytes), "ocq-panel")?.content?.mod === "ocq", "Invalid canonical layout");
fs.writeFileSync(backup + "/canonical-layout.json", layoutBytes, { flag: "wx", mode: 0o600 }); sync(backup + "/canonical-layout.json");
const current = api("status");
verifyImage();
check(JSON.stringify(navigation(current)) === JSON.stringify(navigation(before)), "User navigation changed; no retirement");
check(JSON.stringify(fs.readdirSync(home + "/layouts").filter(n => n.endsWith(".json")).sort()) ===
  JSON.stringify([...layouts, ...retained].map(l => l.name).sort()), "Layout inventory changed; HOLD");
for (const l of [...layouts, ...retained]) check(hash(read(l.file)) === l.sha256, "Layout changed; HOLD");
for (const m of modules.values()) check(JSON.stringify(tree(m.dir)) === JSON.stringify(m.files), "Old assets changed; HOLD");
if (pointer) {
  const source = storeSnapshot(pointer.path, pointer);
  const data = home + "/data/ocq", target = data + "/store.json";
  if (!fs.existsSync(data)) fs.mkdirSync(data, { mode: 0o700 });
  owned(data, true);
  if (!targetPin) {
    check(!fs.existsSync(target), "Canonical store appeared since preflight; HOLD");
    const store = JSON.parse(source.raw);
    check(Object.keys(store).length === 1, "Source has unrelated store keys; preserve it and HOLD");
    fs.writeFileSync(target, source.raw, { flag: "wx", mode: 0o600 });
    sync(target); sync(data);
    const snapshot = storeSnapshot(target);
    check(snapshot.pin.file_sha256 === pointer.file_sha256, "Exact new store readback differs");
    targetPin = snapshot.pin;
  }
  verifyPointers();
  save("pointer-carried.json", { source: pointerPin, target: targetPin, value_sha256: pointer.value_sha256, nested_value_reserialized: false });
}

// Explicit policy: ALL proven old OCQ layout entries leave the visible list
// BEFORE the canonical replacement is published. Data directories never move.
save("retire-intent.json", { layouts: layouts.map(l => l.file), mods: [...modules.keys()] });
for (const l of layouts) fs.renameSync(l.file, backup + "/retired-layouts/" + l.name);
sync(home + "/layouts");
for (const [name, m] of modules) fs.renameSync(m.dir, backup + "/retired-mods/" + name);
sync(home + "/mods");
for (const [name, s] of staged) {
  const seed = name + "-install-seed";
  check(!fs.existsSync(home + "/mods/" + seed) && !fs.existsSync(home + "/mods/" + name), "Publication target exists; no retry");
  fs.renameSync(s.path, home + "/mods/" + seed); sync(home + "/mods");
  save(name + "-clone-intent.json", { from: seed, to: name });
  save(name + "-clone-reply.json", api("do", "mod.clone", "--name", seed, "--as", name));
  for (const f of s.files) check(hash(read(home + "/mods/" + name + f.path.slice(s.path.length))) === f.sha256, "Cloned assets differ");
  check(json(home + "/mods/" + name + "/mod.json").place === "none", "Clone would auto-place");
  save(name + "-native-check.json", { output: call("check", home + "/mods/" + name) });
  fs.renameSync(home + "/mods/" + seed, backup + "/retired-mods/" + seed); sync(home + "/mods");
}
verifyPointers();
const available = home + "/layouts/ocq-workspace.json";
fs.linkSync(backup + "/canonical-layout.json", available); sync(home + "/layouts");
check(JSON.stringify(navigation(api("status"))) === JSON.stringify(navigation(before)), "User navigation changed; canonical installed but not activated");
save("switch-intent.json", { from: before.layout, to: "ocq-workspace" });
save("switch-reply.json", api("do", "layout.switch", "--name", "ocq-workspace"));
const after = api("status"), afterLS = api("ls");
save("after-status.json", after);
check(after.layout === "ocq-workspace", "Switch outcome differs; no automatic replay/rollback");
const ids = paneIDs(afterLS);
check(paneIDs(beforeLS).every(a => ids.some(b => JSON.stringify(a) === JSON.stringify(b))), "Prior pane identity missing");
check(["space", "tab", "focused_pane", "agent"].every(k => before[k] === after[k]), "User navigation changed during switch; no guessed restoration");
verifyPointers();
check(fs.readdirSync(home + "/layouts").filter(n => n.endsWith(".json")).every(n =>
  n === "ocq-workspace.json" || !refs(json(home + "/layouts/" + n)).some(r => modules.has(r) || ["ocq", "ocq-chrome"].includes(r))), "Old OCQ layout still visible");
save("result.json", { installed: true, activated: true, layout: "ocq-workspace", visible_ocq_layout_count: 1,
  canonical_mods: ["ocq", "ocq-chrome"], retired_layouts: layouts.map(l => l.name), retired_mods: [...modules.keys()],
  installed_manifest: ["ocq", "ocq-chrome"].flatMap(n => tree(home + "/mods/" + n)), backup,
  pointer_value_sha256: pointer?.value_sha256, navigation_preserved: true, no_queue_changes: true });
console.log(`Installed and activated ocq-workspace. Previous OCQ installs retained privately in ${backup}`);
