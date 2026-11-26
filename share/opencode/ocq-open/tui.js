import { Plugin } from "@opencode/plugin/tui";
import { execFile } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { closeSync, constants, fstatSync, openSync, readFileSync, realpathSync } from "node:fs";
import { createServer } from "node:net";
import { isAbsolute } from "node:path";

const localPath = value => typeof value === "string" && isAbsolute(value) && !/[\r\n\0]/.test(value);
const routeKey = route => JSON.stringify([route?.type, route?.sessionID, route?.name]);
const nonce = () => randomBytes(16).toString("hex");
const foreground = birth => {
  try {
    const stat = readFileSync("/proc/self/stat", "utf8");
    const fields = stat.slice(stat.lastIndexOf(") ") + 2).trim().split(/\s+/);
    if (fields[4] !== "0" && fields[2] === String(process.pid) && fields[2] === fields[5] &&
        (!birth || birth === fields[19])) return fields[19];
  } catch (_) { /* not a usable local foreground terminal */ }
};

// Match the Go digests without exporting ownership metadata from the TUI.
function digest(value) {
  function sorted(item) {
    if (Array.isArray(item)) return item.map(sorted);
    if (item && typeof item === "object")
      return Object.fromEntries(Object.keys(item).sort().map(key => [key, sorted(item[key])]));
    if (typeof item === "number" && (!Number.isFinite(item) || Number.isInteger(item) && !Number.isSafeInteger(item)))
      throw Error("Unsupported identity number");
    if (item !== null && !["string", "boolean", "number"].includes(typeof item)) throw Error("Incomplete identity");
    return item;
  }
  const json = JSON.stringify(sorted(value)).replace(/[<>&\u2028\u2029]/g,
    c => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
  return createHash("sha256").update(json).digest("hex");
}

function resolveTarget(helper, state, serviceState, raw, signal) {
  return new Promise((resolve, reject) => {
    const child = execFile(helper, ["--state-dir", state, "project", "client", "resolve-tui", "--", serviceState],
      { signal, timeout: 3500, maxBuffer: 8192 }, (error, stdout) => {
        if (error) return reject(error);
        try { resolve(JSON.parse(stdout)); } catch (error) { reject(error); }
      });
    child.stdin.on("error", reject);
    child.stdin.end(raw);
  });
}

export default Plugin.define({
  id: "ocq.open",
  setup(context) {
    const { helper, state_dir: state, opencode_state_dir: serviceState } = context.options ?? {};
    const { router } = context.ui;
    const runtime = process.env.XDG_RUNTIME_DIR, terminal = process.env.HC_TERMINAL_ID;
    const birth = foreground();
    // Explicit endpoints and private servers are not the ordinary managed
    // connection. A remote endpoint's self-reported /api/info isn't authority.
    const explicitServer = process.argv.some(arg => /^--(?:server|standalone)(?:=|$)/.test(arg));
    if (process.platform !== "linux" || ![helper, state, serviceState, runtime].every(localPath) ||
        explicitServer ||
        !/^t[1-9][0-9bcdfghjkmnpqrstvwxyz]{0,30}$/.test(terminal ?? "") || !/^\d+$/.test(birth ?? "") ||
        typeof router?.current !== "function" || typeof router?.navigate !== "function") return;
    let fd;
    try {
      if (realpathSync(runtime) !== runtime) return;
      fd = openSync(runtime, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
      const dir = fstatSync(fd);
      if (!dir.isDirectory() || dir.uid !== process.getuid() || (dir.mode & 0o777) !== 0o700) {
        closeSync(fd); return;
      }
    } catch (_) { if (fd !== undefined) closeSync(fd); return; }
    const clientID = nonce(), sockets = new Set();
    let busy = false;
    // The socket is reachable only through this user's private runtime dir.
    // Each connection receives one fresh challenge and may carry one request.
    const server = createServer(socket => {
      socket.on("error", () => {});
      if (busy || sockets.size >= 2 || !foreground(birth)) { socket.destroy(); return; }
      const lifetime = new AbortController();
      const deadline = setTimeout(() => socket.destroy(), 4500);
      sockets.add(socket);
      socket.once("close", () => { clearTimeout(deadline); lifetime.abort(); sockets.delete(socket); });
      const challenge = nonce();
      const identity = { challenge, client: clientID, terminal };
      socket.write(JSON.stringify(identity) + "\n");
      let input = "";
      socket.setEncoding("utf8");
      socket.on("data", async chunk => {
        input += chunk;
        if (Buffer.byteLength(input) > 7000) { socket.destroy(); return; }
        const line = input.indexOf("\n");
        if (line < 0) return;
        socket.removeAllListeners("data");
        if (busy || line !== input.length - 1) { socket.destroy(); return; }
        busy = true;
        try {
          const request = JSON.parse(input);
          if (request.challenge !== challenge || request.client !== clientID || request.terminal !== terminal ||
              !["check", "open"].includes(request.mode) || !localPath(request.cwd)) return;
          const client = context.client, origin = routeKey(router.current());
          const current = () => !lifetime.signal.aborted && foreground(birth) && context.client === client &&
            process.cwd() === request.cwd && context.data.location.default()?.directory === request.cwd;
          if (!current()) return;
          const raw = JSON.stringify(request.target);
          if (Buffer.byteLength(raw) > 2048) return;
          const signal = lifetime.signal;
          const target = await resolveTarget(helper, state, serviceState, raw, signal);
          const { session, kind } = request.target;
          if (!current() || JSON.stringify(target.pointer) !== raw) return;
          const info = await client.server.info({ signal });
          if (!current() || info.pid !== target.server_pid || info.version !== target.server_version ||
              info.hostname !== target.server_hostname || !info.urls?.includes(target.server_origin)) return;
          const saved = await client.session.get({ sessionID: session }, { signal });
          if (!current() || saved?.id !== session || saved.parentID || saved.projectID !== target.opencode_project_id ||
              saved.location?.directory !== target.directory || digest(saved.metadata) !== target.ownership_sha256 ||
              kind === "worker" && digest({ agent: saved.agent, model: saved.model, permissions: saved.permissions }) !== target.profile_sha256)
            return;
          if (request.mode === "open") {
            if (routeKey(router.current()) !== origin) return;
            const before = router.current();
            if (before?.type !== "session" || before.sessionID !== session)
              router.navigate({ type: "session", sessionID: session });
            const after = router.current();
            if (after?.type !== "session" || after.sessionID !== session) return;
          }
          socket.end(JSON.stringify({ ...identity, session, outcome: request.mode === "open" ? "routed" : "ready" }) + "\n");
        } catch (_) { /* refuse without changing the route or disclosing details */ }
        finally {
          busy = false;
          if (!socket.writableEnded) socket.end();
        }
      });
    });
    let closed = false;
    const stop = () => {
      if (closed) return;
      closed = true;
      for (const socket of sockets) socket.destroy();
      server.close(() => closeSync(fd));
    };
    server.on("error", stop);
    // The directory FD pins the private directory and keeps long XDG paths
    // from overflowing sockaddr_un. Bun's Node API unlinks on close.
    server.listen(`/proc/self/fd/${fd}/ocq-tui-${process.pid}-${birth}`);
    return stop;
  },
});
