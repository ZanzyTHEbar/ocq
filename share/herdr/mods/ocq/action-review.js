import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { readableValue } from "./readable-record.js";

export const mainAction = mode => ({ create: "Create Main", adopt: "Adopt Main", send: "Start goal", refresh: "Refresh Main", "role-refresh": "Update Main operating contract", reopen: "Reopen Main" }[mode] || "Start Main operation");

export function retainedGoal(value) {
  const input = JSON.parse(value.input_json), prompt = input.prompt;
  const prefix = `OCQ retained goal operation: ${value.status.operation_id}. integration_authorized=false: deliver a handoff, never integrate under this send. For EACH prepared Work request, before confirming it, record exact goal lineage using project work link-goal ${value.status.project_id} REQUEST ${value.status.operation_id}. Keep all stage transitions under this goal; do not infer completion from this instruction.\n\n`;
  return typeof prompt === "string" && prompt.startsWith(prefix) ? prompt.slice(prefix.length) : prompt;
}

// Summaries are derived from the frozen server preview, never editable form
// state. Full bytes remain available without becoming a navigation prerequisite.
export function mainSummary(value, machine) {
  const input = JSON.parse(value.input_json), status = value.status;
  return v_flex().gap(6)
    .child(div().font_semibold().child(mainAction(status.mode)))
    .child(div().child(`Project: ${input.label || status.project_id}\nMachine: ${machine ?? "This computer"}\nRepository: ${status.repo}\nBranch: ${status.main_branch}`))
    .when(typeof input.prompt === "string" && input.prompt.length > 0, e => e
      .child(div().font_semibold().child(status.mode === "send" ? "Goal" : "Initial message"))
      .child(div().child(readableValue(status.mode === "send" ? retainedGoal(value) : input.prompt)))
      .when(["create", "adopt"].includes(status.mode), p => p.child(div().text_size(12)
        .child(`${mainAction(status.mode)} will also send this initial message.`))))
    .child(div().text_size(12).child(status.mode === "send"
      ? "Send this goal to this project's existing Main. Implementation runs in an owned worktree; independent review and handoff follow. Integration is not authorized."
      : status.mode === "role-refresh"
      ? `Select the disclosed successor CLI and operating instructions. Preserve the old entry and every goal. No prompt, profile, permissions, branch, anchor or project binding change.\nPrevious contract: ${value.role.previous_sha256}\nSuccessor CLI: ${value.role.cli}\nImage: ${value.role.cli_sha256}`
      : "Keep one Main conversation linked to this project. Goals can request implementation in an owned worktree, independent review, and handoff. Nothing is integrated automatically."))
    .child(div().text_size(12).child("Permissions requiring your approval stay in OpenCode."));
}

export function roleSummary(value) {
  const input = JSON.parse(value.input_json);
  const session = input.profile ? null : JSON.parse(value.session_json);
  const main = input.profile ? JSON.parse(input.profile) : {
    agent: session.agent, provider_id: session.model.providerID,
    model_id: session.model.id, variant: session.model.variant,
  };
  const profile = p => `${p.agent} · ${p.provider_id}/${p.model_id}${p.variant ? ` · ${p.variant}` : ""}`;
  return div().text_size(12).child([
    `Main: ${profile(main)}`,
    `Implementation: ${profile(JSON.parse(value.role.selection.implementation_profile))}`,
    `Independent review: ${profile(JSON.parse(value.role.selection.review_profile))}`,
  ].filter(Boolean).join("\n"));
}

export function technicalDetails(review, button, prefix) {
  const box = v_flex().gap(4).child(button(`${prefix}-details`, review.details ? "Hide technical details" : "Technical details", cx => {
    review.details = !review.details; cx.notify();
  }));
  if (review.details) box.child(div().text_size(12).child(`Technical details · ${review.page + 1}/${review.pages.length}`))
    .child(div().child(review.pages[review.page]))
    .child(button(`${prefix}-copy-fields`, "Copy displayed fields", cx => cx.write_to_clipboard(review.pages.join("\n\n"))))
    .child(h_flex().gap(4)
      .when(review.page > 0, e => e.child(button(`${prefix}-previous`, "Previous details", cx => { review.page--; cx.notify(); })))
      .when(review.page < review.pages.length - 1, e => e.child(button(`${prefix}-next`, "More details", cx => { review.page++; cx.notify(); }))));
  return box;
}
