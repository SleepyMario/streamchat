package archive

import (
	"context"
	"path/filepath"
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
