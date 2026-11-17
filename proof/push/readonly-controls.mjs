// Focused production-controller -> real default read-only socket acceptance.
// Host selection/render bindings are supplied without starting another GUI.
// The compatibility module, command transport, bridge and SQLite are real.
import fs from "node:fs";
import vm from "node:vm";

const [configPath, modulePath, outputPath] = process.argv.slice(2);
const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
const check = (ok, message) => { if (!ok) throw Error(message); };
const socket = new WebSocket(config.endpoint);
await new Promise((resolve, reject) => {
  socket.addEventListener("open", resolve, { once: true });
  socket.addEventListener("error", reject, { once: true });
});
let welcome;
let welcomed;
const welcomeReady = new Promise(resolve => { welcomed = resolve; });
let serial = 0;
const pending = new Map();
socket.addEventListener("message", event => {
  const frame = JSON.parse(event.data);
  if (frame.type === "welcome") { welcome = frame; welcomed(); return; }
  if (frame.type === "command-result") {
    check(frame.connection_id === welcome.connection_id, "wrong command connection");
    const p = pending.get(frame.id);
    check(p, "unowned command response");
    pending.delete(frame.id); p.resolve(frame.result);
  }
});
socket.addEventListener("close", () => {
  for (const p of pending.values()) p.reject(Error("read endpoint closed"));
  pending.clear();
});
socket.send(JSON.stringify({ type: "auth", protocol: "ocq-push-v1", token: fs.readFileSync(config.token_file, "utf8").trim(), space: config.space, folder: config.folder }));
await welcomeReady;
check(welcome.mutations === false, "must exercise default read-only endpoint");
const command = args => new Promise((resolve, reject) => {
  const id = String(++serial);
  pending.set(id, { resolve, reject });
  socket.send(JSON.stringify({ type: "command", id, request: { connection_id: welcome.connection_id, args } }));
});
const selected = { id: config.space, folder: config.folder, machine: null, active: true };
const context = vm.createContext({});
const unusedRender = () => { throw Error("No renderer used by this focused flow"); };
const host = (exports, identifier) => new vm.SyntheticModule(Object.keys(exports), function () {
  for (const [key, value] of Object.entries(exports)) this.setExport(key, value);
}, { context, identifier });
const modules = new Map([
  ["gpui-kit", host({ div: unusedRender }, "gpui-kit")],
  ["gpui-base", host({ v_flex: unusedRender }, "gpui-base")],
  ["herdr", host({ spaces: () => [selected] }, "herdr")],
  ["./panel-state.js", host({ panelEpoch: () => 1, panelVisible: () => true }, "panel-state")],
]);
const module = new vm.SourceTextModule(fs.readFileSync(modulePath, "utf8"), { context, identifier: modulePath });
await module.link(name => {
  check(modules.has(name), "unexpected compatibility dependency");
  return modules.get(name);
});
await module.evaluate();
const { compatibility, enabledButton, exec } = module.namespace;
compatibility.observe();
compatibility.status = compatibility.validate(welcome.descriptor, selected);
compatibility.allowActions = welcome.mutations;
compatibility.pushConnection = { state: "connected", updates: { current: () => true }, command };
check(!compatibility.ready() && !compatibility.workReady(), "no mutation readiness");
const readControls = ["ocq-work-status", "ocq-work-recover", "ocq-work-output", "ocq-work-handoff", "ocq-inspection-check-mail", "ocq-result-check-assoc-real-selection", "ocq-agent-handoff-verify"];
check(readControls.every(enabledButton), "read controls require no mutation opt-in");
check(!enabledButton("ocq-work-preview") && !enabledButton("ocq-agent-reconcile-once"), "preview/live attempt remains gated");
const d = welcome.descriptor;
const base = [config.space, d.queue_identity, config.project, "socket-task"];
const status = await exec("ocq-herdr", ["work-status", "--", ...base], { on: null });
check(status.code === 0 && JSON.parse(status.stdout).state === "prepared", "actual stored work status");
// Retained uncertainty does not acquire mutation authority on reconnect.
compatibility.invalidateObservation("Retained uncertain operation; explicit read-only reconnect");
check(enabledButton("ocq-work-recover"), "uncertain recovery control remains available");
const recovery = await exec("ocq-herdr", ["work-recover", "--", ...base], { on: null });
check(recovery.code === 0 && JSON.parse(recovery.stdout).state === "prepared", "actual exact read-only recovery");
let mainRecoveryState = null;
if (config.prepared_main) {
  check(enabledButton("ocq-main-recover"), "Main recovery independent of mutation readiness");
  const main = await exec("ocq-herdr", ["main-recover", "--", config.space, d.queue_identity, "proj-socket-main-journal", "socket-main-prepared-only"], { on: null });
  check(main.code === 0, "explicit GET-only Main recovery available on default read-only endpoint");
  mainRecoveryState = JSON.parse(main.stdout).state;
}
const beforeMutation = serial;
let refused = false;
try { await exec("ocq-herdr", ["agent-reconcile-prepare", config.space, d.queue_identity], { on: null, current: () => true }); }
catch (_) { refused = true; }
check(refused && serial === beforeMutation, "mutating preparation refused before transport");
const preview = await command(["work-preview", "--work-observation-context", d.observation_context, "--", ...base, "local", config.folder, d.observation_store_context, d.image_sha256]);
check(preview.code !== 0, "server also refuses private preview on read-only endpoint");
const closed = new Promise(resolve => socket.addEventListener("close", resolve, { once: true }));
socket.close();
await closed;
fs.writeFileSync(outputPath, JSON.stringify({
  status: "pass", actual_production_module: modulePath, endpoint_mutations: false,
  read_controls: readControls, actual_work_status: "prepared", actual_uncertain_recovery: "prepared",
  actual_main_recovery_state: mainRecoveryState,
  mutation_not_sent: true, server_preview_refused: true,
  validation: "focused controller/real socket/core flow; host selection bindings only, not native render acceptance",
}, null, 2) + "\n", { mode: 0o600, flag: "wx" });
