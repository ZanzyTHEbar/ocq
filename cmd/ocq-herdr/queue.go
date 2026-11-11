package main

import (
	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func queueCommands() []*cobra.Command {
	commands := []*cobra.Command{
		viewCommand("queue-jobs AFTER", 1, func(r *core.Reader, args []string) (any, error) {
			after, err := cursor(args[0])
			if err != nil {
				return nil, err
			}
			return r.QueueJobs(after, pageSize)
		}),
		viewCommand("queue-job JOB", 1, func(r *core.Reader, args []string) (any, error) { return r.QueueJob(args[0]) }),
		viewCommand("queue-log JOB stdout|stderr OFFSET", 3, func(r *core.Reader, args []string) (any, error) {
			offset, err := cursor(args[2])
			if err != nil {
				return nil, err
			}
			return r.QueueLog(args[0], args[1], offset)
		}),
		viewCommand("notifications", 0, func(r *core.Reader, _ []string) (any, error) { return r.PendingNotifications(pageSize) }),
		viewCommand("notification EVENT", 1, func(r *core.Reader, args []string) (any, error) { return r.Notification(args[0]) }),
	}
	commands = append(commands, notificationAckCommands()...)
	commands = append(commands, queuedCancelCommands()...)
	commands = append(commands, commandResolutionCommands()...)
	commands = append(commands, commandSubmissionCommands()...)
	commands = append(commands, agentSubmissionCommands()...)
	commands = append(commands, commandDispatcherCommands()...)
	commands = append(commands, agentHandoffCommands()...)
	return append(commands, agentReconcileCommands()...)
}
