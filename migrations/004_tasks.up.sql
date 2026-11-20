-- Agent tasks and the trainer's approval: a task is created from a Telegram message, runs through the queue,
-- and every run produces a new draft version that the trainer approves, sends back for revision or rejects.
-- PostgreSQL is the source of truth about statuses; the Redis queue only delivers task ids to workers.

CREATE TABLE tasks (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent             text        NOT NULL,              -- agent that does the task (agents/<name>)
    kind              text        NOT NULL,              -- task type or command, e.g. reels-script
    input             text        NOT NULL,              -- the trainer's request
    media             text[]      NOT NULL DEFAULT '{}', -- references to attached media (S3 keys, file ids)
    -- Source message; (chat, message) is the idempotency key: a redelivered message does not create
    -- a second task. Message ids are unique within a chat, so the topic is not part of the key.
    source_chat_id    bigint      NOT NULL,
    source_topic_id   bigint      NOT NULL DEFAULT 0,    -- 0: no forum topic; drafts go to this topic
    source_message_id bigint      NOT NULL,
    author_id         bigint      NOT NULL DEFAULT 0,    -- Telegram user id of the author
    status            text        NOT NULL CHECK (status IN (
        'queued', 'running', 'awaiting_approval', 'approved', 'rejected', 'failed'
    )),
    version           integer     NOT NULL DEFAULT 0 CHECK (version >= 0), -- latest draft version, 0: none yet
    attempts          integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0), -- runs of the current revision
    last_error        text        NOT NULL DEFAULT '',
    run_id            text        NOT NULL DEFAULT '',   -- random id of the current run: only its worker finishes it
    run_after         timestamptz NOT NULL DEFAULT now(), -- a queued task is not run earlier (retry backoff)
    enqueued_at       timestamptz NOT NULL DEFAULT now(), -- last time the task was put into the queue
    started_at        timestamptz,                        -- start of the current run; NULL when not running
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_chat_id, source_message_id)
);

-- The sweeper looks for queued tasks the queue lost and for running tasks whose worker died.
CREATE INDEX tasks_queued_idx ON tasks (enqueued_at) WHERE status = 'queued';
CREATE INDEX tasks_running_idx ON tasks (started_at) WHERE status = 'running';

CREATE TABLE task_versions (
    task_id           bigint      NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    version           integer     NOT NULL CHECK (version > 0),
    content           text        NOT NULL,
    -- Telegram message of the sent draft: NULL until it is sent, 0 when the notifier has no messages (log).
    draft_message_id  bigint,
    -- Delivery of the draft: a sender claims it by moving delivery_after forward (a lease while it sends,
    -- then the retry backoff); after the attempt limit the task fails.
    delivery_attempts integer     NOT NULL DEFAULT 0,
    delivery_after    timestamptz NOT NULL DEFAULT now(),
    delivery_note     text        NOT NULL DEFAULT '', -- shown above the draft, e.g. why a revision failed
    -- Earlier messages of this version (replaced by a resent draft, or duplicates): a reply to one of them
    -- is answered "reply to the latest draft" instead of "not found".
    old_draft_message_ids bigint[] NOT NULL DEFAULT '{}',
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, version)
);

-- A reply to a draft finds the task by the draft's message id.
CREATE INDEX task_versions_draft_message_idx ON task_versions (draft_message_id) WHERE draft_message_id > 0;
CREATE INDEX task_versions_old_draft_messages_idx ON task_versions USING gin (old_draft_message_ids);
-- The sweeper sends drafts that were never sent.
CREATE INDEX task_versions_undelivered_idx ON task_versions (delivery_after) WHERE draft_message_id IS NULL;

CREATE TABLE task_feedback (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id    bigint      NOT NULL,
    version    integer     NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('revise', 'reject')),
    text       text        NOT NULL DEFAULT '',
    author_id  bigint      NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (task_id, version) REFERENCES task_versions (task_id, version) ON DELETE CASCADE
);

CREATE INDEX task_feedback_task_idx ON task_feedback (task_id, version, created_at DESC);
