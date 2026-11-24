package ideas

import "errors"

var (
	ErrNotFound   = errors.New("idea not found")
	ErrValidation = errors.New("validation error")
	// ErrClosed is a change of an idea that is already done or dropped.
	ErrClosed = errors.New("idea is closed")
	// ErrNoCard: the model finished the run without a valid card.
	ErrNoCard = errors.New("no idea card")
)
