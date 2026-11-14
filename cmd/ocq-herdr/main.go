// ocq-herdr exposes core views and explicit inbox acknowledgements to Herdr.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ocq-herdr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root := &cobra.Command{
		Use: "ocq-herdr", Short: "OCQ views and explicit inbox actions for Herdr", SilenceErrors: true, SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			return faults.New(faults.CodeValidationFailed, "usage: ocq-herdr projects | board PROJECT | job PROJECT JOB | recent-inbox PROJECT | inbox PROJECT AFTER | mail PROJECT MESSAGE SEQUENCE | mail-ack PROJECT MESSAGE SEQUENCE read|archive | recent-results PROJECT | results PROJECT AFTER | manifest PROJECT DIGEST | compose-receive LAUNCH | compose-preview ID LAUNCH | compose-rendezvous LAUNCH | compose-status | compose-dispatch-intent ID | compose-arm ID | compose-dispatch ID | compose-reconcile ID | compose-cancel ID | compose-retire ID | intake-receive KIND LAUNCH | intake-receive-text KIND LAUNCH | intake-preview ID LAUNCH | intake-rendezvous LAUNCH | intake-status | intake-arm ID | intake-dispatch ID | intake-reconcile ID | intake-cancel ID | intake-retire ID")
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(palette()...)
	root.AddCommand(queueCommands()...)
	root.AddCommand(composeCommands()...)
	root.AddCommand(intakeCommands()...)
	root.AddCommand(resultPublicationCommands()...)
	root.AddCommand(capturePublicationCommands()...)
	root.AddCommand(workCommands()...)
	root.AddCommand(mainClientCommands()...)
	installCompatibility(root)
	root.SetArgs(args)
	return root.Execute()
}

type readView func(*core.Reader, []string) (any, error)

const pageSize = 20

func palette() []*cobra.Command {
	return []*cobra.Command{
		viewCommand("verify-worktree PROJECT JOB ASSOCIATION DIGEST STORED_WORKTREE", 5, verifyOwnedWorktree),
		viewCommand("projects", 0, func(r *core.Reader, _ []string) (any, error) { return r.Projects() }),
		viewCommand("board PROJECT", 1, func(r *core.Reader, args []string) (any, error) {
			board, err := r.Board(args[0])
			if err != nil {
				return nil, err
			}
			return boardView(board), nil
		}),
		viewCommand("job PROJECT JOB", 2, func(r *core.Reader, args []string) (any, error) {
			detail, err := r.Job(args[0], args[1])
			if err != nil {
				return nil, err
			}
			return projectDetail(detail), nil
		}),
		viewCommand("recent-inbox PROJECT", 1, func(r *core.Reader, args []string) (any, error) {
			page, err := r.RecentInbox(args[0], pageSize)
			if err != nil {
				return nil, err
			}
			messages := make([]inboxMessage, 0, len(page.Messages))
			for _, message := range page.Messages {
				messages = append(messages, inboxMessage{message, strconv.FormatInt(message.Sequence, 10)})
			}
			return struct {
				core.ProjectInboxOutput
				Messages []inboxMessage `json:"messages"`
			}{page, messages}, nil
		}),
		viewCommand("recent-results PROJECT", 1, func(r *core.Reader, args []string) (any, error) {
			page, err := r.RecentResults(args[0], pageSize)
			if err != nil {
				return nil, err
			}
			return projectResults(page), nil
		}),
		viewCommand("inbox PROJECT AFTER", 2, func(r *core.Reader, args []string) (any, error) {
			after, err := cursor(args[1])
			if err != nil {
				return nil, err
			}
			page, err := r.Inbox(args[0], "all", false, after, pageSize+1)
			if err != nil {
				return nil, err
			}
			hasMore := len(page.Messages) > pageSize
			if hasMore {
				page.Messages = page.Messages[:pageSize]
				page.NextSequence = page.Messages[len(page.Messages)-1].Sequence
			}
			messages := make([]inboxMessage, 0, len(page.Messages))
			for _, message := range page.Messages {
				messages = append(messages, inboxMessage{message, strconv.FormatInt(message.Sequence, 10)})
			}
			return struct {
				core.ProjectInboxOutput
				Messages []inboxMessage `json:"messages"`
				Cursor   string         `json:"cursor"`
				HasMore  bool           `json:"has_more"`
			}{page, messages, strconv.FormatInt(page.NextSequence, 10), hasMore}, nil
		}),
		viewCommand("mail PROJECT MESSAGE SEQUENCE", 3, func(r *core.Reader, args []string) (any, error) {
			return readMail(r, args[0], args[1], args[2])
		}),
		viewCommand("results PROJECT AFTER", 2, func(r *core.Reader, args []string) (any, error) {
			after, err := cursor(args[1])
			if err != nil {
				return nil, err
			}
			page, err := r.Results(args[0], after, pageSize+1)
			if err != nil {
				return nil, err
			}
			hasMore := len(page.Items) > pageSize
			if hasMore {
				page.Items = page.Items[:pageSize]
				page.NextSequence = page.Items[len(page.Items)-1].Event.Sequence
			}
			return struct {
				projectResultsPage
				Cursor  string `json:"cursor"`
				HasMore bool   `json:"has_more"`
			}{projectResults(page), strconv.FormatInt(page.NextSequence, 10), hasMore}, nil
		}),
		viewCommand("manifest PROJECT DIGEST", 2, func(r *core.Reader, args []string) (any, error) {
			manifest, err := r.Manifest(args[0], args[1])
			if err != nil {
				return nil, err
			}
			return string(manifest), nil
		}),
		mailAckCommand(),
	}
}

