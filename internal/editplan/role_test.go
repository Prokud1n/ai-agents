package editplan

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roleFile is the role prompt of the editor the model decides by.
const roleFile = "../../agents/editor/agent.md"

// TestRolePromptSelectionLikeTrainer (T3.22): the role prompt has the rules of the trainer's selection — the climax of
// the last point stays, the introduction after the call goes, links and reactions and the spoken section headings are
// cut, names distorted by the recognition are corrected; the cleaning keeps the end of a kept sentence and the failed
// takes do not count against its floor (T3.26).
func TestRolePromptSelectionLikeTrainer(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(roleFile)
	require.NoError(t, err)

	role := string(raw)

	for _, rule := range []string{
		"→ кульминация: доказательство или история внутри последнего пункта",
		"→ вопрос зрителю и призыв",
		"- Кульминацию не вырезай",
		"- Самопрезентация и «подписывайся» после вопроса или призыва к зрителю",
		"Если тренер в подписи или правке просит оставить представление — оставь.",
		"- Короткие вставки-связки и реакции между мыслями",
		"Произнесённое чтение заголовка («ошибка номер два», «пункт",
		"вырежи из звука через remove_words (если в задаче можно\n  вырезать), не через cleanup_words",
		"Раздел можно ставить на\n  вырезанное чтение",
		"«в Новосибе» → «Новосибирске»",
		"- cleanup_words не трогают конец оставленного предложения или истории",
		"Неудачные дубли и повторы из подсказок в это не\n  считаются",
	} {
		assert.Contains(t, role, rule)
	}

	assert.NotContains(t, role, "Примеры, истории и повторы мысли внутри пунктов она вырезает",
		"the story of the last point is the climax")
}
