package streamprobe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeReportsDimensionsAndOffline(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '{\"streams\":[{\"width\":1920,\"height\":1080,\"bit_rate\":\"6000000\"}]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := New(bin, "rtsp://private/pc/stream", time.Hour)
	p.check(context.Background())
	if got := p.State(); !got.Online || got.Width != 1920 || got.Height != 1080 || got.Bitrate != 6000000 {
		t.Fatalf("state=%+v", got)
	}
	p = New(bin, "", time.Hour)
	p.check(context.Background())
	if got := p.State(); got.Online || got.Width != 0 || got.Height != 0 {
		t.Fatalf("offline state=%+v", got)
	}
}

func TestParseInboundBytesIsRestrictedToConfiguredMediaPath(t *testing.T) {
	metrics := `paths_inbound_bytes{name="other",state="ready"} 9000
paths_inbound_bytes{name="pc/stream",state="ready"} 123456
paths_outbound_bytes{name="pc/stream",state="ready"} 654321
`
	got, err := parseInboundBytes(strings.NewReader(metrics), "pc/stream")
	if err != nil || got != 123456 {
		t.Fatalf("bytes=%d err=%v", got, err)
	}
}
