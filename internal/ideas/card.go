package ideas

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of the card fields, in characters (the same as in the tool schema).
const (
	MaxTitleLength      = 80
	MaxSummaryLength    = 600
	MaxFormatNoteLength = 60
	MaxHookLength       = 200
)

// formatNames are the formats as the trainer reads them.
var formatNames = map[Format]string{
	TalkFormat:     "разговорный рилс",
	WorkoutFormat:  "рилс-тренировка",
	CarouselFormat: "карусель",
	OtherFormat:    "другое",
}

// CheckError is a card that failed the checks; Message goes back to the model.
type CheckError struct {
	Message string
}

func (e *CheckError) Error() string {
	return "invalid idea card: " + e.Message
}

func (e *CheckError) Unwrap() error {
	return ErrValidation
}

// ValidateCard normalizes the card (spaces and line breaks collapse to one space) and checks it: every field is set
// and fits its limit, the format is known (OtherFormat needs FormatNote), no control or invisible characters.
func ValidateCard(card *Card) (Card, error) {
	clean := Card{
		Title:      normalize(card.Title),
		Summary:    normalize(card.Summary),
		Format:     card.Format,
		FormatNote: normalize(card.FormatNote),
		Hook:       normalize(card.Hook),
	}

	if clean.Format != OtherFormat {
		clean.FormatNote = ""
	}

	if err := checkField("title", clean.Title, MaxTitleLength, true); err != nil {
		return Card{}, err
	}

	if err := checkField("summary", clean.Summary, MaxSummaryLength, true); err != nil {
		return Card{}, err
	}

	if err := checkField("hook", clean.Hook, MaxHookLength, true); err != nil {
		return Card{}, err
	}

	if !slices.Contains(Formats, clean.Format) {
		return Card{}, &CheckError{Message: fmt.Sprintf("format %q неизвестен: talk, workout, carousel или other", clean.Format)}
	}

	if err := checkField("format_note", clean.FormatNote, MaxFormatNoteLength, clean.Format == OtherFormat); err != nil {
		return Card{}, err
	}

	return clean, nil
}

func checkField(name, value string, limit int, required bool) error {
	switch {
	case required && value == "":
		return &CheckError{Message: name + " пустое"}
	case utf8.RuneCountInString(value) > limit:
		return &CheckError{Message: fmt.Sprintf("%s длиннее %d символов", name, limit)}
	case strings.ContainsFunc(value, invisible):
		return &CheckError{Message: name + " содержит управляющие или невидимые символы"}
	}

	return nil
}

// normalize collapses any whitespace (line breaks included) to single spaces.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func invisible(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// FormatName is the format as the trainer reads it: «карусель», «другое — сторис с опросом».
func FormatName(card *Card) string {
	name, ok := formatNames[card.Format]
	if !ok {
		name = string(card.Format)
	}

	if card.Format == OtherFormat && card.FormatNote != "" {
		return name + " — " + card.FormatNote
	}

	return name
}

// FormatCard is the text of the card's draft (plain text: Telegram shows it as is).
func FormatCard(card *Card) string {
	return "Идея: " + card.Title + "\n\n" +
		"О чём: " + card.Summary + "\n" +
		"Формат: " + FormatName(card) + "\n" +
		"Хук: «" + card.Hook + "»"
}
