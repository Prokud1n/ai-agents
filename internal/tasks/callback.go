package tasks

import (
	"fmt"
	"strconv"
	"strings"
)

// Action is a button under a draft.
type Action string

const (
	ApproveAction Action = "a"
	ReviseAction  Action = "r" // the gateway asks the trainer to reply to the draft with the revision
	RejectAction  Action = "x"
)

const (
	callbackPrefix = "t"
	callbackSep    = ":"
	callbackParts  = 4
	decimalBase    = 10
	int64Bits      = 64
	// MaxCallbackBytes is the Telegram limit of callback_data.
	MaxCallbackBytes = 64
)

// Callback is the callback_data of a draft button: "t:<task id>:<version>:<action>", at most 43 bytes.
type Callback struct {
	TaskID  int64
	Version int
	Action  Action
}

// EncodeCallback renders the callback_data of a button.
func EncodeCallback(c Callback) string {
	return callbackPrefix + callbackSep + strconv.FormatInt(c.TaskID, decimalBase) +
		callbackSep + strconv.Itoa(c.Version) + callbackSep + string(c.Action)
}

// ParseCallback parses callback_data made by EncodeCallback. Anything else, including the same values
// written differently (leading zeros, a sign), is ErrInvalidCallback (an ErrValidation): callback_data of
// other buttons must not be taken for a task action.
func ParseCallback(data string) (Callback, error) {
	invalid := fmt.Errorf("callback data %q: %w", data, ErrInvalidCallback)

	if len(data) > MaxCallbackBytes {
		return Callback{}, invalid
	}

	parts := strings.Split(data, callbackSep)
	if len(parts) != callbackParts || parts[0] != callbackPrefix {
		return Callback{}, invalid
	}

	taskID, err := strconv.ParseInt(parts[1], decimalBase, int64Bits)
	if err != nil || taskID <= 0 {
		return Callback{}, invalid
	}

	version, err := strconv.Atoi(parts[2])
	if err != nil || version <= 0 {
		return Callback{}, invalid
	}

	c := Callback{TaskID: taskID, Version: version, Action: Action(parts[3])}

	switch c.Action {
	case ApproveAction, ReviseAction, RejectAction:
	default:
		return Callback{}, invalid
	}

	if EncodeCallback(c) != data { // canonical form only
		return Callback{}, invalid
	}

	return c, nil
}

// Button is an inline button under a draft.
type Button struct {
	Text         string
	CallbackData string
}

// DraftButtons are the buttons «Одобрить / Правки / Отклонить» of a draft version.
func DraftButtons(taskID int64, version int) []Button {
	return []Button{
		{Text: "Одобрить", CallbackData: EncodeCallback(Callback{TaskID: taskID, Version: version, Action: ApproveAction})},
		{Text: "Правки", CallbackData: EncodeCallback(Callback{TaskID: taskID, Version: version, Action: ReviseAction})},
		{Text: "Отклонить", CallbackData: EncodeCallback(Callback{TaskID: taskID, Version: version, Action: RejectAction})},
	}
}
