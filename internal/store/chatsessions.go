package store

import (
	"context"
	"database/sql"
	"time"
)

// ChatSession is a persisted conversation with an LLM-generated title.
type ChatSession struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	AutoApprove  bool      `json:"auto_approve"`
}

// ChatMessage is one persisted chat turn.
type ChatMessage struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Actions   string    `json:"actions"` // JSON array
	CreatedAt time.Time `json:"created_at"`
}

// AppendChatMessage stores one turn, creating the session row if needed.
func (s *Store) AppendChatMessage(ctx context.Context, sessionID, role, content, actionsJSON string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chat_sessions (id) VALUES (?)
	`, sessionID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_messages (session_id, role, content, actions) VALUES (?, ?, ?, ?)
	`, sessionID, role, content, actionsJSON); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE chat_sessions SET message_count = message_count + 1, updated_at = datetime('now') WHERE id = ?
	`, sessionID)
	return err
}

// ListChatSessions returns sessions newest-first.
func (s *Store) ListChatSessions(ctx context.Context, limit int) ([]ChatSession, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, created_at, updated_at, message_count, auto_approve
		FROM chat_sessions ORDER BY updated_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChatSession
	for rows.Next() {
		var cs ChatSession
		var createdAt, updatedAt string
		var autoApprove int
		if err := rows.Scan(&cs.ID, &cs.Title, &createdAt, &updatedAt, &cs.MessageCount, &autoApprove); err != nil {
			return nil, err
		}
		cs.CreatedAt = parseTime(createdAt)
		cs.UpdatedAt = parseTime(updatedAt)
		cs.AutoApprove = autoApprove != 0
		list = append(list, cs)
	}
	return list, nil
}

// GetChatSession returns one session, or nil if it does not exist yet.
func (s *Store) GetChatSession(ctx context.Context, sessionID string) (*ChatSession, error) {
	var cs ChatSession
	var createdAt, updatedAt string
	var autoApprove int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, title, created_at, updated_at, message_count, auto_approve
		FROM chat_sessions WHERE id = ?
	`, sessionID).Scan(&cs.ID, &cs.Title, &createdAt, &updatedAt, &cs.MessageCount, &autoApprove)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cs.CreatedAt = parseTime(createdAt)
	cs.UpdatedAt = parseTime(updatedAt)
	cs.AutoApprove = autoApprove != 0
	return &cs, nil
}

// GetChatAutoApprove reports whether the session has auto-approve enabled.
// Sessions that do not exist yet are not auto-approved.
func (s *Store) GetChatAutoApprove(ctx context.Context, sessionID string) (bool, error) {
	sess, err := s.GetChatSession(ctx, sessionID)
	if err != nil || sess == nil {
		return false, err
	}
	return sess.AutoApprove, nil
}

// SetChatAutoApprove toggles auto-approve for a session, creating the session
// row if the toggle is flipped before the first message is sent.
func (s *Store) SetChatAutoApprove(ctx context.Context, sessionID string, on bool) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chat_sessions (id) VALUES (?)
	`, sessionID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE chat_sessions SET auto_approve = ? WHERE id = ?
	`, boolToInt(on), sessionID)
	return err
}

// GetChatMessages returns the last messages of a session in chronological
// order (most recent `limit` turns).
func (s *Store) GetChatMessages(ctx context.Context, sessionID string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, actions, created_at FROM (
			SELECT id, session_id, role, content, actions, created_at
			FROM chat_messages WHERE session_id = ?
			ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC
	`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Actions, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(createdAt)
		list = append(list, m)
	}
	return list, nil
}

// SetSessionTitle sets the title only if it is not already set, so a racing
// title generation does not overwrite an existing one.
func (s *Store) SetSessionTitle(ctx context.Context, sessionID, title string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE chat_sessions SET title = ? WHERE id = ? AND title = ''
	`, title, sessionID)
	return err
}

func (s *Store) DeleteChatSession(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chat_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_sessions WHERE id = ?`, sessionID)
	return err
}
