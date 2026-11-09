package core

import (
	"errors"

	errbuilder "github.com/ZanzyTHEbar/errbuilder-go"
	"github.com/ZanzyTHEbar/faults-go"
)

// diagnosticFault keeps diagnostics that are persisted as job reasons or
// returned in projections byte-for-byte stable while exposing a fault and its
// cause through the normal error chain.
type diagnosticFault struct {
	text  string
	fault error
}

func (e diagnosticFault) Error() string { return e.text }
func (e diagnosticFault) Unwrap() error { return e.fault }

func diagnostic(code faults.Code, text string) error {
	return diagnosticFault{text: text, fault: faults.New(code, text)}
}

func diagnosticWrap(code faults.Code, text string, cause error) error {
	if cause == nil {
		return nil
	}
	return diagnosticFault{text: text + ": " + cause.Error(), fault: faults.Wrap(code, text, cause)}
}

// classifyDiagnostic adds a category to an unclassified internal diagnostic
// without changing its text or hiding an existing, more specific fault.
func classifyDiagnostic(code faults.Code, err error) error {
	var existing *errbuilder.ErrBuilder
	if err == nil || errors.As(err, &existing) {
		return err
	}
	return diagnosticFault{text: err.Error(), fault: faults.Wrap(code, err.Error(), err)}
}
