package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChatNotFound is returned when a chat does not exist.
var ErrChatNotFound = errors.New("chat not found")

// ChatMessage is one message in a chat: a user prompt or the assistant's reply.
type ChatMessage struct {
	ID   string    `json:"id"`
	Role string    `json:"role"` // "user" | "assistant"
	Text string    `json:"text"`
	Mode string    `json:"mode,omitempty"`
	Time time.Time `json:"time"`
}

// Chat is a persistent conversation between the user and the agents.
type Chat struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Mode     string        `json:"mode"`
	Created  time.Time     `json:"created"`
	Messages []ChatMessage `json:"messages"`
}

// ChatRepo persists chats and their messages in PostgreSQL.
type ChatRepo struct {
	pool *pgxpool.Pool
}

func NewChatRepo(pool *pgxpool.Pool) *ChatRepo {
	return &ChatRepo{pool: pool}
}

// Create inserts a new empty chat with the given mode.
func (r *ChatRepo) Create(ctx context.Context, mode string) (Chat, error) {
	c := Chat{ID: newChatID(), Title: "New chat", Mode: mode, Created: time.Now(), Messages: []ChatMessage{}}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO chats (id, title, mode, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`,
		c.ID, c.Title, c.Mode, c.Created)
	if err != nil {
		return Chat{}, err
	}
	return c, nil
}

// List returns chat summaries (without messages), most recently updated first.
func (r *ChatRepo) List(ctx context.Context) ([]Chat, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, title, mode, created_at FROM chats ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Chat, 0)
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.Mode, &c.Created); err != nil {
			return nil, err
		}
		c.Messages = nil
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one chat with all its messages in chronological order.
func (r *ChatRepo) Get(ctx context.Context, id string) (Chat, error) {
	var c Chat
	err := r.pool.QueryRow(ctx,
		`SELECT id, title, mode, created_at FROM chats WHERE id = $1`, id).
		Scan(&c.ID, &c.Title, &c.Mode, &c.Created)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Chat{}, ErrChatNotFound
		}
		return Chat{}, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, role, text, mode, created_at FROM chat_messages
		 WHERE chat_id = $1 ORDER BY created_at ASC, id ASC`, id)
	if err != nil {
		return Chat{}, err
	}
	defer rows.Close()

	c.Messages = make([]ChatMessage, 0)
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Text, &m.Mode, &m.Time); err != nil {
			return Chat{}, err
		}
		c.Messages = append(c.Messages, m)
	}
	return c, rows.Err()
}

// Delete removes a chat and (via ON DELETE CASCADE) its messages.
func (r *ChatRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrChatNotFound
	}
	return nil
}

// AppendMessage adds a message to a chat and bumps the chat's updated_at so the
// list reorders. It returns the stored message (with its generated id/time).
func (r *ChatRepo) AppendMessage(ctx context.Context, chatID, role, text, mode string) (ChatMessage, error) {
	m := ChatMessage{ID: newChatID(), Role: role, Text: text, Mode: mode, Time: time.Now()}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO chat_messages (id, chat_id, role, text, mode, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, chatID, m.Role, m.Text, m.Mode, m.Time)
	if err != nil {
		return ChatMessage{}, err
	}
	_, err = r.pool.Exec(ctx, `UPDATE chats SET updated_at = now() WHERE id = $1`, chatID)
	if err != nil {
		return ChatMessage{}, err
	}
	return m, nil
}

// SetTitle updates a chat's title.
func (r *ChatRepo) SetTitle(ctx context.Context, chatID, title string) error {
	_, err := r.pool.Exec(ctx, `UPDATE chats SET title = $2, updated_at = now() WHERE id = $1`, chatID, title)
	return err
}

// SetMode updates a chat's selected mode.
func (r *ChatRepo) SetMode(ctx context.Context, chatID, mode string) error {
	_, err := r.pool.Exec(ctx, `UPDATE chats SET mode = $2, updated_at = now() WHERE id = $1`, chatID, mode)
	return err
}

// chatID returns a random hex identifier for a chat or message.
func newChatID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
