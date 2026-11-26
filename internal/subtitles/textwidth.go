package subtitles

import "unicode/utf8"

// firaLineEm is the height of a line of Fira Sans Extra Condensed in its em (its ascent and descent): libass makes a
// line as tall as the font size, so the em is the size divided by it.
const (
	firaLineEm = 1.2
	// perMille: the advances below are in thousandths of the em.
	perMille = 1000
)

// firaAdvances are the advances of the capitals, digits and punctuation of Fira Sans Extra Condensed ExtraBold
// (FiraSansExtraCondensed-ExtraBold.ttf), in thousandths of the em: the hook of the trainer's style is measured by
// them, so its frame fits the text and its lines fill the width.
var firaAdvances = map[rune]int{
	'А': 511, 'Б': 508, 'В': 517, 'Г': 425, 'Д': 552, 'Е': 431, 'Ё': 431, 'Ж': 781, 'З': 456, 'И': 544,
	'Й': 544, 'К': 542, 'Л': 541, 'М': 670, 'Н': 550, 'О': 583, 'П': 550, 'Р': 507, 'С': 489, 'Т': 456,
	'У': 493, 'Ф': 714, 'Х': 501, 'Ц': 577, 'Ч': 522, 'Ш': 749, 'Щ': 779, 'Ъ': 610, 'Ы': 729, 'Ь': 507,
	'Э': 454, 'Ю': 800, 'Я': 515, 'A': 511, 'B': 517, 'C': 489, 'D': 553, 'E': 431, 'F': 404, 'G': 551,
	'H': 550, 'I': 252, 'J': 272, 'K': 553, 'L': 432, 'M': 670, 'N': 544, 'O': 583, 'P': 507, 'Q': 583,
	'R': 522, 'S': 485, 'T': 456, 'U': 536, 'V': 493, 'W': 719, 'X': 501, 'Y': 513, 'Z': 457, '0': 522,
	'1': 401, '2': 426, '3': 443, '4': 483, '5': 465, '6': 498, '7': 399, '8': 495, '9': 493, ' ': 177,
	'-': 351, '–': 440, '—': 670, '.': 223, ',': 217, ':': 223, ';': 223, '!': 221, '?': 418, '«': 574,
	'»': 574, '"': 425, '\'': 218, '(': 284, ')': 284, '%': 743, '+': 455, '/': 426, '&': 647, '№': 942,
	'*': 395,
}

// hookTextWidth is the width of a line of the hook at its full size, px: by the advances of the font for the accent
// font (the characters it has), otherwise by hook.char_width.
func (s *Style) hookTextWidth(text string) float64 {
	h := &s.Hook
	size := float64(h.Size)

	if h.Font != defaultAccentFont {
		return float64(utf8.RuneCountInString(text)) * size * h.CharWidth
	}

	width := 0.0

	for _, r := range text {
		if advance, ok := firaAdvances[r]; ok {
			width += float64(advance) / perMille * size / firaLineEm
		} else {
			width += size * h.CharWidth
		}
	}

	return width
}
