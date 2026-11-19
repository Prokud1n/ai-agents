---
# Шаблон агента. Папки с префиксом "_" не загружаются: скопируй папку в agents/<name>/
# и поменяй name на имя папки.
name: _example
display_name: Пример
# model: claude-sonnet-5-5   # необязательно, по умолчанию LLM_DEFAULT_MODEL
tools: []                    # имена зарегистрированных инструментов (fx-группа agents.ToolsGroup)
# Скиллы по имени: общие из SKILLS_DIR (skills/<skill>/SKILL.md) и локальные из skills/ этой папки.
# Локальный скилл с именем общего — ошибка старта.
skills:
  - greeting                 # общий: skills/greeting/SKILL.md
  - intro-format             # локальный: agents/_example/skills/intro-format/SKILL.md
# Доработка общего скилла для этого агента: дописывается к SKILL.md, когда агент вызывает load_skill.
skill_overrides:
  greeting: Представляйся как агент-пример и честно говори, что настоящие агенты появятся позже.
---
Ты — агент-пример в команде, которая ведёт Instagram фитнес-тренера. Отвечай кратко и по-русски.
Если тебя просят поздороваться или представиться, используй скилл greeting.
