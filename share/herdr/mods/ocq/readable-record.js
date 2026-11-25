// Presentation only. Never use these rows as request bytes, hashes or authority.
// Domain fields are explicit: an unknown record has no raw-data fallback.
const groups = [
  ["Identity", "protocol version kind key project_id project_ids label id job_id request_id navigation_id operation_id setup_id lane_id owner_job_id session_id prompt_id sequence sequence_text opencode_project_id main_id returned_terminal_id observed_terminal_id peer"],
  ["Progress", "state status outcome reason attention guidance terminal_proven terminal_state current_route attempt_permitted result_status validation verification source claim_label verdict integration worktree_match freshness association_count integrator_unread_mail_count output_status exit_code dirty head_advanced launch ownership_mode"],
  ["Context", "machine host space space_id folder repo repo_root cwd worktree recorded_worktree recorded_session_id branch main_branch head base base_ref base_commit commit_anchor binding_revision revision target_kind work_request_id parent_request_id fsfork_driver fsfork_path"],
  ["Time", "read_at observed_at observation_time created_at accepted_at recorded_at started_at finished_at completed_at archived_at"],
  ["Handoff", "subject body text from to intent goal prompt brief scope exclusions acceptance_criteria allowed_actions context_references decisions risks notes findings location severity message limitations criteria rules target_job_id review_job_id review_session_id readiness integration_authorized"],
  ["Evidence", "manifest_digest manifest_sha256 digest sha256 intent_sha256 preview_sha256 pointer_sha256 payload_sha256 binding_sha256 queue_identity image_sha256 ownership_sha256 request_digest reviewed_result_digest target_result_digest result_digest before_status_digest after_status_digest before_content_digest after_content_digest evidence_digests supersedes_id association_id assoc_id completion_event_id event_id brief_digest sealed_manifest_digest service_identity profile_sha256 launcher_sha256 opencode_sha256 asset_sha256 overlay_sha256 store_context bridge_context observation_context observation_store_context"],
  ["Files and validation", "path old_mode new_mode contents changed_files untracked_paths staged_status unstaged_status diff inline_output ignored_exclusions commands results provenance claimed_validation"],
  ["Profile and service", "agent provider_id providerID model_id variant provision_policy memory_bytes action resource effect socket device inode uid pid start_ticks boot_id core_id server_origin cwd launcher_argv argv executable arguments auth_ref"],
  ["Recorded observation", "phase server_outcome assertion verified stale_for_latest_result stale_for_current_worktree review_association_id review_manifest_digest latest_result_digest resolution_evidence stdout stderr terminal_evidence completion_policy tool_policy tool_count tool_error_count auxiliary_event_count auxiliary_provenance active stable_transcript pending_permissions pending_forms"],
];
const containers = new Set(("project current_binding binding status job stored_job job_detail event completion_event result association associations lane lanes jobs results inbox message manifest handoff_manifest brief_manifest referenced_manifests request input_json session_json selection profile implementation_profile review_profile role client pointer terminal hc_binding dispatcher target scope ownership snapshot before after model permissions attention receipt observation terminal_receipt counts request_status context options execution review current_job result_summary process scheduling implementation capture review_result implementation_manifest review_manifest implementation_output review_output worktree resources_before resources_final prior_diagnostic").split(" "));
const labels = new Map(groups.flatMap(([group, keys]) => keys.split(" ").map(key => [key, [group, key.replaceAll("_", " ")]])));
const hiddenBytes = new Set(["canonical", "raw", "input_json", "session_json", "registration_json", "prompt_json", "payload", "profile", "implementation_profile", "review_profile"]);
const supportedKinds = new Set(["handoff", "brief", "review_brief", "result", "review_result", "command", "agent", "implementation", "review", "registration", "submission", "lane.assigned", "lane.blocked", "work.submitted", "review.submitted"]);
const summaryOrder = ["label", "subject", "kind", "state", "status", "outcome", "reason", "job", "execution",
  "result_status", "result", "validation", "review", "verdict", "findings", "limitations", "integration",
  "project_id", "job_id", "session_id", "machine", "repo", "worktree", "base", "read_at"];

