package ideas

import "errors"

var (
	ErrNotFound   = errors.New("idea not found")
	ErrValidation = errors.New("validation error")
	// ErrClosed is a change of an idea that is already done or dropped.
	ErrClosed = errors.New("idea is closed")
	// ErrNoCard: the model finished the run without a valid card.
	ErrNoCard = errors.New("no idea card")
	// ErrCardMismatch: the card of the approved version is not the approved draft (a stale run wrote it).
	ErrCardMismatch = errors.New("idea card does not match the approved draft")
	// ErrVersionStored: the card of a version that is already stored is not replaced (the run is stale).
	ErrVersionStored = errors.New("the draft version is already stored")
)
