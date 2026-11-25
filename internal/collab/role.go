package collab

// Role is the provenance of a participant id: who stamped CreatedBy on an
// intent or FromParticipantID on a message. It answers "where did this row come
// from" only. It is deliberately not a permission, not a rank, and not a
// hierarchy: the ledger records provenance, and authorization is decided by the
// API's ownership rules.
type Role string

const (
	// RoleCommander is the project commander's participant id.
	RoleCommander Role = "commander"
	// RoleOperator is the person at the console.
	RoleOperator Role = "operator"
	// RoleExecutor is a running direction's participant id, as minted by
	// ParticipantID. It records that a direction produced the row; it never
	// decides who may read or change it.
	RoleExecutor Role = "executor"
	// RoleUnknown is an empty id or a value that is not a participant of this
	// project (legacy rows without CreatedBy, or a malformed id).
	RoleUnknown Role = "unknown"
)

// RoleOf classifies a participant id. The commander and operator are exact
// matches; an executor is recognized by reversing ParticipantID
// (IntentIDFromParticipant), so an empty id, a bare "pt_", or an unfamiliar
// string is unknown instead of being guessed from a prefix.
func RoleOf(participant string) Role {
	switch participant {
	case CommanderID:
		return RoleCommander
	case OperatorID:
		return RoleOperator
	}
	if _, ok := IntentIDFromParticipant(participant); ok {
		return RoleExecutor
	}
	return RoleUnknown
}
