package core

// Stable command entry points retain the original v1 journal namespace and
// validation. Agent entry points bind a different adapter/namespace explicitly.
func openCommandSubmission(d string, s CommandSubmissionScope) (*composeJournal, error) {
	return commandAdmission.open(d, s)
}
func commandSubmissionStatus(j *composeJournal, s CommandSubmissionScope) (CommandSubmissionStatus, composeRecord) {
	return commandAdmission.status(j, s)
}
func InspectCommandSubmission(d string, s CommandSubmissionScope) (CommandSubmissionStatus, error) {
	return commandAdmission.inspect(d, s)
}
func PrepareCommandSubmission(d string, s CommandSubmissionScope, b []byte) (CommandSubmissionStatus, error) {
	return commandAdmission.prepare(d, s, b)
}
func ArmCommandSubmission(d string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	return commandAdmission.mark(d, s, id, "arm")
}
func CancelCommandSubmission(d string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	return commandAdmission.mark(d, s, id, "cancel")
}
func AttemptCommandSubmission(d string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	return commandAdmission.attempt(d, s, id)
}
func ReconcileCommandSubmission(d string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	return commandAdmission.reconcile(d, s, id)
}
func RetireCommandSubmission(d string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	return commandAdmission.retire(d, s, id)
}
func CommandSubmissionRendezvous(d string, s CommandSubmissionScope, l string) (ComposeRendezvous, error) {
	return commandAdmission.rendezvous(d, s, l)
}
func ExchangeCommandSubmission(d string, s CommandSubmissionScope, m, id, l string) (CommandSubmissionStatus, error) {
	return commandAdmission.exchange(d, s, m, id, l)
}
