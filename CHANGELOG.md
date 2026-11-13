# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Docker Compose stack: app, PostgreSQL with pgvector, Redis, MinIO; local Telegram Bot API server under the `telegram` profile.
- Multi-stage Dockerfile with a distroless non-root runtime image.
- Providers for PostgreSQL (pgxpool), Redis (go-redis) and S3 (minio-go) with connection checks on start.
- SQL migrations with golang-migrate, applied on start (`MIGRATIONS_RUN_ON_START`) or via `make migrate`.
- HTTP endpoints `/healthz` (liveness) and `/readyz` (readiness of PostgreSQL, Redis, S3).
- Make targets `up`, `down`, `logs`, `migrate`; CI job `compose-smoke`.
