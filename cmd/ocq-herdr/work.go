package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

var workPointer = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var workSpace = regexp.MustCompile(`^sp[0-9a-f]{32}$`)

// No authoring/upload endpoint: all content was prepared locally on the server.
func workCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, action := range []string{"list", "status", "recover", "output", "handoff", "preview", "confirm"} {
		action := action
		n, use := 3, "work-"+action+" SPACE QUEUE PROJECT"
		if action != "list" {
			n++
			use += " REQUEST_ID"
		}
		if action == "preview" || action == "confirm" {
			n += 4
			use += " PEER FOLDER STORE_CONTEXT BRIDGE_IMAGE"
		}
		if action == "confirm" {
			n++
			use += " PREVIEW_SHA256"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			if !workSpace.MatchString(a[0]) || (action != "list" && !workPointer.MatchString(a[3])) {
				return errors.New("invalid work scope or pointer")
			}
			dir, err := existingBridgeStateDir()
			if err != nil {
				return err
			}
			r, err := core.OpenReader(dir)
			if err != nil {
				return err
			}
			queue, store, err := r.WorkQueueContext()
			if closeErr := r.Close(); err == nil {
				err = closeErr
			}
			if err != nil || queue != a[1] {
				return errors.New("work queue context changed")
			}
			var out any
			if action == "preview" || action == "confirm" {
				// Peer is the caller's routing pin, not authenticated identity.
				// Independently inspect THIS executable, store and selected folder.
				if a[4] == "" || len(a[4]) > 128 {
					return errors.New("invalid work peer pin")
				}
				binding, e := compatibility(a[0], a[5])
				observation, flagErr := cmd.Flags().GetString("work-observation-context")
				if e != nil || flagErr != nil || binding.Queue != queue || binding.ObservationStoreContext != store || store != a[6] || binding.Image != a[7] || observation != binding.ObservationContext {
					return errors.New("work bridge/store/image context changed")
				}
				c := core.WorkActionContext{Protocol: core.WorkProtocol, Peer: a[4], Space: a[0], Folder: a[5], QueueIdentity: queue, StoreContext: store, ImageSHA256: binding.Image, ImageRole: "bridge", Action: "confirm"}
				if action == "preview" {
					out, err = core.PreviewProjectWork(dir, a[2], a[3], c)
				} else {
					out, err = core.ConfirmProjectWork(dir, a[2], a[3], a[8], c)
				}
			} else {
				switch action {
				case "list":
					out, err = core.ListProjectWork(dir, a[2])
				case "status", "recover":
					// Recovery observes only the exact server record/receipt.
					out, err = core.InspectProjectWork(dir, a[2], a[3])
				case "output":
					out, err = core.ReadProjectWorkOutput(dir, a[2], a[3])
				case "handoff":
					out, err = core.ProjectWorkHandoff(dir, a[2], a[3])
				}
			}
			if err != nil {
				return err
			}
			return writeWork(cmd, out)
		}})
	}
	return commands
}

func workProjection(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var safe any
	if err = decoder.Decode(&safe); err != nil {
		return nil, err
	}
	observationDecimalFields(safe)
	return safe, nil
}

func writeWork(cmd *cobra.Command, value any) error {
	safe, err := workProjection(value)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(safe)
	if err != nil {
		return err
	}
	if len(raw)+1 > core.WorkResponseBytes {
		return errors.New("work response exceeds bound")
	}
	_, err = cmd.OutOrStdout().Write(append(raw, '\n'))
	return err
}
