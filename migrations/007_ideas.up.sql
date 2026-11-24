-- The idea backlog (T2.4). /idea makes a task of the marketer; every run of it writes a card of its version to
-- idea_cards (the model answers with the submit_idea tool, so the card is kept by fields, not as text). When the
-- trainer approves a version («В бэклог»), its card becomes an idea; one idea per task: a repeated press or a
-- redelivery does not make a duplicate.

CREATE TABLE idea_cards (
    task_id     bigint      NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    version     integer     NOT NULL CHECK (version > 0),
    title       text        NOT NULL,
    summary     text        NOT NULL,                -- what the reel or post is about: 1-3 sentences
    format      text        NOT NULL CHECK (format IN ('talk', 'workout', 'carousel', 'other')),
    format_note text        NOT NULL DEFAULT '',     -- the format in words when it is 'other'
    hook        text        NOT NULL,                -- the first phrase
    -- The draft text made of the card (ideas.FormatCard): an idea is made only of a card whose content is the
    -- content of the approved version in task_versions, so a card of a stale run cannot reach the backlog.
    content     text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, version),
    CHECK (format = 'other' OR format_note = '')
);

-- id is the short number the trainer uses: /idea_done 5.
CREATE TABLE ideas (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id      bigint      NOT NULL UNIQUE,
    task_version integer     NOT NULL CHECK (task_version > 0), -- the approved version the card comes from
    title        text        NOT NULL,
    summary      text        NOT NULL,
    format       text        NOT NULL CHECK (format IN ('talk', 'workout', 'carousel', 'other')),
    format_note  text        NOT NULL DEFAULT '',
    hook         text        NOT NULL,
    source_text  text        NOT NULL,             -- the trainer's words after /idea
    -- new: in the backlog; planned: taken into a content plan (/plan); done: made; dropped: not needed.
    status       text        NOT NULL CHECK (status IN ('new', 'planned', 'done', 'dropped')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (format = 'other' OR format_note = ''),
    FOREIGN KEY (task_id, task_version) REFERENCES idea_cards (task_id, version) ON DELETE CASCADE
);

-- /ideas and the plan read the backlog by status.
CREATE INDEX ideas_status_idx ON ideas (status, id);
-- The backlog looks for approved idea tasks without an idea (an idea whose save failed after the approval).
CREATE INDEX tasks_approved_ideas_idx ON tasks (id) WHERE kind = 'idea' AND status = 'approved';
