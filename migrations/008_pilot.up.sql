-- The pilot with the trainer (T4.3a, T4.5).
-- * feedback: the trainer's /feedback. A reply to a draft links it to the task and version of the draft. The source
--   message is the idempotency key: a redelivered /feedback is not saved twice.
-- * pilot_reports: the weeks whose report went to «Техничка»; a restart does not send it twice.
-- * onboarding_messages: the pinned greeting of each topic (/onboard): a repeated /onboard edits it instead of
--   posting a duplicate.

CREATE TABLE feedback (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id         bigint      NOT NULL,              -- Telegram user id
    author_name       text        NOT NULL DEFAULT '',   -- first name (or username) at the time of the feedback
    text              text        NOT NULL,
    truncated         boolean     NOT NULL DEFAULT false, -- the text was cut to the limit
    task_id           bigint      REFERENCES tasks (id) ON DELETE SET NULL, -- NULL: not a reply to a draft
    task_version      integer     CHECK (task_version > 0),
    topic             text        NOT NULL DEFAULT '',   -- gateway.Topic: PLANNING, SOURCES, PRODUCTION, TECH, OTHER
    topic_id          bigint      NOT NULL DEFAULT 0,
    source_chat_id    bigint      NOT NULL,
    source_message_id bigint      NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_chat_id, source_message_id)
);

-- The metrics and the weekly report read feedback of a period.
CREATE INDEX feedback_created_idx ON feedback (created_at);

-- week is the date of the scheduled report in TEAM_TIMEZONE.
CREATE TABLE pilot_reports (
    week    date        PRIMARY KEY,
    sent_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE onboarding_messages (
    chat_id    bigint      NOT NULL,
    topic      text        NOT NULL,                  -- gateway.Topic
    role       text        NOT NULL,                  -- the bot that sent the greeting: only it can edit it
    message_id bigint      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, topic)
);

-- The metrics read tasks of a period.
CREATE INDEX tasks_created_idx ON tasks (created_at);
