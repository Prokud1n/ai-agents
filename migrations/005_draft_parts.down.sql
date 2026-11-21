DROP INDEX IF EXISTS task_feedback_source_message_idx;
ALTER TABLE task_feedback DROP COLUMN IF EXISTS source_message_id;

DROP INDEX IF EXISTS task_versions_draft_parts_idx;
ALTER TABLE task_versions DROP COLUMN IF EXISTS draft_part_message_ids;
