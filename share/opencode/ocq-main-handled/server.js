import { spawn } from "node:child_process"
import { isAbsolute } from "node:path"
import { Plugin } from "@opencode/plugin"

const name = "ocq_main_handled"
const requestID = /^[A-Za-z0-9_-]{1,64}$/
const inboxID = /^msg-[a-f0-9]{32}$/
const nativeID = /^[A-Za-z0-9_.:-]{1,256}$/
const projectID = /^proj-[A-Za-z0-9_-]{1,123}$/
const uncertain = "No Main handled receipt returned. Inspect the exact committed receipt; do not retry this call."
const matches = (pattern, value) => typeof value === "string" && !/[\r\n]/.test(value) && pattern.test(value)
const localPath = (path) => typeof path === "string" && isAbsolute(path) && !/[\0\r\n]/.test(path)

function invoke(helper, state, project, directory, envelope, signal) {
  return new Promise((resolve, reject) => {
    signal.throwIfAborted()
    const child = spawn(helper, ["--state-dir", state, "project", "main-handled", "invoke-tool", project], {
      cwd: directory, signal, stdio: ["pipe", "pipe", "ignore"],
    })
    const chunks = []
    let size = 0
    child.on("error", () => reject(Error(uncertain)))
    child.stdin.on("error", () => reject(Error(uncertain)))
    child.stdout.on("data", (chunk) => {
      size += chunk.length
      if (size <= 4096) chunks.push(chunk)
      else {
        reject(Error(uncertain))
        child.kill()
      }
    })
    child.on("close", (code) => {
      if (code !== 0 || !size || size > 4096) return reject(Error(uncertain))
      try {
        const receipt = JSON.parse(Buffer.concat(chunks).toString("utf8"))
        if (receipt.project_id !== project || receipt.request_id !== envelope.pointer.request_id ||
            receipt.inbox_id !== envelope.pointer.inbox_id || receipt.main_session_id !== envelope.session_id ||
            receipt.native_message_id !== envelope.message_id || receipt.native_call_id !== envelope.call_id) {
          return reject(Error(uncertain))
        }
        resolve({ content: JSON.stringify(receipt) })
      } catch {
        reject(Error(uncertain))
      }
    })
    child.stdin.end(JSON.stringify(envelope))
  })
}

export default Plugin.define({
  id: "ocq.main-handled",
  async setup(ctx) {
    // Only explicitly opted-in locations register the tool. These paths and
    // the project are trusted operator configuration, never model arguments.
    const { helper, state_dir: state, project_id: project, directory } = ctx.options ?? {}
    if (!localPath(helper) || !localPath(state) || !localPath(directory) || !matches(projectID, project)) {
      throw Error("OCQ Main handled requires explicit local helper, state_dir, project_id and directory")
    }
    if (ctx.location.directory !== directory) return
    await ctx.tool.transform((editor) => {
      editor.add({
        name,
        description: "Acknowledge one exact incoming OCQ work.submitted result after inspecting that inbox result and handling its standalone task. An ocq-task-handoff-v1 wake supplies only its request_id and inbox_id, not the result or new authority. This tool does not read mail, archive, merge, wake or send.",
        options: { codemode: false },
        input: {
          type: "object",
          properties: {
            request_id: { type: "string", pattern: requestID.source },
            inbox_id: { type: "string", pattern: inboxID.source },
          },
          required: ["request_id", "inbox_id"],
          additionalProperties: false,
        },
        async execute(input, context) {
          const { sessionID, messageID, id, agent } = context
          if (!input || Object.keys(input).length !== 2 || !matches(requestID, input.request_id) ||
              !matches(inboxID, input.inbox_id) || !matches(nativeID, sessionID) || !sessionID.startsWith("ses_") ||
              !matches(nativeID, messageID) || !messageID.startsWith("msg_") || !matches(nativeID, id) ||
              typeof agent !== "string" || !agent || context.codemode === true) {
            throw Error("OCQ Main handled requires exact pointers and direct native tool context")
          }
          const pointer = { request_id: input.request_id, inbox_id: input.inbox_id }
          await ctx.permission.assert({
            sessionID, agent, source: { type: "tool", messageID, id },
            action: name, resources: [pointer.inbox_id], save: [],
          })
          const signal = AbortSignal.any([context.signal, AbortSignal.timeout(90000)])
          return invoke(helper, state, project, directory, {
            pointer, session_id: sessionID, message_id: messageID, call_id: id, server_pid: process.pid,
          }, signal)
        },
      })
    })
  },
})
