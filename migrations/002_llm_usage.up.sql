-- One row per Claude API call made by an agent.
CREATE TABLE llm_usage (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent              TEXT        NOT NULL,
    model              TEXT        NOT NULL,
    request_id         TEXT        NOT NULL DEFAULT '',
    input_tokens       BIGINT      NOT NULL,
    output_tokens      BIGINT      NOT NULL,
    cache_read_tokens  BIGINT      NOT NULL,
    cache_write_tokens BIGINT      NOT NULL,
    -- USD; NULL when the model has no price in LLM_PRICES.
    cost_usd           NUMERIC(20, 9),
    duration_ms        BIGINT      NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX llm_usage_created_at_idx ON llm_usage (created_at);
