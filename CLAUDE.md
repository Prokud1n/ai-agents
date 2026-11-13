# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Общие правила для агентов и код-стайл подключаются целиком:

@AGENTS.md
@CODE_STYLE.md

## Хуки Claude Code

В `.claude/settings.json` настроены хуки, которые следят за код-стайлом автоматически:

- после каждой правки `.go`-файла он форматируется (`golangci-lint fmt`);
- перед завершением ответа, если менялись `.go`-файлы, запускается `golangci-lint run ./...`. Если есть ошибки — исправь их, не отключая линтер.
