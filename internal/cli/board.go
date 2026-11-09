package cli

import (
	"bufio"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

// projectBoard is a non-consuming terminal view. Run it again to refresh; it
// never reconciles, acknowledges mail, or treats stored observations as live.
func projectBoard(state *string) *cobra.Command {
	return readerCommand(state, "board PROJECT", func(reader *core.Reader, args []string) error {
		if len(args) != 1 {
			return validation("usage: ocq [--state-dir DIR] project board PROJECT")
		}
		projectID := args[0]
		board, err := reader.Board(projectID)
		if err != nil {
			return err
		}
		show, messages, mailReadAt := board.Project, board.Messages, board.MailReadAt
		w := bufio.NewWriter(os.Stdout)

		// Quote stored/user-supplied text so terminal control sequences cannot be
		// interpreted as display commands. The exact IDs remain usable in the CLI.
		fmt.Fprintf(w, "PROJECT %q (%s)\n", show.Label, show.ProjectID)
		fmt.Fprintf(w, "Stored snapshot: %s; refresh by running this command again.\n", show.ReadAt)
		fmt.Fprintf(w, "Jobs: %d queued, %d starting, %d running, %d unknown, %d finished, %d cancelled, %d resolved\n",
			show.JobCounts.Queued, show.JobCounts.Launching, show.JobCounts.Running, show.JobCounts.Unknown, show.JobCounts.Finished, show.JobCounts.Cancelled, show.JobCounts.Resolved)
		fmt.Fprintf(w, "Blocked outcomes: %d (also counted by state)\n\n", show.JobCounts.Blocked)
		fmt.Fprintln(w, "WORK")
		if len(show.Jobs) == 0 {
			fmt.Fprintln(w, "  No jobs.")
		}
		for _, job := range show.Jobs {
			fmt.Fprintf(w, "  %s  %s  state=%s outcome=%s lane=%s\n", job.JobID, job.Role, job.State, job.Outcome, job.LaneID)
			fmt.Fprintf(w, "    result=%s validation=%s verified=%t review=%s verdict=%s integration=%s worktree=%s\n",
				job.ResultStatus, job.Validation.Status, job.Validation.Verified, job.Review.Status, job.Review.Verdict, job.Integration, job.WorktreeMatch)
			if job.Review.LatestResultDigest != "" {
				fmt.Fprintf(w, "    latest result: %s\n", job.Review.LatestResultDigest)
			}
			if job.Review.ReviewedResultDigest != "" {
				fmt.Fprintf(w, "    reviewed result: %s\n", job.Review.ReviewedResultDigest)
			}
			if job.Review.StaleForLatestResult {
				fmt.Fprintln(w, "    review is stale for latest result")
			}
			if job.Reason != "" {
				fmt.Fprintf(w, "    reason: %q\n", job.Reason)
			}
		}
		fmt.Fprintf(w, "\nINBOX (unread, first 20; separate stored read at %s)\n", mailReadAt)
		if len(messages) == 0 {
			fmt.Fprintln(w, "  No unread messages.")
		}
		for _, message := range messages {
			fmt.Fprintf(w, "  %s  %s  from=%q  %q\n", message.ID, message.Kind, message.Sender, message.Subject)
		}
		fmt.Fprintf(w, "\nInspect: ocq project job %s JOB; ocq project inbox %s\n", projectID, projectID)
		fmt.Fprintln(w, "If using --state-dir, include it before project in these commands.")
		fmt.Fprintln(w, "Results and review verdicts may be imported claims; inspect their provenance before relying on them.")
		fmt.Fprintln(w, "Worktree drift is not checked here; use project verify-worktree.")
		fmt.Fprintln(w, "Reading this board does not acknowledge mail or authorize integration.")
		return w.Flush()
	})
}
