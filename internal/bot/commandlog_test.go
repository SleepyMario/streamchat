package bot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandLogStoresOnlyMinimalCommandRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot-commands.jsonl")
	log, err := OpenCommandLog(path)
	if err != nil {
		t.Fatal(err)
	}
	record := CommandRecord{Time: time.Unix(100, 0).UTC(), Platform: "twitch", Command: "!commands", Succeeded: true}
	if err = log.WriteCommand(record); err != nil {
		t.Fatal(err)
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["platform"] != "twitch" || fields["command"] != "!commands" || fields["succeeded"] != true {
		t.Fatalf("unexpected command record: %s", payload)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}