function clean(value) {
  return String(value).replace(/[\p{Cc}\p{Default_Ignorable_Code_Point}\p{Zl}\p{Zp}]/gu,
    c => c === "\n" || c === "\t" ? c : `〈Unicode ${c.codePointAt(0).toString(16).toUpperCase()}〉`);
}

export function readableRows(value, title = "Record", depth = 0) {
  if (depth > 16) return [{ title, text: "Record nesting exceeds this view's supported depth. Exact evidence remains retained." }];
  if (typeof value === "string") {
    try {
      const parsed = JSON.parse(value);
      // Decode embedded records and quoted strings before presentation.
      if (parsed !== value) return readableRows(parsed, title, depth + 1);
    } catch (_) { /* Ordinary text, not a serialized record. */ }
    if (/^\s*[\[{]/.test(value)) return [{ title, text: "Cannot display this record type. Exact evidence remains retained; use Copy UI state for context." }];
    return [{ title, text: clean(value) || "Empty" }];
  }
  if (value == null) return [{ title, text: "Not recorded" }];
  if (typeof value === "boolean") return [{ title, text: value ? "Yes" : "No" }];
  if (typeof value === "number") return [{ title, text: Number.isSafeInteger(value) ? String(value) : "Number cannot be represented exactly by this view." }];
  if (Array.isArray(value)) return value.length ? value.flatMap((v, i) => readableRows(v, `${title} · ${i + 1}`, depth + 1)) : [{ title, text: "None" }];
  if (typeof value !== "object") return [{ title, text: "Cannot display this record type." }];
  const rows = [];
  if (value.kind && !supportedKinds.has(value.kind) && !value.protocol && !value.job_id && !value.id)
    return [{ title, text: "Cannot display this record type. Exact evidence remains retained; use Copy UI state for context." }];
  const priority = key => { const index = summaryOrder.indexOf(key); return index < 0 ? summaryOrder.length : index; };
  for (const [key, entry] of Object.entries(value).sort(([a], [b]) => priority(a) - priority(b))) {
    if (key === "owner_token") {
      rows.push({ title: `${title} · Ownership`, text: "Private ownership evidence retained" });
      continue;
    }
    if (labels.has(key) || containers.has(key) || hiddenBytes.has(key)) {
      const label = labels.get(key);
      // Never expose a credential reference as configuration to copy/execute.
      if (key === "auth_ref") { rows.push({ title: `${title} · Authentication`, text: "Private reference retained" }); continue; }
      rows.push(...readableRows(entry, `${title} · ${label ? label[1] : key.replaceAll("_", " ")}`, depth + 1));
    }
  }
  if (!rows.length || Object.keys(value).some(key => !labels.has(key) && !containers.has(key) && !hiddenBytes.has(key)))
    rows.push({ title, text: "Cannot display this record type completely. Supported context is shown; exact evidence remains retained. Use Copy UI state for context." });
  return rows;
}

export function readableText(value, title = "Record") {
  return readableRows(value, title).map(row => `${row.title}\n${row.text}`).join("\n\n");
}
export function readableValue(value) {
  if (typeof value === "string") {
    try { return readableText(JSON.parse(value)); }
    catch (_) { if (!/^\s*[\[{]/.test(value)) return clean(value) || "Empty"; }
  }
  return readableText(value);
}

export function readablePages(value, title = null) {
  if (!title) title = value?.job ? "Worker record" : value?.pointer ? "Conversation viewer" :
    value?.kind === "review_result" ? "Independent review" : value?.kind === "result" ? "Result snapshot" :
    value?.binding && value?.input_json ? "Retained request" : "Record";
  const pages = [];
  let page = "";
  for (const row of readableRows(value, title)) {
    const text = Array.from(row.text);
    for (let start = 0; start < Math.max(1, text.length); start += 900) {
      const block = `${row.title}${start ? " · continued" : ""}\n${text.slice(start, start + 900).join("")}`;
      if (page && page.length + block.length > 1200) { pages.push(page); page = ""; }
      page += `${page ? "\n\n" : ""}${block}`;
    }
  }
  if (page) pages.push(page);
  return pages;
}
