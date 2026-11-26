package editplan

import (
	"slices"
	"strings"
)

// DeclinedKind is a kind of request the editor does not do: the trainer gets a prepared text for it, never the model's
// own words.
type DeclinedKind string

const (
	MusicDeclined   DeclinedKind = "music"
	ColorDeclined   DeclinedKind = "color"
	EffectsDeclined DeclinedKind = "effects"
	BRollDeclined   DeclinedKind = "broll"
	SpeedDeclined   DeclinedKind = "speed"
	BeautyDeclined  DeclinedKind = "beauty"
	ReorderDeclined DeclinedKind = "reorder"
)

// declinedTexts are the trainer's texts of the kinds, in the order the description lists them.
var declinedTexts = map[DeclinedKind]string{
	MusicDeclined:   "Музыку добавьте в Instagram при публикации — я её не накладываю.",
	ColorDeclined:   "Цвет и яркость я не меняю — их можно поправить фильтром в Instagram при публикации.",
	EffectsDeclined: "Эффекты и переходы я не добавляю — склейки остаются простыми.",
	BRollDeclined:   "Вставки из других видео и фото я не добавляю — монтирую только из присланного исходника.",
	SpeedDeclined:   "Ускорение и замедление я не делаю — видео идёт с обычной скоростью.",
	BeautyDeclined:  "Фильтры красоты и ретушь я не накладываю — их можно добавить в Instagram при публикации.",
	ReorderDeclined: "Переставлять фрагменты местами я пока не умею — порядок остаётся как в исходнике.",
}

// declinedOrder is the order of the texts in the description, whatever the order of the model's list.
var declinedOrder = []DeclinedKind{
	MusicDeclined, ColorDeclined, EffectsDeclined, BRollDeclined, SpeedDeclined, BeautyDeclined, ReorderDeclined,
}

// DeclinedText is the texts of the declined requests as one paragraph; "" without them.
func DeclinedText(declined []string) string {
	return strings.Join(declined, " ")
}

// checkDeclined: every kind is known, and there is the trainer's caption or revision to decline anything of.
func checkDeclined(req *Request, kinds []DeclinedKind) error {
	if len(kinds) == 0 {
		return nil
	}

	text := req.Caption
	if req.IsRevision() {
		text = req.Revision
	}

	if strings.TrimSpace(text) == "" {
		return checkFailedf("declined — только для просьб тренера из подписи или правки, а их нет: не указывай его")
	}

	for _, k := range kinds {
		if _, ok := declinedTexts[k]; !ok {
			return checkFailedf("в declined неизвестная категория %q: music, color, effects, broll, speed, beauty или reorder",
				string(k))
		}
	}

	return nil
}

// declinedLines are the texts of the kinds (checked), once each, in declinedOrder; nil without them.
func declinedLines(kinds []DeclinedKind) []string {
	var lines []string

	for _, k := range declinedOrder {
		if slices.Contains(kinds, k) {
			lines = append(lines, declinedTexts[k])
		}
	}

	return lines
}
