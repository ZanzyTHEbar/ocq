// Package cli parses command-line input and renders results from the core.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func output(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }

func validation(message string) error { return faults.New(faults.CodeValidationFailed, message) }

func Run(args []string) error {
	var state string
	helpRequested := false
	root := &cobra.Command{Use: "ocq", Short: "Local-first execution queue", SilenceUsage: true, SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return validation(fmt.Sprintf("unknown command %q", args[0]))
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			return validation("usage: ocq [--state-dir DIR] submit|submit-agent|project|status|list|logs|dispatch|cancel|resolve|reconcile|notifications|ack ...")
		},
	}
	root.PersistentFlags().StringVar(&state, "state-dir", "", "private persistent local state directory")
	root.CompletionOptions.DisableDefaultCmd = true
	// The former ContinueOnError flag sets returned flag.ErrHelp and wrote
	// help to stderr; the process entry point therefore exits nonzero.
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		helpRequested = true
		_, _ = fmt.Fprintf(os.Stderr, "Usage of %s:\n", cmd.CommandPath())
		cmd.Flags().SetOutput(os.Stderr)
		cmd.Flags().PrintDefaults()
	})
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return faults.Wrap(faults.CodeValidationFailed, "invalid flags", err)
	})
	root.AddCommand(commandPalette(&state)...)
	parsedArgs, err := legacyFlagSpelling(root, args)
	if err != nil {
		return err
	}
	root.SetArgs(parsedArgs)
	if err := root.Execute(); err != nil {
		return err
	}
	if helpRequested {
		return flag.ErrHelp
	}
	return nil
}

// The former standard flag parser accepted -long as well as --long. Rewrite
// only recognized option tokens, never option values, positional arguments or
// anything following --. Cobra/pflag still performs all parsing and validation.
func legacyFlagSpelling(root *cobra.Command, args []string) ([]string, error) {
	result := make([]string, len(args))
	copy(result, args)
	cmd := root
	for i := 0; i < len(result); i++ {
		arg := result[i]
		if arg == "--" {
			// The old global parser consumed its own terminator before
			// handing the remaining command to the command parser.
			if cmd == root && i+1 < len(result) {
				found := false
				for _, child := range root.Commands() {
					if child.Name() == result[i+1] {
						result = append(result[:i], result[i+1:]...)
						i--
						found = true
						break
					}
				}
				if found {
					continue
				}
			}
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name := strings.TrimPrefix(arg, "-")
			name = strings.TrimPrefix(name, "-")
			name, _, _ = strings.Cut(name, "=")
			if cmd != root && name == "state-dir" {
				return nil, validation("--state-dir must precede the command")
			}
			flag := cmd.Flags().Lookup(name)
			if flag == nil {
				flag = root.PersistentFlags().Lookup(name)
			}
			if flag != nil {
				if !strings.HasPrefix(arg, "--") {
					result[i] = "-" + arg
				}
				if flag.NoOptDefVal == "" && !strings.Contains(arg, "=") {
					i++
				}
			}
			continue
		}
		if cmd.HasSubCommands() {
			var next *cobra.Command
			for _, child := range cmd.Commands() {
				if child.Name() == arg {
					next = child
					break
				}
			}
			if next == nil {
				break
			}
			// These two legacy views moved a leading PROJECT behind their
			// flags before using the standard non-interspersed parser.
			if cmd.Name() == "project" && (next.Name() == "results" || next.Name() == "inbox") && i+1 < len(result) && !strings.HasPrefix(result[i+1], "-") {
				project := result[i+1]
				copy(result[i+1:], result[i+2:])
				result[len(result)-1] = project
			}
			cmd = next
			continue
		}
		break
	}
	return result, nil
}

