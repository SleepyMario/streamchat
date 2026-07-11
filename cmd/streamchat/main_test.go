package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDemoOfflineAndHelp(t *testing.T) {
	var out, err bytes.Buffer
	if c := run([]string{"demo", "--no-color"}, &out, &err); c != 0 {
		t.Fatalf("%d %s", c, err.String())
	}
	s := out.String()
	if strings.Count(s, "duplicate") != 0 || !strings.Contains(s, "[YT]") || !strings.Contains(s, "[KICK]") || !strings.Contains(s, "↪") || !strings.Contains(s, "[PAID") {
		t.Fatal(s)
	}
	out.Reset()
	if c := run([]string{"--help"}, &out, &err); c != 0 || !strings.Contains(out.String(), "kick subscribe") {
		t.Fatal(out.String())
	}
}
func TestEmptyConfigCheck(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, err bytes.Buffer
	if c := run([]string{"config", "check"}, &out, &err); c != 0 || !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("%d %s", c, err.String())
	}
}
