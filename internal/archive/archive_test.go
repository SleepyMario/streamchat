package archive

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func message(id string, platform chat.Platform) chat.Message {
	return chat.Message{ID: id, Platform: platform, ChannelID: "broadcast-1", Timestamp: time.Date(2026, 8, 14, 3, 4, 5, 0, time.UTC), AuthorID: "user-1", AuthorDisplayName: "Alice", Text: "hello", EventType: chat.EventMessage}
}

func TestSchemaInitializationAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "streamchat.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s, err := a.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != CurrentSchemaVersion || s.Total != 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
	if _, err = a.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (99, 'now')`); err != nil {
		t.Fatal(err)
	}
	if err = a.initialize(context.Background()); err == nil {
		t.Fatal("expected newer schema rejection")
	}
}

func TestDuplicateMessageHandlingAndPlatforms(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, m := range []chat.Message{message("kick-1", chat.PlatformKick), message("youtube-1", chat.PlatformYouTube)} {
		inserted, err := a.Store(context.Background(), m)
		if err != nil || !inserted {
			t.Fatalf("first store = %v, %v", inserted, err)
		}
	}
	inserted, err := a.Store(context.Background(), message("kick-1", chat.PlatformKick))
	if err != nil || inserted {
		t.Fatalf("duplicate store = %v, %v", inserted, err)
	}
	s, err := a.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 || len(s.Platforms) != 2 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestArchiveNormalizedJSONPreservesProviderNeutralRoles(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := message("kick-role", chat.PlatformKick)
	m.Roles = chat.NewRoleSet(chat.RoleModerator, chat.RoleSubscriber)
	if inserted, storeErr := a.Store(context.Background(), m); storeErr != nil || !inserted {
		t.Fatalf("store inserted=%v err=%v", inserted, storeErr)
	}
	var normalized string
	if err = a.db.QueryRow(`SELECT normalized_json FROM messages WHERE message_id = ?`, m.ID).Scan(&normalized); err != nil {
		t.Fatal(err)
	}
	var archived chat.Message
	if err = json.Unmarshal([]byte(normalized), &archived); err != nil {
		t.Fatal(err)
	}
	if !archived.Roles.Has(chat.RoleModerator) || !archived.Roles.Has(chat.RoleSubscriber) {
		t.Fatalf("archived roles=%v JSON=%s", archived.Roles, normalized)
	}
}

func TestWriteFailureIsReturned(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store(context.Background(), message("kick-1", chat.PlatformKick)); err == nil {
		t.Fatal("expected closed database write to fail")
	}
}

func TestMessageIDsSinceFiltersPlatformTimestampAndEmptyIDs(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, m := range []chat.Message{
		{ID: "old-kick", Platform: chat.PlatformKick, Timestamp: base.Add(-25 * time.Hour), EventType: chat.EventMessage},
		{ID: "recent-kick", Platform: chat.PlatformKick, Timestamp: base.Add(-2 * time.Hour), EventType: chat.EventMessage},
		{ID: " recent-kick ", Platform: chat.PlatformKick, Timestamp: base.Add(-time.Hour), EventType: chat.EventMessage},
		{ID: "recent-youtube", Platform: chat.PlatformYouTube, Timestamp: base.Add(-time.Hour), EventType: chat.EventMessage},
	} {
		if inserted, storeErr := a.Store(context.Background(), m); storeErr != nil || !inserted {
			t.Fatalf("store %q: inserted=%v err=%v", m.ID, inserted, storeErr)
		}
	}
	if _, err = a.db.ExecContext(context.Background(), `INSERT INTO messages (
        platform, message_id, event_timestamp, event_type, normalized_json, archived_at
    ) VALUES ('kick', '', ?, 'message', '{}', ?)`, base.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	ids, err := a.MessageIDsSince(context.Background(), chat.PlatformKick, base.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"recent-kick"}) {
		t.Fatalf("IDs=%v", ids)
	}
}
