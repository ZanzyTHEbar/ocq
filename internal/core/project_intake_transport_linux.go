package core

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
)

// LocalProjectIntakeRendezvous returns the short-lived capability for one exact
// launch. It contains no request; a stale entry is not proof of a live peer.
func LocalProjectIntakeRendezvous(stateDir, launch string) (ComposeRendezvous, error) {
	if !isValidHexLen(launch, 64) {
		return ComposeRendezvous{}, faults.New(faults.CodeValidationFailed, "invalid intake launch identifier")
	}
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ComposeRendezvous{}, err
	}
	defer j.close()
	b, err := j.read("rendezvous.json", 4096)
	var entry ComposeRendezvous
	if errors.Is(err, syscall.ENOENT) || (err == nil && (json.Unmarshal(b, &entry) != nil || entry.Launch != launch || entry.Port < 1 || entry.Port > 65535 || !isValidHexLen(entry.Capability, 64))) {
		return ComposeRendezvous{}, faults.New(faults.CodeNotFound, "exact intake rendezvous not found")
	}
	if err != nil {
		return ComposeRendezvous{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return entry, nil
}

// ReceiveLocalProjectIntake authenticates one loopback peer and accepts a
// 4-byte big-endian length followed by exact request bytes; it never dispatches.
func ReceiveLocalProjectIntake(stateDir, kind, launch string) (ProjectIntakeStatus, error) {
	return receiveLocalProjectIntake(stateDir, kind, launch, false)
}

// ReceiveLocalProjectIntakeText uses an ASCII decimal length plus newline for
// clients whose native socket only writes UTF-8 strings. Payload bytes are still
// retained exactly and the binary-framed command remains unchanged.
func ReceiveLocalProjectIntakeText(stateDir, kind, launch string) (ProjectIntakeStatus, error) {
	return receiveLocalProjectIntake(stateDir, kind, launch, true)
}

func receiveLocalProjectIntake(stateDir, kind, launch string, textFrame bool) (ProjectIntakeStatus, error) {
	if !isValidHexLen(launch, 64) || (kind != "registration" && kind != "submission") {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "invalid intake launch or kind")
	}
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, _ := intakeStatus(j)
	if status.State != "idle" {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "unresolved project intake blocks preparation")
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		var size uint32
		if textFrame {
			var digits strings.Builder
			var b [1]byte
			for {
				if _, err := io.ReadFull(conn, b[:]); err != nil {
					return faults.New(faults.CodeValidationFailed, "incomplete intake frame")
				}
				if b[0] == '\n' {
					break
				}
				if b[0] < '0' || b[0] > '9' || digits.Len() >= 6 {
					return faults.New(faults.CodeValidationFailed, "invalid intake frame size")
				}
				digits.WriteByte(b[0])
			}
			n, err := strconv.ParseUint(digits.String(), 10, 32)
			if err != nil || digits.String() != strconv.FormatUint(n, 10) {
				return faults.New(faults.CodeValidationFailed, "invalid intake frame size")
			}
			size = uint32(n)
		} else {
			var header [4]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return faults.New(faults.CodeValidationFailed, "incomplete intake frame")
			}
			size = binary.BigEndian.Uint32(header[:])
		}
		if size == 0 || size > composeLimit {
			return faults.New(faults.CodeValidationFailed, "invalid intake frame size")
		}
		raw := make([]byte, int(size))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return faults.New(faults.CodeValidationFailed, "incomplete intake frame")
		}
		key, err := intakeKey(kind, raw)
		if err != nil {
			return faults.New(faults.CodeValidationFailed, "invalid project intake request")
		}
		status, err = prepareIntakeLocked(j, kind, key, raw)
		if err != nil {
			return err
		}
		// An acknowledgement failure never removes the durable intent.
		_, _ = fmt.Fprintln(conn, "PREPARED "+status.ID)
		return nil
	})
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	return status, nil
}

// PreviewLocalProjectIntake sends a one-byte kind (R or S), 4-byte big-endian
// length and exact retained bytes to one authenticated peer. It supports
// recovery in every valid retained state and never dispatches.
func PreviewLocalProjectIntake(stateDir, id, launch string) (ProjectIntakeStatus, error) {
	return previewLocalProjectIntake(stateDir, id, launch, false)
}

// ReviewLocalProjectIntake adds exact parser-selected embedded bytes/digests to
// the private preview. The original raw protocol and all mutation routes stay
// unchanged. The larger bounded frame accounts for escaped duplicate bytes.
func ReviewLocalProjectIntake(stateDir, id, launch string) (ProjectIntakeStatus, error) {
	return previewLocalProjectIntake(stateDir, id, launch, true)
}

func previewLocalProjectIntake(stateDir, id, launch string, review bool) (ProjectIntakeStatus, error) {
	if !isValidHexLen(id, 64) || !isValidHexLen(launch, 64) {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "invalid intake preview identifier")
	}
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, _ := intakeStatus(j)
	if status.ID != id || !intakePreviewable(status.State) {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "exact retained intake unavailable")
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		current, r := intakeStatus(j)
		if current.ID != id || !intakePreviewable(current.State) {
			return faults.New(faults.CodeValidationFailed, "exact retained intake unavailable")
		}
		var header [5]byte
		header[0] = 'R'
		if r.Kind == "submission" {
			header[0] = 'S'
		}
		payload := r.Request
		if review {
			var err error
			payload, err = projectIntakeReview(r.Kind, r.Request)
			if err != nil || len(payload) > 8*composeLimit {
				return faults.New(faults.CodeValidationFailed, "private intake review unavailable")
			}
		}
		binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
		if _, err := conn.Write(header[:]); err != nil {
			return faults.New(faults.CodeRuntimeFailed, "intake preview connection failed")
		}
		if _, err := conn.Write(payload); err != nil {
			return faults.New(faults.CodeRuntimeFailed, "intake preview connection failed")
		}
		return nil
	})
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	return status, nil
}

func intakePreviewable(state string) bool {
	return state == "prepared" || state == "armed" || state == "attempted" || state == "completed" || state == "cancelled"
}