type inboxMessage struct {
	core.ProjectInboxMessage
	SequenceText string `json:"sequence_text"`
}

type mailObservation struct {
	Message inboxMessage `json:"message"`
	ReadAt  string       `json:"read_at"`
}

// Locate the exact row without scanning or relying on JavaScript's integer range.
func readMail(r *core.Reader, project, message, sequence string) (mailObservation, error) {
	var out mailObservation
	n, err := cursor(sequence)
	if err != nil {
		return out, err
	}
	if n == 0 {
		return out, faults.New(faults.CodeValidationFailed, "message sequence must be positive")
	}
	page, err := r.Inbox(project, "all", false, n-1, 1)
	if err != nil {
		return out, err
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != message || page.Messages[0].Sequence != n {
		return out, faults.New(faults.CodeNotFound, "exact inbox message not found")
	}
	return mailObservation{inboxMessage{page.Messages[0], strconv.FormatInt(n, 10)}, page.ReadAt}, nil
}

func mailAckCommand() *cobra.Command {
	return &cobra.Command{
		Use: "mail-ack PROJECT MESSAGE SEQUENCE read|archive",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 4 || (args[3] != "read" && args[3] != "archive") {
				return faults.New(faults.CodeValidationFailed, "mail-ack requires PROJECT MESSAGE SEQUENCE read|archive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			sequence, err := cursor(args[2])
			if err != nil {
				return err
			}
			if sequence == 0 {
				return faults.New(faults.CodeValidationFailed, "message sequence must be positive")
			}
			ack, err := core.OpenExistingMailAcknowledger(os.Getenv("OCQ_STATE_DIR"))
			if err != nil {
				return err
			}
			defer ack.Close()
			result, err := ack.Acknowledge(args[0], args[1], sequence, args[3] == "archive")
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
}

func cursor(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, faults.Wrap(faults.CodeValidationFailed, "invalid cursor", err)
	}
	if n < 0 {
		return 0, faults.New(faults.CodeValidationFailed, "cursor must be non-negative")
	}
	return n, nil
}

func viewCommand(use string, count int, view readView) *cobra.Command {
	return &cobra.Command{
		Use: use,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != count {
				return faults.New(faults.CodeValidationFailed, "invalid arguments", "usage", use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			reader, err := core.OpenReader(os.Getenv("OCQ_STATE_DIR"))
			if err != nil {
				return faults.Wrap(faults.CodeDatabaseRead, "open OCQ reader", err)
			}
			defer reader.Close()
			result, err := view(reader, args)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
}
