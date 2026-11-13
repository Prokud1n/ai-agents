#!/usr/bin/env bash
# Запускает golangci-lint зафиксированной версии без установки.
# Единственное место, где задаётся версия: Makefile, git- и Claude-хуки, CI ходят через этот скрипт.
set -euo pipefail

GOLANGCI_LINT_VERSION=v2.15.0

exec go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}" "$@"