// queueCommand owns the lifetime of a mutable queue. Projection commands use
// readerCommand instead, so inspecting a project cannot initialize a store.
func queueCommand(state *string, use string, run func(*core.Queue, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: use, RunE: queueRun(state, run)}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func commandPalette(state *string) []*cobra.Command {
	notifications := queueCommand(state, "notifications", nil)
	limit := notifications.Flags().Int("limit", 20, "maximum unacknowledged completion events (1..100)")
	notifications.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("notifications [--limit N] takes no positional arguments")
		}
		items, err := q.Notifications(*limit)
		if err != nil {
			return err
		}
		return output(items)
	})
	ack := queueCommand(state, "ack EVENT_ID", func(q *core.Queue, args []string) error {
		if len(args) != 1 || args[0] == "" {
			return validation("ack requires one exact notification event ID")
		}
		item, err := q.Acknowledge(args[0])
		if err != nil {
			return err
		}
		return output(item)
	})
	reconcile := queueCommand(state, "reconcile JOB_ID", func(q *core.Queue, args []string) error {
		if len(args) != 1 {
			return validation("reconcile requires one exact agent job ID")
		}
		job, err := q.Reconcile(args[0])
		if err != nil {
			return err
		}
		return output(job)
	})
	submit := queueCommand(state, "submit [flags] -- COMMAND [ARG...]", nil)
	key := submit.Flags().String("key", "", "required immutable idempotency key")
	cwd := submit.Flags().String("cwd", ".", "existing working directory (canonicalized at submission)")
	memory := submit.Flags().Int64("memory-bytes", 64<<20, "declared memory demand, not an enforced limit")
	submit.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		job, err := q.SubmitCommand(*key, *cwd, args, *memory)
		if err != nil {
			return err
		}
		return output(job)
	})
	jobCommand := func(name string, cancel bool) *cobra.Command {
		return queueCommand(state, name+" JOB_ID", func(q *core.Queue, args []string) error {
			if len(args) != 1 {
				return validation("status/cancel requires one job ID")
			}
			var job core.Job
			var err error
			if cancel {
				job, err = q.Cancel(args[0])
			} else {
				job, err = q.Job(args[0])
			}
			if err != nil {
				return err
			}
			return output(job)
		})
	}
	list := queueCommand(state, "list", func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("list takes no arguments")
		}
		jobs, err := q.Jobs()
		if err != nil {
			return err
		}
		return output(jobs)
	})
	logs := queueCommand(state, "logs JOB_ID", nil)
	stderr := logs.Flags().Bool("stderr", false, "read stderr instead of stdout")
	logs.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 1 {
			return validation("logs [--stderr] JOB_ID")
		}
		file, err := q.OpenLog(args[0], *stderr)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(os.Stdout, file)
		return err
	})
	dispatch := queueCommand(state, "dispatch", nil)
	slots := dispatch.Flags().Int("slots", 2, "maximum launching/running/unknown jobs")
	floor := dispatch.Flags().Int64("memory-floor-bytes", 6<<30, "minimum Linux MemAvailable to preserve")
	commandsOnly := dispatch.Flags().Bool("commands-only", false, "block agent admission/polling; foreground command queues only")
	dispatch.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 || *slots < 1 || *floor < 0 {
			return validation("dispatch requires slots>=1 and memory-floor-bytes>=0")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return q.Dispatch(ctx, *slots, *floor, *commandsOnly, func() {
			_, _ = io.WriteString(os.Stderr, "dispatcher ready; shutdown stops admission and waits for owned foreground commands\n")
		})
	})
	resolve := queueCommand(state, "resolve JOB_ID", nil)
	evidence := resolve.Flags().String("evidence", "", "required operator reconciliation evidence; not validation")
	resolve.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 1 || *evidence == "" {
			return validation("resolve --evidence TEXT JOB_ID (dispatcher must be stopped)")
		}
		job, err := q.Resolve(args[0], *evidence)
		if err != nil {
			return err
		}
		return output(job)
	})
	return []*cobra.Command{notifications, ack, submitAgentCommand(state), reconcile, submit, jobCommand("status", false), jobCommand("cancel", true), list, logs, dispatch, resolve, projectCommand(state)}
}

func queueRun(state *string, run func(*core.Queue, []string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		q, err := core.OpenQueue(*state)
		if err != nil {
			return err
		}
		defer q.Close()
		return run(q, args)
	}
}

func submitAgentCommand(state *string) *cobra.Command {
	cmd := queueCommand(state, "submit-agent", nil)
	f := cmd.Flags()
	key := f.String("key", "", "required immutable idempotency key")
	repo := f.String("repo", ".", "repository to canonicalize")
	base := f.String("base", "HEAD", "commit to pin at submission")
	prompt := f.String("prompt-file", "", "UTF-8 prompt file, at most 16 KiB")
	agent := f.String("agent", "", "explicit OpenCode agent ID")
	model := f.String("model", "", "explicit provider/model#variant")
	permissions := f.String("permissions-file", "", "explicit session-scoped JSON permission rules")
	memory := f.Int64("memory-bytes", 64<<20, "declared demand, not enforcement")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 || *key == "" || len(*key) > 1024 || !utf8.ValidString(*key) || *prompt == "" || *permissions == "" || *agent == "" || !utf8.ValidString(*agent) || !utf8.ValidString(*model) || *memory < 0 {
			return validation("submit-agent requires --key, --repo, --prompt-file, --agent, --model, --permissions-file")
		}
		text, err := os.ReadFile(*prompt)
		if err != nil {
			return faults.Wrap(faults.CodeStorageFailed, "read prompt", err)
		}
		rules, err := os.ReadFile(*permissions)
		if err != nil {
			return faults.Wrap(faults.CodeStorageFailed, "read permissions", err)
		}
		job, err := q.SubmitAgent(core.AgentSubmission{Key: *key, Repo: *repo, Base: *base, Agent: *agent, Model: *model, Prompt: text, Permissions: rules, MemoryBytes: *memory})
		if err != nil {
			return err
		}
		return output(job)
	})
	return cmd
}
