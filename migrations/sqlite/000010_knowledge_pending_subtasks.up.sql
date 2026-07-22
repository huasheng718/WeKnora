ALTER TABLE knowledges
    ADD COLUMN pending_subtasks_count INTEGER NOT NULL DEFAULT 0;
