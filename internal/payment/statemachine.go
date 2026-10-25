package payment

import "fmt"

// validTransitions maps source status to allowed target statuses.
// The payment lifecycle begins at 'pending' and transitions once to a terminal state ('succeeded' or 'failed').
var validTransitions = map[Status]map[Status]bool{
	StatusPending: {
		StatusSucceeded: true,
		StatusFailed:    true,
	},
	StatusSucceeded: {}, // Terminal state: no outbound transitions
	StatusFailed:    {}, // Terminal state: no outbound transitions
}

// CanTransition returns true if the transition from current to target is legally allowed.
func CanTransition(current, target Status) bool {
	targets, exists := validTransitions[current]
	if !exists {
		return false
	}
	return targets[target]
}

// Transition validates and applies a state transition, returning an error for illegal moves.
func Transition(current, target Status) (Status, error) {
	if !CanTransition(current, target) {
		return current, fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, current, target)
	}
	return target, nil
}
