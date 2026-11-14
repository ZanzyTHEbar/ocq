package core

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// A tombstone, not a selected-Main registry. Persist before the successor intent
// so an interrupted prepare can never leave the old setup startable.
type mainSupersession struct {
	Version   int    `json:"version"`
	Intent    string `json:"intent_sha256"`
	Successor string `json:"successor_operation_id"`
	Input     string `json:"successor_input_sha256"`
}

func readMainSupersession(j *os.File, id, digest string) (mainSupersession, bool, error) {
	var marker mainSupersession
	b, err := workRead(j, mainFile(id, "superseded"))
	if errors.Is(err, syscall.ENOENT) {
		return marker, false, nil
	}
	if err != nil {
		return marker, true, err
	}
	if _, err = decodeStrictJSONObject(b, WorkRequestBytes, &marker); err != nil {
		return marker, true, err
	}
	if marker.Version != 1 || marker.Intent != digest || !workID(marker.Successor) || marker.Successor == id || !isValidHexLen(marker.Input, 64) {
		return marker, true, errors.New("invalid Main supersession evidence")
	}
	return marker, true, nil
}

// Called only under the same project journal lock as Start. Every retained
// setup must be provably create-only and unarmed/unattempted, including history.
// A distinct explicit create operation is the supersession request; no age or
// latest-session heuristic selects a replacement.
func mainCreatePredecessor(r *Reader, journal *composeJournal, project, id, input string) (string, string, error) {
	j := journal.dir
	if _, err := j.Seek(0, 0); err != nil {
		return "", "", err
	}
	entries, err := workEntries(j)
	if err != nil {
		return "", "", err
	}
	known := map[string]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".main-") && strings.HasSuffix(entry.Name(), ".intent.json") {
			known[strings.TrimSuffix(strings.TrimPrefix(entry.Name(), ".main-"), ".intent.json")] = true
		}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".main-") {
			continue
		}
		owner, _, ok := strings.Cut(strings.TrimPrefix(entry.Name(), ".main-"), ".")
		if !ok || !known[owner] {
			return "", "", errors.New("orphan Main evidence forbids replacement")
		}
	}
	prior, digest := "", ""
	dangling := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".main-") || !strings.HasSuffix(name, ".intent.json") {
			continue
		}
		oldID := strings.TrimSuffix(strings.TrimPrefix(name, ".main-"), ".intent.json")
		old, in, err := mainLoad(r, j, project, oldID)
		if err != nil {
			return "", "", err
		}
		if in.Mode != "create" {
			return "", "", errors.New("retained non-create Main evidence requires exact recovery")
		}
		for _, file := range entries {
			prefix := ".main-" + oldID + "."
			if !strings.HasPrefix(file.Name(), prefix) {
				continue
			}
			suffix := strings.TrimPrefix(file.Name(), prefix)
			if suffix == "intent.json" || suffix == "superseded.json" {
				continue
			}
			if strings.HasPrefix(suffix, "preview-") && strings.HasSuffix(suffix, ".json") && isValidHexLen(strings.TrimSuffix(strings.TrimPrefix(suffix, "preview-"), ".json"), 64) {
				continue
			}
			return "", "", errors.New("Main setup has armed/attempted/unknown stage evidence; no replacement")
		}
		marker, superseded, err := readMainSupersession(j, oldID, old.Digest)
		if err != nil {
			return "", "", err
		}
		if superseded {
			// Recover durability after an earlier create/fsync error before
			// allowing a successor intent to become startable.
			if err := journal.syncFile(mainFile(oldID, "superseded")); err != nil {
				return "", "", err
			}
			next, nextInput, err := mainLoad(r, j, project, marker.Successor)
			if errors.Is(err, syscall.ENOENT) {
				if marker.Successor != id || marker.Input != input {
					return "", "", errors.New("incomplete Main supersession requires its exact successor operation/input")
				}
				dangling++
				continue
			}
			if err != nil || next.Digest != marker.Input || nextInput.Mode != "create" {
				return "", "", errors.New("Main supersession successor evidence mismatched")
			}
			continue
		}
		if prior != "" {
			return "", "", errors.New("ambiguous unsuperseded Main setups; no replacement")
		}
		prior, digest = oldID, old.Digest
	}
	if dangling > 1 || dangling > 0 && prior != "" || len(known) > 0 && prior == "" && dangling == 0 {
		return "", "", errors.New("ambiguous Main supersession history; no replacement")
	}
	return prior, digest, nil
}
