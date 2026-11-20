-- Telegram link of drafts and revisions (teamchat):
-- * a draft longer than one Telegram message is sent in parts; the last part (with the buttons) stays in
--   draft_message_id, the earlier ones are kept here, so a reply to any part of the draft is a revision;
-- * a revision remembers the reply message it came from, so a redelivered reply does not revise twice.

ALTER TABLE task_versions ADD COLUMN draft_part_message_ids bigint[] NOT NULL DEFAULT '{}';

CREATE INDEX task_versions_draft_parts_idx ON task_versions USING gin (draft_part_message_ids);

ALTER TABLE task_feedback ADD COLUMN source_message_id bigint NOT NULL DEFAULT 0; -- 0: not from a message

CREATE UNIQUE INDEX task_feedback_source_message_idx ON task_feedback (task_id, source_message_id)
    WHERE source_message_id > 0;
