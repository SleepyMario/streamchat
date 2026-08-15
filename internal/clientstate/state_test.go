package clientstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathUsesXDGStateHomeAndHomeFallback(t *testing.T) {
	path, err := defaultPath(func(key string) string {
		if key == "XDG_STATE_HOME" {
			return "/tmp/example-state"
		}
		return ""
	}, func() (string, error) { return "/home/example", nil })
	if err != nil || path != "/tmp/example-state/streamchat/client.json" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	path, err = defaultPath(func(string) string { return "" }, func() (string, error) { return "/home/example", nil })
	if err != nil || path != "/home/example/.local/state/streamchat/client.json" {
		t.Fatalf("fallback path=%q err=%v", path, err)
	}
}

func TestSaveLoadIsAtomicPrivateAndContainsOnlyRuntimeState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "streamchat", filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	store := New(path)
	if err := store.Save(State{LastOutboundTarget: "kick"}); err != nil {
		t.Fatal(err)
	}
	if got := store.Load().LastOutboundTarget; got != "kick" {
		t.Fatalf("target=%q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"last_outbound_target\": \"kick\"\n}\n" {
		t.Fatalf("state=%q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0600 {
		t.Fatalf("permissions=%#o", permissions)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := directoryInfo.Mode().Perm(); permissions != 0700 {
		t.Fatalf("directory permissions=%#o", permissions)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".client-") {
			t.Fatalf("temporary state file remains: %s", entry.Name())
		}
	}
}

func TestMissingCorruptAndUnreadableStateFallBackToEmpty(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, filename)
	store := New(path)
	if got := store.Load(); got != (State{}) {
		t.Fatalf("missing state=%+v", got)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != (State{}) {
		t.Fatalf("corrupt state=%+v", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != (State{}) {
		t.Fatalf("unreadable state=%+v", got)
	}
}

func TestSecondSaveAtomicallyReplacesPriorState(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	store := New(path)
	if err := store.Save(State{LastOutboundTarget: "kick"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{LastOutboundTarget: "twitch"}); err != nil {
		t.Fatal(err)
	}
	if got := store.Load().LastOutboundTarget; got != "twitch" {
		t.Fatalf("target=%q", got)
	}
}
