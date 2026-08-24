-- Persistent memory for the AI agent: every management action is recorded
-- per node, and the agent can store its own notes for future sessions.
CREATE TABLE IF NOT EXISTS memories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id    TEXT NOT NULL DEFAULT '',
    hostname   TEXT NOT NULL DEFAULT '',
    scope      TEXT NOT NULL DEFAULT 'global', -- node:<host_id> or global
    kind       TEXT NOT NULL DEFAULT 'action', -- action, note, observation
    title      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT 'agent',  -- agent (automatic), llm, user
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories (scope, id DESC);
CREATE INDEX IF NOT EXISTS idx_memories_time ON memories (created_at DESC);

-- Persisted chat sessions with LLM-generated titles.
CREATE TABLE IF NOT EXISTS chat_sessions (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    message_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    role       TEXT NOT NULL, -- user, assistant
    content    TEXT NOT NULL,
    actions    TEXT NOT NULL DEFAULT '[]', -- JSON array of tool actions
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_session ON chat_messages (session_id, id);
