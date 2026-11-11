package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// HC registration and native layout routing deliberately have distinct IDs:
// native --machine consumes the returned registry NAME; ID identifies its core.
type peerRegistration struct {
	Name, ID, Session, Target string
	Temporary, Via            bool
}

// HC0.1.19 terminal.list is aggregate, not --on scoped. Its schema promises
// connected machines' addresses as name/tN. Join its line state to the exact
// machine ls registry identity: installed ls omits ID even though its published
// schema references MachineView. Never infer an identity from that omission.
// Refuse incomplete/disconnected views rather than treating them as empty.
func peerInventory(root string, peer peerRegistration) []byte {
	var list struct {
		Machines  []struct{ Name, ID, State string }
		Terminals []json.RawMessage
	}
	// ListResult explicitly does not wait for connecting machines. Native layout
	// readiness belongs to the app's connection, not this CLI inventory reader.
	// Poll only this read; never replay registration or any native mutation.
	wait("exact peer inventory did not connect; see peer-inventory-observation.json", func() bool {
		var registry struct{ Machines []peerRegistration }
		must(json.Unmarshal(run(filepath.Join(root, "bin", "hc"), "machine", "ls", "--json"), &registry))
		check(len(registry.Machines) == 1, "private inventory registry changed")
		identity := registry.Machines[0]
		check(identity.Name == peer.Name && identity.ID == peer.ID && identity.Session == peer.Session && identity.Target == peer.Target && identity.Via == peer.Via && !identity.Temporary, "private inventory registry identity changed")
		b := run(filepath.Join(root, "bin", "hc"), "ls", "--all", "--json")
		list.Machines, list.Terminals = nil, nil
		must(json.Unmarshal(b, &list))
		save(filepath.Join(root, "peer-inventory-observation.json"), map[string]any{"machines": list.Machines, "registry_identity": identity, "terminal_count": len(list.Terminals), "expected_name": peer.Name, "expected_core_id": peer.ID})
		found, connected := false, false
		for _, m := range list.Machines {
			if m.Name == peer.Name {
				check(!found && (m.ID == "" || m.ID == peer.ID), "peer inventory identity mismatch; see peer-inventory-observation.json")
				check(m.State == "connected" || m.State == "connecting", "peer inventory connection refused; see peer-inventory-observation.json")
				found, connected = true, m.State == "connected"
			}
		}
		check(found, "peer missing from aggregate inventory; empty is not proof")
		return connected
	})
	terminals := []json.RawMessage{}
	for _, raw := range list.Terminals {
		var t struct{ Address string }
		must(json.Unmarshal(raw, &t))
		check(t.Address != "", "inventory terminal address unavailable")
		if strings.HasPrefix(t.Address, peer.Name+"/") {
			terminals = append(terminals, raw)
		}
	}
	filtered, e := json.Marshal(map[string]any{"terminals": terminals, "machine": peer.Name, "core_id": peer.ID, "source": "hc ls --all --json / exact machine ls registry join and address prefix"})
	must(e)
	return filtered
}

func modelAnchorReady(root, role, address string, terminals []json.RawMessage) bool {
	expected := []string{filepath.Join(root, "bin", "dispatcher-proof"), "model-anchor", role}
	for _, raw := range terminals {
		var t struct {
			Address, State string
			PID            int
			Argv           []string
			Foreground     *struct {
				PID  int
				Argv []string
				Cwd  string
			}
		}
		must(json.Unmarshal(raw, &t))
		if t.Address != address {
			continue
		}
		if t.State != "live" || t.Foreground == nil {
			return false
		}
		check(strings.Join(t.Argv, "\x00") == strings.Join(expected, "\x00"), "model anchor did not use exact direct default argv; no shell fallback")
		check(t.PID == t.Foreground.PID && strings.Join(t.Foreground.Argv, "\x00") == strings.Join(expected, "\x00") && t.Foreground.Cwd == root, "model anchor foreground identity/folder mismatch")
		return true
	}
	return false
}
