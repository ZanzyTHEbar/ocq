import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { exec, spaces } from "herdr";

// Ephemeral point-in-time observations, never a stored dimension or an
// authorization. Opening/refreshing a result does not inspect the filesystem.
export class WorktreeVerification {
  reset() { this.generation = (this.generation || 0) + 1; this.screen = false; this.target = null; this.result = null; this.working = false; this.error = null; }
  open(owner, association, worktree, cx) {
    this.reset();
    this.target = Object.freeze({ machine: owner.machine, space: owner.spaceID, folder: owner.folder, project: owner.selected, job: association.job_id, association: association.id, digest: association.manifest_digest, worktree: worktree || "", source: association.source, verification: association.verification });
    this.screen = true; owner.handle.focus(); cx.notify();
  }
  current(owner, t, generation) {
    const active = spaces().find((s) => s.active);
    return this.screen && this.target === t && this.generation === generation && owner.selected === t.project && owner.machine === t.machine && owner.spaceID === t.space && owner.folder === t.folder && active?.id === t.space && active?.machine === t.machine && active?.folder === t.folder && !owner.queueMode;
  }
  check(owner, cx) {
    const t = this.target, generation = this.generation;
    if (this.working || !this.current(owner, t, generation) || t.machine !== null || !t.worktree) return;
    this.working = true; this.result = null; this.error = null;
    cx.spawn(async (cx) => {
      try {
        if (!this.current(owner, t, generation)) return;
        const response = await exec("ocq-herdr", ["verify-worktree", t.project, t.job, t.association, t.digest, t.worktree], { on: null, timeout: 120000 });
        if (!this.current(owner, t, generation)) return;
        if (response.code !== 0) throw new Error("Check unavailable; match unknown. The existing queue could not be read. No fallback or retry was performed.");
        const r = JSON.parse(response.stdout);
        if (r.project_id !== t.project || r.job_id !== t.job || r.association_id !== t.association || r.manifest_digest !== t.digest || r.worktree !== t.worktree || r.integration !== "not_authorized" || !["matches_snapshot", "modified_since_snapshot", "worktree_unavailable", "unknown"].includes(r.worktree_match) || !r.attempted_at || (r.worktree_match !== "unknown" && (!r.checked_at || r.freshness !== "live_worktree_check"))) throw new Error("Check response context invalid; match unknown.");
        this.result = r;
      } catch (e) { if (this.current(owner, t, generation)) this.error = String(e); }
      finally { if (this.current(owner, t, generation)) { this.working = false; cx.notify(); } }
    }); cx.notify();
  }
  render(owner, cx, theme, button) {
    const t = this.target, r = this.result;
    const back = (cx) => { this.reset(); cx.notify(); };
    return v_flex().id("ocq-worktree-check").size_full().min_h(0).p(14).gap(10).bg(theme.background).text_color(theme.foreground)
      .track_focus(owner.handle).on_key_down((e, cx) => {
        if (e.modifiers.control || e.modifiers.alt || e.modifiers.platform) return;
        if (e.key === "escape") back(cx);
        else if (e.key === "v" && !e.is_held) this.check(owner, cx);
        else return;
        cx.stop_propagation();
      })
      .child(div().font_semibold().child("Verify owned worktree"))
      .child(button("ocq-worktree-back", "Back to stored results", back))
      .child(v_flex().id("ocq-worktree-check-content").flex_1().min_h(0).overflow_y_scroll().gap(10)
        .child(div().child("Explicit LIVE filesystem read. Compares this owned checkout to this exact captured result. No job reconciliation, test execution or integration authorization. Refresh and inspection do not run this check."))
        .child(div().text_size(12).text_color(theme.muted).child(`Machine: ${t.machine || "local"}\nProject: ${t.project}\nJob: ${t.job}\nAssociation: ${t.association}\nResult SHA-256: ${t.digest}\nStored owned worktree: ${t.worktree || "not recorded"}\nSource: ${t.source || "unknown"} · ${t.verification || "unknown"}`))
        .when(t.machine !== null, (e) => e.child(div().child("Remote filesystem verification is not enabled. Use this machine's local UI.")))
        .when(!t.worktree, (e) => e.child(div().child("No stored owned worktree. Manifest-path fallback and arbitrary paths are not supported.")))
        .when(Boolean(this.working), (e) => e.child(div().child("Reading live filesystem… match unknown until this check returns.")))
        .when(Boolean(this.error), (e) => e.child(div().text_color(theme.ansi1).child(this.error)))
        .when(Boolean(r), (e) => e.child(v_flex().gap(6)
          .child(div().font_semibold().child(`${r?.checked_at ? "Prior check" : "Check not performed"}: ${r?.worktree_match}`))
          .child(div().text_size(12).child(`Attempted: ${r?.attempted_at}\nFilesystem checked: ${r?.checked_at || "not completed"}\nResult SHA-256 requested: ${r?.manifest_digest}\nDifferences reported: ${r?.checked_at && r?.worktree_match !== "worktree_unavailable" ? r.difference_count : "unknown"}\nValidation: ${r?.validation}\nIntegration: ${r?.integration}`))
          .when(Boolean(r?.error), (e) => e.child(div().text_color(theme.ansi1).child(r.error)))))
        .child(div().text_size(12).text_color(theme.muted).child("Current filesystem state is unknown. A prior check is point-in-time evidence only, separate from the stored project snapshot; files may change immediately afterward. A match does not validate tests or authorize integration.")))
      .when(t.machine === null && Boolean(t.worktree) && !this.working, (e) => e.child(button("ocq-worktree-verify", "Verify filesystem now · V", (cx) => this.check(owner, cx))));
  }
}
