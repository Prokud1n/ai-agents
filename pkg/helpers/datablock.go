// Package helpers holds small generic helpers without business logic.
package helpers

import (
	"regexp"
	"strings"
)

// ClosingTag matches a closing tag of a data block in a prompt as a model might read it: in any case and with
// spaces anywhere inside the tag — </tag>, < /tag >, </ TAG>.
func ClosingTag(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)<\s*/\s*` + regexp.QuoteMeta(tag) + `\s*>`)
}

// EscapeClosingTag trims the text and breaks every closing tag of its block in it (closing is ClosingTag(tag)), so
// a text put into the block cannot end it early.
func EscapeClosingTag(text string, closing *regexp.Regexp, tag string) string {
	return closing.ReplaceAllLiteralString(strings.TrimSpace(text), "<\\/"+tag+">")
}
