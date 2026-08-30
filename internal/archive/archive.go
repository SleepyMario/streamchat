package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	_ "modernc.org/sqlite"
)

const CurrentSchemaVersion = 1

var migrations = []string{
	`CREATE TABLE messages (
        platform TEXT NOT NULL,
        channel_id TEXT NOT NULL DEFAULT '',
        message_id TEXT NOT NULL,
        event_timestamp TEXT NOT NULL,
        user_id TEXT NOT NULL DEFAULT '',
        display_name TEXT NOT NULL DEFAULT '',
        message_text TEXT NOT NULL DEFAULT '',
        event_type TEXT NOT NULL,
        moderation_state TEXT NOT NULL DEFAULT '',
        emotes_json TEXT NOT NULL DEFAULT '[]',
        metadata_json TEXT NOT NULL DEFAULT '{}',
        normalized_json TEXT NOT NULL,
        archived_at TEXT NOT NULL,
        PRIMARY KEY (platform, message_id)
    );
    CREATE INDEX messages_platform_timestamp ON messages(platform, event_timestamp);
    CREATE INDEX messages_channel_timestamp ON messages(platform, channel_id, event_timestamp);`,
}

type Archive struct {
	db   *sql.DB
	path string
}

type PlatformCount struct {
	Platform string
	Count    int64
}

// MessageReference is the provider identity and event time needed for remote
// moderation. Reading these references never changes archived history.
type MessageReference struct {
	ID        string
	Timestamp time.Time
}

type AuthorReference struct {
	ID, DisplayName string
}

type Stats struct {
	SchemaVersion int
	Total         int64
	First         time.Time
	Last          time.Time
	Platforms     []PlatformCount
}

func Open(path string) (*Archive, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("create SQLite directory %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite archive %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	a := &Archive{db: db, path: path}
	if err = a.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite archive %s: %w", path, err)
	}
	if path != ":memory:" {
		if err = os.Chmod(path, 0600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure SQLite archive %s: %w", path, err)
		}
	}
	return a, nil
}

func (a *Archive) initialize(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err = tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, i+1, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *Archive) Close() error { return a.db.Close() }

// Store archives a normalized message. It returns false for an already-seen
// provider message ID so reconnects cannot duplicate either storage or relay.
func (a *Archive) Store(ctx context.Context, m chat.Message) (bool, error) {
	if err := m.Validate(); err != nil {
		return false, err
	}
	emotes, err := json.Marshal(m.Emotes)
	if err != nil {
		return false, err
	}
	metadata, err := json.Marshal(m.SafePlatformMetadata)
	if err != nil {
		return false, err
	}
	normalized, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	result, err := a.db.ExecContext(ctx, `INSERT INTO messages (
        platform, channel_id, message_id, event_timestamp, user_id, display_name,
        message_text, event_type, moderation_state, emotes_json, metadata_json,
        normalized_json, archived_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(platform, message_id) DO NOTHING`,
		m.Platform, m.ChannelID, m.ID, m.Timestamp.UTC().Format(time.RFC3339Nano),
		m.AuthorID, m.AuthorDisplayName, m.Text, m.EventType, moderationState(m),
		string(emotes), string(metadata), string(normalized), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("write SQLite archive: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// MessageIDsSince returns a point-in-time snapshot of distinct provider
// message IDs without loading archived message bodies.
func (a *Archive) MessageIDsSince(ctx context.Context, platform chat.Platform, since time.Time) ([]string, error) {
	references, err := a.MessageReferencesSince(ctx, platform, since)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(references))
	for _, reference := range references {
		ids = append(ids, reference.ID)
	}
	return ids, nil
}

// MessageReferencesSince returns a point-in-time snapshot of distinct
// provider message IDs and their earliest archived event timestamp.
func (a *Archive) MessageReferencesSince(ctx context.Context, platform chat.Platform, since time.Time) ([]MessageReference, error) {
	if platform == "" {
		return nil, errors.New("archive platform is required")
	}
	if since.IsZero() {
		return nil, errors.New("archive start time is required")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT TRIM(message_id) AS provider_id, MIN(event_timestamp)
        FROM messages
        WHERE platform = ? AND event_timestamp >= ? AND TRIM(message_id) <> ''
        GROUP BY provider_id
        ORDER BY MIN(event_timestamp) ASC`, platform, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("query archived message IDs: %w", err)
	}
	defer rows.Close()
	references := make([]MessageReference, 0)
	for rows.Next() {
		var id, timestamp string
		if err = rows.Scan(&id, &timestamp); err != nil {
			return nil, fmt.Errorf("read archived message ID: %w", err)
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp)
		if parseErr != nil {
			return nil, fmt.Errorf("read archived message timestamp: %w", parseErr)
		}
		references = append(references, MessageReference{ID: id, Timestamp: parsed})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read archived message IDs: %w", err)
	}
	return references, nil
}

// LatestAuthor resolves a displayed chat name to the provider identity most
// recently observed by the authoritative archive.
func (a *Archive) LatestAuthor(ctx context.Context, platform chat.Platform, displayName string) (AuthorReference, error) {
	displayName = strings.TrimSpace(displayName)
	if platform == "" || displayName == "" {
		return AuthorReference{}, errors.New("archive platform and display name are required")
	}
	var result AuthorReference
	err := a.db.QueryRowContext(ctx, `SELECT user_id, display_name FROM messages
        WHERE platform = ? AND LOWER(TRIM(display_name)) = LOWER(?) AND TRIM(user_id) <> ''
        ORDER BY event_timestamp DESC LIMIT 1`, platform, displayName).Scan(&result.ID, &result.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorReference{}, fmt.Errorf("%s user %q was not found in the Streamchat archive", platform, displayName)
	}
	if err != nil {
		return AuthorReference{}, fmt.Errorf("resolve archived author: %w", err)
	}
	return result, nil
}

func moderationState(m chat.Message) string {
	if m.EventType != chat.EventModeration {
		return ""
	}
	for _, value := range m.SafePlatformMetadata {
		v := strings.ToLower(value)
		switch {
		case strings.Contains(v, "delete") || strings.Contains(v, "tombstone"):
			return "deleted"
		case strings.Contains(v, "ban"):
			return "banned"
		}
	}
	return "moderation_event"
}

func (a *Archive) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&s.SchemaVersion); err != nil {
		return s, err
	}
	var first, last sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(event_timestamp), MAX(event_timestamp) FROM messages`).Scan(&s.Total, &first, &last); err != nil {
		return s, err
	}
	if first.Valid {
		s.First, _ = time.Parse(time.RFC3339Nano, first.String)
	}
	if last.Valid {
		s.Last, _ = time.Parse(time.RFC3339Nano, last.String)
	}
	rows, err := a.db.QueryContext(ctx, `SELECT platform, COUNT(*) FROM messages GROUP BY platform`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PlatformCount
		if err = rows.Scan(&p.Platform, &p.Count); err != nil {
			return s, err
		}
		s.Platforms = append(s.Platforms, p)
	}
	sort.Slice(s.Platforms, func(i, j int) bool { return s.Platforms[i].Platform < s.Platforms[j].Platform })
	return s, rows.Err()
}
