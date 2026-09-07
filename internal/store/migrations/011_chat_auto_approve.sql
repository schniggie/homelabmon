-- Per-chat auto-approve toggle: when enabled for a session, the agent may
-- execute confirmation-gated actions (run_command, disruptive docker control,
-- deletions) without asking the user for each one.
ALTER TABLE chat_sessions ADD COLUMN auto_approve INTEGER NOT NULL DEFAULT 0;
