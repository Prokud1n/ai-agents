-- Video drafts of the editor (T3.4b): the version keeps the S3 key of its video; content is then the video's
-- description. The plan, the decisions log and the source transcript lie next to the video in S3 (editjob.Files),
-- so the next step (revisions of a video by text) can cut it again without a new transcription.
ALTER TABLE task_versions ADD COLUMN media_key text NOT NULL DEFAULT ''; -- '': a text draft
