package terminalui

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/chattercolor"
	"github.com/SleepyMario/streamchat/internal/emote"
	"github.com/SleepyMario/streamchat/internal/render"
	"github.com/mattn/go-runewidth"
)

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func plainTerminalOutput(value string) string {
	return ansiSequence.ReplaceAllString(value, "")
}

func TestScreenIncomingOutputPreservesInputAndTarget(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plainTerminalOutput(output.String()), "[NONE] > ") {
		t.Fatalf("missing initial target: %q", output.String())
	}
	for _, r := range "hello everyone" {
		screen.Feed(r)
	}
	screen.SetTarget("kick")
	writer := screen.Writer(&output)
	if _, err := io.WriteString(writer, "[KICK] viewer  incoming\n"); err != nil {
		t.Fatal(err)
	}
	if screen.Text() != "hello everyone" {
		t.Fatalf("input was lost: %q", screen.Text())
	}
	got := plainTerminalOutput(output.String())
	if !strings.Contains(got, "[KICK] viewer  incoming") || !strings.Contains(got, "[KICK] > hello everyone") {
		t.Fatalf("chat/input redraw missing: %q", got)
	}
}

func TestFixedUIAndAutocompleteDoNotUseChatterPalette(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "/c" {
		screen.Feed(r)
	}
	for _, color := range chattercolor.Palette() {
		if strings.Contains(output.String(), color.ANSI) {
			t.Fatalf("fixed UI used chatter color %s: %q", color.Name, output.String())
		}
	}
}

func TestChatterAssignmentsSurviveResizeScrollCleanAndEmoteRedraw(t *testing.T) {
	var output bytes.Buffer
	width := 40
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return width }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	allocator := chattercolor.NewAllocator()
	formatter := render.New(io.Discard, render.Options{
		Color:              true,
		ChatColorMode:      chattercolor.ModeLine,
		ChatterColorSource: allocator,
		Emotes: func(chat.Platform, chat.Emote) (string, bool) {
			return "/cache/emote.img", true
		},
	})
	first := chat.Message{Platform: chat.PlatformKick, AuthorID: "1", AuthorDisplayName: "first", Text: "[emote:7:wave]", Emotes: []chat.Emote{{ID: "7", Name: "wave", URL: "https://example.invalid/7", Start: 0, End: 13}}}
	appendMessage := func(id string, message chat.Message) {
		t.Helper()
		copyOfMessage := message
		if err := screen.AppendMessage(DisplayMessage{ID: id, Platform: string(message.Platform), Author: message.AuthorDisplayName, Render: func() emote.Line {
			return formatter.Format(copyOfMessage)
		}}); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("first", first)
	width = 32
	screen.Redraw()
	for index := 0; index < 6; index++ {
		appendMessage(fmt.Sprintf("repeat-%d", index), first)
	}
	if removed, err := screen.CleanAll(); err != nil || removed != 7 {
		t.Fatalf("clean removed=%d err=%v", removed, err)
	}
	second := chat.Message{Platform: chat.PlatformKick, AuthorID: "2", AuthorDisplayName: "second", Text: "after clean"}
	appendMessage("second", second)
	appendMessage("first-again", first)
	screen.mu.Lock()
	secondLine := screen.messages[0].rendered().Text
	firstAgainLine := screen.messages[1].rendered().Text
	screen.mu.Unlock()
	if !strings.HasPrefix(secondLine, chattercolor.Palette()[1].ANSI) {
		t.Fatalf("clean/redraw reset assignment sequence: %q", secondLine)
	}
	if !strings.HasPrefix(firstAgainLine, chattercolor.Palette()[0].ANSI) {
		t.Fatalf("first chatter lost original assignment: %q", firstAgainLine)
	}
	if len(backend.latest()) == 0 {
		t.Fatal("emote redraw path was not exercised")
	}
}

func TestProviderReceiptEchoIsColoredAndReplacedByAuthoritativeInboundMessage(t *testing.T) {
	screen := newScreen(io.Discard, func() int { return 60 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	allocator := chattercolor.NewAllocator()
	formatter := render.New(io.Discard, render.Options{Color: true, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: allocator})
	appendMessage := func(message chat.Message, provisional bool) {
		t.Helper()
		copyOfMessage := message
		if err := screen.AppendMessage(DisplayMessage{ID: message.ID, Platform: string(message.Platform), Author: message.AuthorDisplayName, Provisional: provisional, Render: func() emote.Line {
			return formatter.Format(copyOfMessage)
		}}); err != nil {
			t.Fatal(err)
		}
	}
	local := chat.Message{ID: "provider-message-id", Platform: chat.PlatformKick, AuthorID: "100", AuthorDisplayName: "you", Text: "sent from Streamchat", EventType: chat.EventMessage}
	appendMessage(local, true)
	screen.mu.Lock()
	localLine := screen.messages[0].rendered().Text
	screen.mu.Unlock()
	if !strings.HasPrefix(localLine, chattercolor.Palette()[0].ANSI) || !strings.Contains(render.Sanitize(localLine), "sent from Streamchat") {
		t.Fatalf("local echo=%q", localLine)
	}
	confirmed := local
	confirmed.AuthorDisplayName = "Streamer"
	appendMessage(confirmed, false)
	screen.mu.Lock()
	if len(screen.messages) != 1 || screen.messages[0].Provisional {
		t.Fatalf("messages=%+v", screen.messages)
	}
	confirmedLine := screen.messages[0].rendered().Text
	screen.mu.Unlock()
	if !strings.HasPrefix(confirmedLine, chattercolor.Palette()[0].ANSI) || !strings.Contains(render.Sanitize(confirmedLine), "Streamer") {
		t.Fatalf("confirmed=%q", confirmedLine)
	}
	twitch := chat.Message{ID: "twitch-message", Platform: chat.PlatformTwitch, AuthorID: "200", AuthorDisplayName: "TwitchUser", Text: "hello", EventType: chat.EventMessage}
	appendMessage(twitch, false)
	screen.mu.Lock()
	twitchLine := screen.messages[1].rendered().Text
	screen.mu.Unlock()
	if !strings.HasPrefix(twitchLine, chattercolor.Palette()[1].ANSI) {
		t.Fatalf("provider-neutral second assignment=%q", twitchLine)
	}
}

func TestAuthoritativeInboundMessageWinsIfItArrivesBeforeLocalReceipt(t *testing.T) {
	screen := newScreen(io.Discard, func() int { return 40 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	confirmed := DisplayMessage{ID: "same", Platform: "kick", Author: "Streamer", Line: "authoritative"}
	if err := screen.AppendMessage(confirmed); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(DisplayMessage{ID: "same", Platform: "kick", Author: "you", Line: "local", Provisional: true}); err != nil {
		t.Fatal(err)
	}
	if len(screen.messages) != 1 || screen.messages[0].Line != "authoritative" || screen.messages[0].Provisional {
		t.Fatalf("messages=%+v", screen.messages)
	}
}

func TestKickBotAuthoritativeMessageReplacesOnlyMatchingProvisionalEcho(t *testing.T) {
	screen := newScreen(io.Discard, func() int { return 60 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	provisional := DisplayMessage{ID: "overlapping-provider-id", Platform: "kick", Author: "you", Line: "outbound", Provisional: true}
	if err := screen.AppendMessage(provisional); err != nil {
		t.Fatal(err)
	}
	bot := DisplayMessage{ID: "overlapping-provider-id", Platform: "kick", Author: "BotRix", Line: "[KICK] [M] BotRix bot response"}
	if err := screen.AppendMessage(bot); err != nil {
		t.Fatal(err)
	}
	if len(screen.messages) != 1 || screen.messages[0].Provisional || screen.messages[0].Author != "BotRix" || screen.messages[0].Line != bot.Line {
		t.Fatalf("bot was hidden or duplicated during reconciliation: %+v", screen.messages)
	}

	otherBot := DisplayMessage{ID: "independent-bot-id", Platform: "kick", Author: "KickBot", Line: "[KICK] KickBot another response"}
	if err := screen.AppendMessage(otherBot); err != nil {
		t.Fatal(err)
	}
	if len(screen.messages) != 2 || screen.messages[1].Author != "KickBot" {
		t.Fatalf("unrelated bot was mistaken for a self echo: %+v", screen.messages)
	}
}

func TestRestoredCanonicalTargetIsShownOnInitialDrawAndSurvivesRedraws(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	screen.SetTarget("kick")
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	initial := plainTerminalOutput(output.String())
	if !strings.Contains(initial, "[KICK] > ") || strings.Contains(initial, "[NONE] > ") {
		t.Fatalf("initial output=%q", initial)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Title", Category: "Category", ViewerCount: 2, Live: true})
	screen.Redraw()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Render: func() emote.Line {
		return emote.Line{Text: "[KICK] viewer  :wave:", GraphicalText: "[KICK] viewer     ", Images: []emote.InlineImage{{Path: "/cache/wave.png", Column: 15, Width: 3}}}
	}}); err != nil {
		t.Fatal(err)
	}
	if got := plainTerminalOutput(output.String()); !strings.Contains(got, "[KICK] > ") || strings.Contains(got, "[NONE] > ") {
		t.Fatalf("target reset during status/chat redraw: %q", got)
	}
}

func TestScreenConsecutiveMessagesStartAtColumnOneWithoutDecoration(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 48 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kick")
	for _, r := range "partially typed" {
		screen.Feed(r)
	}
	output.Reset()

	writer := screen.Writer(&output)
	for _, message := range []string{"first chat\n", "second is longer\n", "third\n"} {
		if _, err := io.WriteString(writer, message); err != nil {
			t.Fatal(err)
		}
	}

	raw := output.String()
	if strings.Contains(raw, "─") {
		t.Fatalf("separator leaked into output: %q", raw)
	}
	for _, message := range []string{"first chat", "second is longer", "third"} {
		if !strings.Contains(raw, "\x1bD\r\x1b[2K"+message) {
			t.Fatalf("chat did not start at column one with CRLF termination: %q", raw)
		}
	}
	const coloredPrompt = "\x1b[24;1H\x1b[2K\x1b[36m[KICK]\x1b[39m \x1b[32m>\x1b[39m partially typed"
	if count := strings.Count(raw, coloredPrompt); count != 3 {
		t.Fatalf("prompt redraw count=%d; output=%q", count, raw)
	}
	if screen.Text() != "partially typed" {
		t.Fatalf("partial input was lost: %q", screen.Text())
	}
}

func TestScreenStartsAtColumnOneAndUsesDefaultNamedColors(t *testing.T) {
	var output bytes.Buffer
	if _, err := io.WriteString(&output, "cursor is not at column one"); err != nil {
		t.Fatal(err)
	}
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kick")
	raw := output.String()
	if !strings.Contains(raw, "cursor is not at column one"+enterAlternateScreen) {
		t.Fatalf("alternate screen was not entered first: %q", raw)
	}
	if !strings.Contains(raw, "\x1b[2J\x1b[H") {
		t.Fatalf("screen did not reset to column one: %q", raw)
	}
	if !strings.Contains(raw, "\x1b[36m[KICK]\x1b[39m \x1b[32m>\x1b[39m ") {
		t.Fatalf("named-color prompt missing: %q", raw)
	}
	if strings.Contains(raw, "38;2") || strings.Contains(raw, "38;5") {
		t.Fatalf("prompt used a hard-coded RGB/indexed color: %q", raw)
	}
}

func TestScreenUsesCanonicalTwitchTargetPrompt(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("twitch")
	if got := plainTerminalOutput(output.String()); !strings.Contains(got, "[Twitch] > ") || strings.Contains(got, "[TW] > ") {
		t.Fatalf("prompt=%q", got)
	}
}

func TestScreenResizeThenIncomingUnicodePreservesInput(t *testing.T) {
	var output bytes.Buffer
	width := 24
	screen := NewScreen(&output, func() int { return width })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kick")
	for _, r := range "你好🙂 typing" {
		screen.Feed(r)
	}
	width = 16
	screen.Redraw()
	output.Reset()
	if _, err := io.WriteString(screen.Writer(&output), "[KICK] viewer  世界\n"); err != nil {
		t.Fatal(err)
	}
	if screen.Text() != "你好🙂 typing" {
		t.Fatalf("Unicode input changed after resize/redraw: %q", screen.Text())
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1bD\r\x1b[2K[KICK] viewer  世界") {
		t.Fatalf("incoming message drifted after resize: %q", raw)
	}
	if strings.Contains(raw, "─") {
		t.Fatalf("separator fragment appeared after resize: %q", raw)
	}
}

func TestScreenLongUnicodeInputStaysWithinDisplayWindow(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 18 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "前半部分🙂tail" {
		screen.Feed(r)
	}
	if screen.Text() != "前半部分🙂tail" {
		t.Fatalf("input was truncated: %q", screen.Text())
	}
	if !strings.Contains(output.String(), "tail") {
		t.Fatalf("cursor end was not kept visible: %q", output.String())
	}
}

func TestScreenRendersThreeFixedStatusLinesAndLiveOfflineViewers(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	for row, want := range []string{"Title:    unavailable", "Category: unavailable", "Viewers:  unavailable"} {
		if !strings.Contains(plainTerminalOutput(raw), want) || !strings.Contains(raw, fmt.Sprintf("\x1b[%d;1H", row+1)) {
			t.Fatalf("missing status row %d %q: %q", row+1, want, raw)
		}
	}
	if !strings.Contains(raw, "\x1b[5;8r") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("fixed chat/input layout missing: %q", raw)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Live title", Category: "Just Chatting", ViewerCount: 42, Live: true})
	plain := plainTerminalOutput(output.String())
	for _, want := range []string{"Title:    Live title", "Category: Just Chatting", "Viewers:  42"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing live status %q: %q", want, plain)
		}
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Offline title", Category: "Games", ViewerCount: 99, Live: false})
	if plain = plainTerminalOutput(output.String()); !strings.Contains(plain, "Viewers:  OFFLINE") || strings.Contains(plain, "Viewers:  99") {
		t.Fatalf("offline viewers=%q", plain)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Unavailable: true})
	plain = plainTerminalOutput(output.String())
	for _, want := range []string{"Title:    unavailable", "Category: unavailable", "Viewers:  unavailable"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing neutral target status %q: %q", want, plain)
		}
	}
}

func TestScreenRendersLocalDateAndTimeAtRightEdge(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 50 }, func() int { return 10 })
	screen.now = func() time.Time { return time.Date(2026, 8, 15, 16, 7, 59, 0, time.FixedZone("client", 8*60*60)) }
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for row, suffix := range map[int]string{1: "2026-08-15", 2: "16:07"} {
		line := statusRow(output.String(), row)
		if !strings.HasSuffix(line, suffix) || runewidth.StringWidth(line) != 50 {
			t.Fatalf("row %d=%q width=%d", row, line, runewidth.StringWidth(line))
		}
	}
	if line := statusRow(output.String(), 3); strings.Contains(line, "2026") || strings.Contains(line, "16:07") {
		t.Fatalf("viewer row contains clock: %q", line)
	}
}

func TestScreenStatusClockResizeToNarrowWidthPreservesRightSideWithoutOverlap(t *testing.T) {
	var output bytes.Buffer
	width := 30
	screen := newScreen(&output, func() int { return width }, func() int { return 8 })
	screen.now = func() time.Time { return time.Date(2026, 8, 15, 16, 7, 0, 0, time.Local) }
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	width = 14
	output.Reset()
	screen.Redraw()
	dateLine, timeLine := statusRow(output.String(), 1), statusRow(output.String(), 2)
	if !strings.HasSuffix(dateLine, "2026-08-15") || !strings.HasSuffix(timeLine, "16:07") {
		t.Fatalf("narrow clock missing: date=%q time=%q", dateLine, timeLine)
	}
	if runewidth.StringWidth(dateLine) > 14 || runewidth.StringWidth(timeLine) > 14 {
		t.Fatalf("narrow rows overflowed: date=%q time=%q", dateLine, timeLine)
	}
}

func TestClockRefreshPreservesChatSeparatorsAndUnicodeInputCursor(t *testing.T) {
	var output bytes.Buffer
	current := time.Date(2026, 8, 15, 23, 59, 0, 0, time.Local)
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	screen.now = func() time.Time { return current }
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "partly 你好🙂" {
		screen.Feed(r)
	}
	for _, r := range "\x1b[D\x1b[D" {
		screen.Feed(r)
	}
	if err := screen.AppendMessage(DisplayMessage{Line: "chat remains"}); err != nil {
		t.Fatal(err)
	}
	cursor := screen.editor.Cursor()
	output.Reset()
	current = time.Date(2026, 8, 16, 0, 0, 0, 0, time.Local)
	screen.RefreshTime()
	raw := output.String()
	if !strings.Contains(plainTerminalOutput(raw), "2026-08-16") || !strings.Contains(plainTerminalOutput(raw), "00:00") {
		t.Fatalf("clock did not roll over: %q", raw)
	}
	if screen.Text() != "partly 你好🙂" || screen.editor.Cursor() != cursor || len(screen.messages) != 1 {
		t.Fatalf("refresh changed state: text=%q cursor=%d messages=%v", screen.Text(), screen.editor.Cursor(), screen.messages)
	}
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b[4;1H") || strings.Contains(raw, "\x1b[9;1H") || strings.Contains(raw, "chat remains") {
		t.Fatalf("clock refresh disturbed fixed/chat layout: %q", raw)
	}
}

func TestTerminalClockTickerRefreshesStatus(t *testing.T) {
	if clockRefreshInterval > time.Minute {
		t.Fatalf("clock refresh interval=%s", clockRefreshInterval)
	}
	var output synchronizedBuffer
	current := time.Date(2026, 8, 15, 16, 7, 0, 0, time.Local)
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	screen.now = func() time.Time { return current }
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	clock := make(chan time.Time, 1)
	terminal := &Terminal{screen: screen, stop: make(chan struct{}), done: make(chan struct{}), resize: make(chan os.Signal), clock: clock}
	go terminal.watchScreen()
	output.Reset()
	current = time.Date(2026, 8, 15, 16, 8, 0, 0, time.Local)
	clock <- current
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(plainTerminalOutput(output.String()), "16:08") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(terminal.stop)
	<-terminal.done
	if !strings.Contains(plainTerminalOutput(output.String()), "16:08") {
		t.Fatalf("periodic refresh missing: %q", output.String())
	}
}

func statusRow(output string, row int) string {
	startMarker := fmt.Sprintf("\x1b[%d;1H\x1b[2K", row)
	start := strings.Index(output, startMarker)
	if start < 0 {
		return ""
	}
	remainder := output[start+len(startMarker):]
	end := len(remainder)
	if next := strings.Index(remainder, fmt.Sprintf("\x1b[%d;1H", row+1)); next >= 0 {
		end = next
	}
	return plainTerminalOutput(remainder[:end])
}

func TestScreenRendersFixedWidthSeparatorsAndResizesThem(t *testing.T) {
	var output bytes.Buffer
	width, height := 12, 10
	screen := newScreen(&output, func() int { return width }, func() int { return height })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\x1b[4;1H\x1b[2K------------",
		"\x1b[9;1H\x1b[2K------------",
		"\x1b[5;8r",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing fixed layout %q: %q", want, output.String())
		}
	}

	width, height = 7, 8
	output.Reset()
	screen.Redraw()
	raw := output.String()
	for _, want := range []string{
		"\x1b[4;1H\x1b[2K-------",
		"\x1b[7;1H\x1b[2K-------",
		"\x1b[5;6r",
		"\x1b[8;1H",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("resized layout missing %q: %q", want, raw)
		}
	}
	if strings.Contains(raw, "------------") {
		t.Fatalf("separator retained old width: %q", raw)
	}
}

func TestScreenStatusTruncatesLongUnicodeByDisplayWidth(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 30 }, func() int { return 8 })
	screen.now = func() time.Time { return time.Date(2026, 8, 15, 16, 7, 0, 0, time.Local) }
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "世界🙂 very long title", Category: "超長分類🙂tail", Live: true, ViewerCount: 7})
	raw := output.String()
	for row := 1; row <= 2; row++ {
		startMarker := fmt.Sprintf("\x1b[%d;1H", row)
		endMarker := fmt.Sprintf("\x1b[%d;1H", row+1)
		start := strings.Index(raw, startMarker)
		end := strings.Index(raw, endMarker)
		if start < 0 || end <= start {
			t.Fatalf("status row markers missing: %q", raw)
		}
		line := plainTerminalOutput(raw[start+len(startMarker) : end])
		if width := runewidth.StringWidth(line); width > 30 {
			t.Fatalf("row %d width=%d line=%q", row, width, line)
		}
		if row == 1 && (!strings.Contains(line, "世界🙂") || !strings.HasSuffix(line, "2026-08-15")) {
			t.Fatalf("Unicode title/date layout=%q", line)
		}
	}
}

func TestIncomingChatUsesOnlyMiddleScrollRegion(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetStatus(StreamStatus{Title: "Fixed", Category: "Games", Live: true, ViewerCount: 3})
	output.Reset()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Line: "[KICK] Alice  hello"}); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[8;1H\x1bD\r\x1b[2K[KICK] Alice  hello") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("chat/input rows incorrect: %q", raw)
	}
	for _, fixedRow := range []string{"\x1b[1;1H", "\x1b[2;1H", "\x1b[3;1H", "\x1b[4;1H", "\x1b[9;1H"} {
		if strings.Contains(raw, fixedRow) {
			t.Fatalf("incoming chat touched fixed row %q: %q", fixedRow, raw)
		}
	}
}

func TestStatusRedrawAndCleanPreserveInputCursorAndFixedRows(t *testing.T) {
	var output bytes.Buffer
	height := 10
	screen := newScreen(&output, func() int { return 32 }, func() int { return height })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "partly 你好🙂" {
		screen.Feed(r)
	}
	for _, r := range "\x1b[D\x1b[D" {
		screen.Feed(r)
	}
	cursor := screen.editor.Cursor()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Author: "Alice", Line: "chat one"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Updated", Category: "Games", Live: true, ViewerCount: 5})
	if screen.Text() != "partly 你好🙂" || screen.editor.Cursor() != cursor {
		t.Fatalf("status redraw changed editor text=%q cursor=%d", screen.Text(), screen.editor.Cursor())
	}
	if !strings.Contains(output.String(), "\x1b[10;1H") || !strings.Contains(plainTerminalOutput(output.String()), "[NONE] > partly 你好🙂") {
		t.Fatalf("input was not restored: %q", output.String())
	}
	output.Reset()
	if _, err := screen.CleanAll(); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	for _, fixedRow := range []string{"\x1b[1;1H", "\x1b[2;1H", "\x1b[3;1H", "\x1b[4;1H", "\x1b[9;1H"} {
		if strings.Contains(raw, fixedRow) {
			t.Fatalf("clean touched fixed row %q: %q", fixedRow, raw)
		}
	}
	if screen.status.Title != "Updated" || !strings.Contains(raw, "\x1b[5;1H\x1b[2K") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("clean did not preserve fixed state or redraw chat/input: status=%+v output=%q", screen.status, raw)
	}
	height = 8
	output.Reset()
	screen.Redraw()
	if raw := output.String(); !strings.Contains(raw, "\x1b[5;6r") || !strings.Contains(raw, "\x1b[4;1H") || !strings.Contains(raw, "\x1b[7;1H") || !strings.Contains(raw, "\x1b[8;1H") {
		t.Fatalf("resize layout incorrect: %q", raw)
	}
}

func TestScreenVerySmallHeightDegradesWithoutInvalidRegion(t *testing.T) {
	for _, height := range []int{1, 2, 3, 4, 5, 6, 7} {
		t.Run(fmt.Sprintf("height_%d", height), func(t *testing.T) {
			var output bytes.Buffer
			screen := newScreen(&output, func() int { return 12 }, func() int { return height })
			if err := screen.Start(); err != nil {
				t.Fatal(err)
			}
			for _, r := range "/c" {
				screen.Feed(r)
			}
			if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Line: "small chat"}); err != nil {
				t.Fatal(err)
			}
			raw := output.String()
			if strings.Contains(raw, "\x1b[5;5r") || strings.Contains(raw, "\x1b[5;4r") {
				t.Fatalf("invalid scroll region emitted: %q", raw)
			}
			if height == 6 && (!strings.Contains(raw, "\x1b[4;1H\x1b[2K------------") || !strings.Contains(raw, "\x1b[5;1H\x1b[2K------------")) {
				t.Fatalf("distinct separators missing at minimum fixed-layout height: %q", raw)
			}
			if height == 7 && !strings.Contains(raw, "\x1b[5;1H\x1b[2Ksmall chat") {
				t.Fatalf("single-row chat did not degrade safely: %q", raw)
			}
		})
	}
}

func TestScreenSerializesConcurrentInputAndMessages(t *testing.T) {
	var destination synchronizedBuffer
	screen := NewScreen(&destination, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	writer := screen.Writer(&destination)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for _, r := range "concurrent unicode 世界" {
			screen.Feed(r)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			screen.SetStatus(StreamStatus{Title: fmt.Sprintf("title %d", i), Category: "Games", Live: true, ViewerCount: i})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = fmt.Fprintf(writer, "message %d\n", i)
		}
	}()
	wg.Wait()
	if screen.Text() != "concurrent unicode 世界" || !strings.Contains(destination.String(), "message 19") {
		t.Fatalf("text=%q output=%q", screen.Text(), destination.String())
	}
}

func TestScreenCleanAllPreservesPartialUnicodeInput(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kick")
	for _, r := range "partly 你好🙂" {
		screen.Feed(r)
	}
	for _, r := range "\x1b[D\x1b[D" {
		screen.Feed(r)
	}
	cursorBefore := screen.editor.Cursor()
	for _, message := range []DisplayMessage{
		{Platform: "kick", Author: "Alice", Line: "[KICK] Alice  one\n"},
		{Platform: "youtube", Author: "Bob", Line: "[YouTube] Bob  two\n"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	removed, err := screen.CleanAll()
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if screen.Text() != "partly 你好🙂" {
		t.Fatalf("input changed: %q", screen.Text())
	}
	if screen.editor.Cursor() != cursorBefore {
		t.Fatalf("cursor changed: got=%d want=%d", screen.editor.Cursor(), cursorBefore)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[5;1H\x1b[2K") || !strings.Contains(plainTerminalOutput(raw), "[KICK] > partly 你好🙂") {
		t.Fatalf("view/input redraw missing: %q", raw)
	}
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b[4;1H") || strings.Contains(raw, "\x1b[23;1H") {
		t.Fatalf("clean touched status or separator layout: %q", raw)
	}
	if strings.Contains(raw, "Alice") || strings.Contains(raw, "Bob") {
		t.Fatalf("cleaned messages were replayed: %q", raw)
	}
	if strings.Contains(raw, leaveAlternateScreen) {
		t.Fatalf("clean unexpectedly left the alternate screen: %q", raw)
	}
}

func TestScreenCleanPlatformAndCaseInsensitiveAuthor(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []DisplayMessage{
		{Platform: "kick", Author: "BotRix", Line: "kick bot\n"},
		{Platform: "youtube", Author: "BotRix", Line: "youtube bot\n"},
		{Platform: "kick", Author: "Viewer", Line: "kick viewer\n"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	removed, err := screen.CleanPlatform("KICK")
	if err != nil || removed != 2 || len(screen.messages) != 1 || screen.messages[0].Platform != "youtube" {
		t.Fatalf("platform clean removed=%d messages=%v err=%v", removed, screen.messages, err)
	}
	if !strings.Contains(output.String(), "youtube bot") || strings.Contains(output.String(), "kick bot") || strings.Contains(output.String(), "kick viewer") {
		t.Fatalf("platform redraw=%q", output.String())
	}
	if strings.Contains(output.String(), "\x1b[4;1H") || strings.Contains(output.String(), "\x1b[23;1H") {
		t.Fatalf("platform clean touched separators: %q", output.String())
	}
	output.Reset()
	removed, err = screen.CleanAuthor("bOtRiX")
	if err != nil || removed != 1 || len(screen.messages) != 0 {
		t.Fatalf("author clean removed=%d messages=%v err=%v", removed, screen.messages, err)
	}
	if strings.Contains(output.String(), "\x1b[4;1H") || strings.Contains(output.String(), "\x1b[23;1H") {
		t.Fatalf("author clean touched separators: %q", output.String())
	}
}

func TestScreenCleanNoMatchDoesNotRedrawAndIncomingContinues(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "Alice", Line: "old message\n"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	removed, err := screen.CleanAuthor("missing")
	if err != nil || removed != 0 || output.Len() != 0 {
		t.Fatalf("removed=%d output=%q err=%v", removed, output.String(), err)
	}
	if _, err = screen.CleanAll(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err = screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "New", Line: "new after clean\n"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "new after clean") || len(screen.messages) != 1 {
		t.Fatalf("incoming message did not continue: %q messages=%v", output.String(), screen.messages)
	}
}

func TestScreenDisplayBufferIsBounded(t *testing.T) {
	screen := NewScreen(io.Discard, func() int { return 40 })
	for i := 0; i < displayMessageLimit+25; i++ {
		if err := screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "user", Line: fmt.Sprintf("message %d\n", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(screen.messages) != displayMessageLimit || screen.messages[0].Line != "message 25\n" {
		t.Fatalf("buffer len=%d first=%q", len(screen.messages), screen.messages[0].Line)
	}
}

func TestScreenKnownMessageIDsFiltersAndDeduplicatesProviderIDs(t *testing.T) {
	screen := NewScreen(io.Discard, func() int { return 40 })
	for _, message := range []DisplayMessage{
		{ID: "kick-1", Platform: "kick"},
		{ID: "youtube-1", Platform: "youtube"},
		{ID: "kick-1", Platform: "KICK"},
		{ID: "", Platform: "kick"},
		{ID: "kick-2", Platform: "kick"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if got := screen.KnownMessageIDs("kick"); !reflect.DeepEqual(got, []string{"kick-1", "kick-2"}) {
		t.Fatalf("IDs=%v", got)
	}
	if _, err := screen.CleanPlatform("kick"); err != nil {
		t.Fatal(err)
	}
	if got := screen.KnownMessageIDs("kick"); len(got) != 0 {
		t.Fatalf("cleaned IDs remain: %v", got)
	}
}

func TestScreenConcurrentIncomingAndCleanIsRaceSafe(t *testing.T) {
	var destination synchronizedBuffer
	screen := NewScreen(&destination, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "viewer", Line: fmt.Sprintf("incoming %d\n", i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = screen.CleanPlatform("kick")
		}
	}()
	wg.Wait()
	if len(screen.messages) > displayMessageLimit {
		t.Fatalf("buffer exceeded limit: %d", len(screen.messages))
	}
}

func TestScreenSuggestionPopupStaysAboveInputAndSurvivesIncomingChat(t *testing.T) {
	var output bytes.Buffer
	height := 12
	screen := newScreen(&output, func() int { return 32 }, func() int { return height })
	screen.SetTarget("kick")
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "/cl" {
		screen.Feed(r)
	}
	layout := screen.layoutLocked(height)
	if layout.suggestionTop != 9 || layout.suggestionBottom != 10 || layout.chatBottom != 8 {
		t.Fatalf("layout=%+v", layout)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[9;1H\x1b[2K  clean") || !strings.Contains(raw, "\x1b[10;1H\x1b[2K  clear") {
		t.Fatalf("suggestions not rendered in popup: %q", raw)
	}
	if !strings.Contains(raw, "\x1b[11;1H\x1b[2K"+strings.Repeat("-", 32)) || !strings.Contains(raw, "\x1b[12;1H\x1b[2K\x1b[36m[KICK]") {
		t.Fatalf("fixed bottom layout missing: %q", raw)
	}

	output.Reset()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Author: "viewer", Line: "incoming while completing"}); err != nil {
		t.Fatal(err)
	}
	raw = output.String()
	if !strings.Contains(raw, "\x1b[8;1H") || strings.Contains(raw, "\x1b[9;1H") || strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("incoming chat escaped its bounded region: %q", raw)
	}
	if screen.Text() != "/cl" || screen.target != "KICK" {
		t.Fatalf("text=%q target=%q", screen.Text(), screen.target)
	}
}

func TestScreenCleanAndResizeRedrawSuggestionsWithoutLosingInput(t *testing.T) {
	var output bytes.Buffer
	width, height := 36, 12
	screen := newScreen(&output, func() int { return width }, func() int { return height })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Author: "viewer", Line: "before clean"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range "/open " {
		screen.Feed(r)
	}
	output.Reset()
	if removed, err := screen.CleanAll(); err != nil || removed != 1 {
		t.Fatalf("removed=%d error=%v", removed, err)
	}
	raw := output.String()
	for _, candidate := range []string{"kick", "youtube", "twitch"} {
		if !strings.Contains(raw, "  "+candidate) {
			t.Fatalf("clean lost %q suggestion: %q", candidate, raw)
		}
	}
	if screen.Text() != "/open " {
		t.Fatalf("clean lost input: %q", screen.Text())
	}

	width, height = 24, 9
	output.Reset()
	screen.Redraw()
	layout := screen.layoutLocked(height)
	// A height of nine leaves one chat row at row five and suggestions at
	// rows six through seven.
	if layout.suggestionTop != 6 || layout.suggestionBottom != 7 || layout.chatTop != 5 || layout.chatBottom != 5 {
		t.Fatalf("resized layout=%+v", layout)
	}
	raw = output.String()
	if !strings.Contains(raw, "\x1b[6;1H\x1b[2K  kick") || !strings.Contains(raw, "\x1b[9;1H") || screen.Text() != "/open " {
		t.Fatalf("resize redraw=%q text=%q", raw, screen.Text())
	}
}

func TestTerminalEscapeDismissesSuggestionsAndArrowKeysNavigate(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 32 }, func() int { return 12 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "/cl" {
		screen.Feed(r)
	}
	terminal := &Terminal{reader: bufio.NewReader(strings.NewReader("\x1b[B")), screen: screen}
	for range 3 {
		if _, err := terminal.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if _, selected := screen.editor.Suggestions(); selected != 0 {
		t.Fatalf("down selected=%d", selected)
	}

	terminal.reader = bufio.NewReader(strings.NewReader("\x1b"))
	if _, err := terminal.Next(); err != nil {
		t.Fatal(err)
	}
	if suggestions, selected := screen.editor.Suggestions(); len(suggestions) != 0 || selected != -1 || screen.Text() != "/cl" {
		t.Fatalf("suggestions=%v selected=%d text=%q", suggestions, selected, screen.Text())
	}
}

func TestTerminalCloseRestoresOnce(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	restored := 0
	terminal := &Terminal{
		screen:  screen,
		restore: func() error { restored++; return nil },
		stop:    make(chan struct{}),
		resize:  make(chan os.Signal, 1),
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restore calls=%d", restored)
	}
	if count := strings.Count(output.String(), leaveAlternateScreen); count != 1 {
		t.Fatalf("alternate-screen leave count=%d output=%q", count, output.String())
	}
}

func TestScreenStartupErrorLeavesAlternateScreenAndRestoresTerminal(t *testing.T) {
	output := &failNthWriter{failAt: 2}
	screen := NewScreen(output, func() int { return 40 })
	restored := 0
	err := startScreen(screen, func() error {
		restored++
		return nil
	})
	if err == nil {
		t.Fatal("expected startup failure")
	}
	if restored != 1 {
		t.Fatalf("terminal restore calls=%d", restored)
	}
	if !strings.Contains(output.String(), enterAlternateScreen) || !strings.Contains(output.String(), leaveAlternateScreen) {
		t.Fatalf("startup cleanup sequences missing: %q", output.String())
	}
}

func TestTerminalCloseStopsScreenWatcher(t *testing.T) {
	var output bytes.Buffer
	terminal := &Terminal{
		screen:  NewScreen(&output, func() int { return 40 }),
		restore: func() error { return nil },
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		resize:  make(chan os.Signal, 1),
	}
	go terminal.watchScreen()
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminal.done:
	default:
		t.Fatal("screen watcher was still running after Close")
	}
}

func TestTerminalExitQuitAndCtrlCCleanup(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLine string
	}{
		{name: "exit", input: "/exit\r", wantLine: "/exit"},
		{name: "quit", input: "/quit\r", wantLine: "/quit"},
		{name: "ctrl-c", input: "\x03"},
		{name: "ctrl-d", input: "\x04"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			screen := NewScreen(&output, func() int { return 40 })
			if err := screen.Start(); err != nil {
				t.Fatal(err)
			}
			restored := false
			terminal := &Terminal{
				reader:  bufio.NewReader(strings.NewReader(test.input)),
				screen:  screen,
				restore: func() error { restored = true; return nil },
				stop:    make(chan struct{}),
				resize:  make(chan os.Signal, 1),
			}
			var event Event
			for !event.Submit && !event.Shutdown {
				var err error
				event, err = terminal.Next()
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.wantLine != "" && (!event.Submit || event.Line != test.wantLine) {
				t.Fatalf("event=%+v", event)
			}
			if test.wantLine == "" && !event.Shutdown {
				t.Fatalf("event=%+v", event)
			}
			if err := terminal.Close(); err != nil {
				t.Fatal(err)
			}
			if !restored {
				t.Fatal("terminal state was not restored")
			}
			if !strings.Contains(output.String(), leaveAlternateScreen) {
				t.Fatalf("main screen was not restored: %q", output.String())
			}
		})
	}
}

type failNthWriter struct {
	writes int
	failAt int
	b      bytes.Buffer
}

func (w *failNthWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("injected write failure")
	}
	return w.b.Write(p)
}

func (w *failNthWriter) String() string { return w.b.String() }

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *synchronizedBuffer) Reset() {
	b.mu.Lock()
	b.b.Reset()
	b.mu.Unlock()
}

type recordingImageBackend struct {
	mu      sync.Mutex
	updates [][]emote.Placement
	closed  int
}

type confirmingImageBackend struct {
	recordingImageBackend
	muConfirmed sync.RWMutex
	confirmed   map[string]bool
}

func (b *confirmingImageBackend) Confirmed(identifier string) bool {
	b.muConfirmed.RLock()
	defer b.muConfirmed.RUnlock()
	return b.confirmed[identifier]
}

func (b *confirmingImageBackend) setConfirmed(identifier string, value bool) {
	b.muConfirmed.Lock()
	if b.confirmed == nil {
		b.confirmed = make(map[string]bool)
	}
	b.confirmed[identifier] = value
	b.muConfirmed.Unlock()
}

func (*recordingImageBackend) Available() bool { return true }
func (b *recordingImageBackend) Update(value []emote.Placement) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updates = append(b.updates, append([]emote.Placement(nil), value...))
}
func (b *recordingImageBackend) Close() error {
	b.mu.Lock()
	b.closed++
	b.mu.Unlock()
	return nil
}
func (b *recordingImageBackend) latest() []emote.Placement {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.updates) == 0 {
		return nil
	}
	return append([]emote.Placement(nil), b.updates[len(b.updates)-1]...)
}

func imageMessage(id string, column int) DisplayMessage {
	return DisplayMessage{ID: id, Platform: "kick", Author: "viewer", Render: func() emote.Line {
		return emote.Line{Text: "[KICK] viewer  hello  ", Images: []emote.InlineImage{{Path: "/cache/" + id + ".img", Column: column, Width: 2}}}
	}}
}

func TestCachedEmoteKeepsReadableBackingUntilPlacementAndAfterBackendFailure(t *testing.T) {
	backend := &confirmingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	message := DisplayMessage{ID: "emote", Platform: "kick", Render: func() emote.Line {
		return emote.Line{
			Text:          "[KICK] viewer  :emojiCheerful:",
			GraphicalText: "[KICK] viewer    ",
			Images:        []emote.InlineImage{{Path: "/cache/kick/1730756.static.png", Column: 15, Width: 2}},
		}
	}}
	if err := screen.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	placements := backend.latest()
	if len(placements) != 1 {
		t.Fatalf("placements=%+v", placements)
	}
	screen.mu.Lock()
	before := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if before != "[KICK] viewer  :emojiCheerful:" {
		t.Fatalf("cache hit hid fallback before placement: %q", before)
	}
	backend.setConfirmed(placements[0].Identifier, true)
	screen.mu.Lock()
	placed := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if placed != "[KICK] viewer    " {
		t.Fatalf("confirmed placement did not expose image backing: %q", placed)
	}
	backend.setConfirmed(placements[0].Identifier, false)
	screen.Redraw()
	screen.mu.Lock()
	afterFailure := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if afterFailure != before {
		t.Fatalf("backend failure did not restore readable fallback: %q", afterFailure)
	}
}

func TestTwitchCachedEmoteKeepsNameUntilPlacementAndRestoresItAfterFailure(t *testing.T) {
	backend := &confirmingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	message := DisplayMessage{ID: "twitch-emote", Platform: "twitch", Render: func() emote.Line {
		return emote.Line{
			Text:          "[Twitch] viewer  Kappa",
			GraphicalText: "[Twitch] viewer     ",
			Images:        []emote.InlineImage{{Path: "/cache/twitch/25.img", Column: 13, Width: 3}},
		}
	}}
	if err := screen.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	placements := backend.latest()
	if len(placements) != 1 {
		t.Fatalf("placements=%+v", placements)
	}
	screen.mu.Lock()
	before := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if before != "[Twitch] viewer  Kappa" {
		t.Fatalf("fallback before confirmation=%q", before)
	}
	backend.setConfirmed(placements[0].Identifier, true)
	screen.mu.Lock()
	confirmed := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if confirmed != "[Twitch] viewer     " {
		t.Fatalf("confirmed backing=%q", confirmed)
	}
	backend.setConfirmed(placements[0].Identifier, false)
	screen.Redraw()
	screen.mu.Lock()
	after := screen.renderedForDisplayLocked(screen.messages[0]).Text
	screen.mu.Unlock()
	if after != before {
		t.Fatalf("fallback after backend failure=%q", after)
	}
}

func TestImagePlacementTracksVisibleChatRowsAndNeverUsesFixedUI(t *testing.T) {
	var output bytes.Buffer
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(imageMessage("one", 20)); err != nil {
		t.Fatal(err)
	}
	placement := backend.latest()
	if len(placement) != 1 || placement[0].X != 20 || placement[0].Y != 7 {
		t.Fatalf("placement=%+v", placement)
	}
	// Four rows fit between separators. A fifth message scrolls the first
	// image out and repositions the remaining overlays within rows 5-8 only.
	for _, id := range []string{"two", "three", "four", "five"} {
		if err := screen.AppendMessage(imageMessage(id, 20)); err != nil {
			t.Fatal(err)
		}
	}
	placement = backend.latest()
	if len(placement) != 4 {
		t.Fatalf("visible placements=%+v", placement)
	}
	for _, item := range placement {
		if item.Y < 4 || item.Y > 7 {
			t.Fatalf("image overlaps fixed UI: %+v", item)
		}
		if strings.Contains(item.Identifier, "one") {
			t.Fatalf("scrolled image survived: %+v", item)
		}
	}
}

func TestVisibleEmoteSurvivesASCIIAndUnicodeViewportRedraws(t *testing.T) {
	var output bytes.Buffer
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return 20 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	renders := 0
	emoteMessage := DisplayMessage{ID: "emote", Platform: "kick", Author: "viewer", Render: func() emote.Line {
		renders++
		return emote.Line{Text: "emote  ", Images: []emote.InlineImage{{Path: "/cache/emote.img", Column: 6, Width: 2}}}
	}}
	if err := screen.AppendMessage(emoteMessage); err != nil {
		t.Fatal(err)
	}
	assertVisibleImage := func(wantY int) {
		t.Helper()
		placements := backend.latest()
		if len(placements) != 1 || placements[0].Path != "/cache/emote.img" || placements[0].Y != wantY {
			t.Fatalf("placements=%+v want y=%d", placements, wantY)
		}
	}
	assertVisibleImage(7)
	if err := screen.AppendMessage(DisplayMessage{ID: "ascii", Platform: "kick", Line: "hello"}); err != nil {
		t.Fatal(err)
	}
	assertVisibleImage(6)
	if err := screen.AppendMessage(DisplayMessage{ID: "cjk", Platform: "kick", Line: "你好"}); err != nil {
		t.Fatal(err)
	}
	assertVisibleImage(5)
	if renders < 3 {
		t.Fatalf("structured emote was not reconstructed on each viewport update: renders=%d", renders)
	}
	if err := screen.AppendMessage(DisplayMessage{ID: "last-visible", Platform: "kick", Line: "still visible"}); err != nil {
		t.Fatal(err)
	}
	assertVisibleImage(4)
	if err := screen.AppendMessage(DisplayMessage{ID: "scroll", Platform: "kick", Line: "scroll it out"}); err != nil {
		t.Fatal(err)
	}
	if placements := backend.latest(); len(placements) != 0 {
		t.Fatalf("scrolled-out emote was not removed: %+v", placements)
	}
}

func TestWideUnicodeChangesGeometryWithoutDisablingVisibleEmote(t *testing.T) {
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 10 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(DisplayMessage{ID: "emote", Platform: "kick", Render: func() emote.Line {
		return emote.Line{Text: "emoji  ", Images: []emote.InlineImage{{Path: "/cache/emote.img", Column: 6, Width: 2}}}
	}}); err != nil {
		t.Fatal(err)
	}
	before := backend.latest()[0]
	// Six CJK runes occupy twelve cells and therefore scroll two terminal
	// rows. Their content must influence geometry only, never image eligibility.
	if err := screen.AppendMessage(DisplayMessage{ID: "wide", Platform: "kick", Line: "你好世界哈囉"}); err != nil {
		t.Fatal(err)
	}
	after := backend.latest()
	if len(after) != 1 || after[0].Path != before.Path || after[0].Y != before.Y-2 {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestImagePlacementsRemainDistinctWithoutProviderMessageIDs(t *testing.T) {
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/cache/first.img", "/cache/second.img"} {
		path := path
		if err := screen.AppendMessage(DisplayMessage{Platform: "kick", Render: func() emote.Line {
			return emote.Line{Text: "emote  ", Images: []emote.InlineImage{{Path: path, Column: 6, Width: 2}}}
		}}); err != nil {
			t.Fatal(err)
		}
	}
	placements := backend.latest()
	if len(placements) != 2 || placements[0].Identifier == placements[1].Identifier {
		t.Fatalf("placements=%+v", placements)
	}
}

func TestResolvedImageInsertionDoesNotReassignExistingPlacement(t *testing.T) {
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	available := map[string]bool{"305954156": true}
	message := DisplayMessage{ID: "async-emotes", Platform: "twitch", Render: func() emote.Line {
		return emote.FormatText(chat.PlatformTwitch, "KappaPogChamp", []chat.Emote{
			{ID: "25", Name: "Kappa", URL: "https://example.invalid/25", Start: 0, End: 4},
			{ID: "305954156", Name: "PogChamp", URL: "https://example.invalid/305954156", Start: 5, End: 12},
		}, nil, func(_ chat.Platform, item chat.Emote) (string, bool) {
			return "/cache/twitch/" + item.ID + ".img", available[item.ID]
		})
	}}
	if err := screen.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	partial := backend.latest()
	if len(partial) != 1 || partial[0].Path != "/cache/twitch/305954156.img" {
		t.Fatalf("partial placements=%+v", partial)
	}
	pogIdentifier := partial[0].Identifier
	available["25"] = true
	screen.Redraw()
	complete := backend.latest()
	if len(complete) != 2 {
		t.Fatalf("complete placements=%+v", complete)
	}
	identifiers := make(map[string]string, len(complete))
	for _, placement := range complete {
		identifiers[placement.Path] = placement.Identifier
	}
	if identifiers["/cache/twitch/305954156.img"] != pogIdentifier || identifiers["/cache/twitch/25.img"] == pogIdentifier {
		t.Fatalf("placement identity migrated between emotes: partial=%+v complete=%+v", partial, complete)
	}
}

func TestMixedProviderImageFrameScrollResizeAndCleanup(t *testing.T) {
	width := 60
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return width }, func() int { return 15 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	graphical := func(platform, id string, columns ...int) DisplayMessage {
		return DisplayMessage{ID: id, Platform: platform, Render: func() emote.Line {
			images := make([]emote.InlineImage, 0, len(columns))
			for index, column := range columns {
				images = append(images, emote.InlineImage{
					Path:         fmt.Sprintf("/cache/%s/%s-%d.img", platform, id, index),
					PlacementKey: fmt.Sprintf("%s-%d", id, index),
					Column:       column,
					Width:        3,
				})
			}
			return emote.Line{Text: strings.Repeat("x", 50), GraphicalText: strings.Repeat(" ", 50), Images: images}
		}}
	}
	history := []DisplayMessage{
		graphical("kick", "kick-1", 10),
		graphical("twitch", "twitch-1", 14),
		graphical("kick", "kick-2", 18),
		graphical("twitch", "twitch-2", 22),
		{ID: "text", Platform: "kick", Line: "ordinary text"},
		graphical("twitch", "twitch-multi", 10, 25, 40),
		graphical("kick", "kick-multi", 12, 44),
	}
	for _, message := range history {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	initial := backend.latest()
	if len(initial) != 9 {
		t.Fatalf("initial placements=%+v", initial)
	}
	initialIDs := make(map[string]string, len(initial))
	for _, placement := range initial {
		if previous, exists := initialIDs[placement.Identifier]; exists {
			t.Fatalf("identifier collision for %q and %q: %+v", previous, placement.Path, initial)
		}
		initialIDs[placement.Identifier] = placement.Path
		if placement.Y < 6 || placement.Y > 12 {
			t.Fatalf("initial placement outside chat history: %+v", placement)
		}
	}
	beforeTargetSwitch := backend.latest()
	screen.SetTarget("twitch")
	screen.SetTarget("kick")
	if got := backend.latest(); !reflect.DeepEqual(got, beforeTargetSwitch) {
		t.Fatalf("target switch changed image frame: before=%+v after=%+v", beforeTargetSwitch, got)
	}
	for index := range 3 {
		if err := screen.AppendMessage(DisplayMessage{ID: fmt.Sprintf("tail-%d", index), Platform: "kick", Line: "tail"}); err != nil {
			t.Fatal(err)
		}
	}
	afterScroll := backend.latest()
	if len(afterScroll) != 8 {
		t.Fatalf("scrolled placements=%+v", afterScroll)
	}
	for _, placement := range afterScroll {
		if placement.Path == "/cache/kick/kick-1-0.img" {
			t.Fatalf("scrolled-out placement survived: %+v", afterScroll)
		}
		if initialIDs[placement.Identifier] != placement.Path {
			t.Fatalf("scroll reassigned placement identifier: %+v", placement)
		}
	}
	width = 30
	screen.Redraw()
	afterResize := backend.latest()
	if len(afterResize) != 5 {
		t.Fatalf("resized placements=%+v", afterResize)
	}
	wantGeometry := map[string]struct{ x, y int }{
		"/cache/twitch/twitch-multi-0.img": {10, 6},
		"/cache/twitch/twitch-multi-1.img": {25, 6},
		"/cache/twitch/twitch-multi-2.img": {10, 7},
		"/cache/kick/kick-multi-0.img":     {12, 8},
		"/cache/kick/kick-multi-1.img":     {14, 9},
	}
	for _, placement := range afterResize {
		want, exists := wantGeometry[placement.Path]
		if !exists || placement.X != want.x || placement.Y != want.y || initialIDs[placement.Identifier] != placement.Path {
			t.Fatalf("unexpected resized placement=%+v want=%v", placement, wantGeometry)
		}
	}
	removed, err := screen.CleanPlatform("twitch")
	if err != nil || removed != 3 {
		t.Fatalf("clean twitch removed=%d err=%v", removed, err)
	}
	remaining := backend.latest()
	if len(remaining) == 0 {
		t.Fatal("clean twitch removed Kick placements")
	}
	for _, placement := range remaining {
		if strings.Contains(placement.Path, "/twitch/") || initialIDs[placement.Identifier] != placement.Path {
			t.Fatalf("cross-provider cleanup corrupted frame: %+v", remaining)
		}
	}
}

func TestTextOnlyMixedProviderHistoryNeverCreatesGraphicalState(t *testing.T) {
	width := 60
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return width }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	formatter := render.New(io.Discard, render.Options{})
	history := []chat.Message{
		{
			ID: "kick-one", Platform: chat.PlatformKick, AuthorDisplayName: "kick-user", Text: "[emote:7:WAVE]",
			Emotes: []chat.Emote{{ID: "7", Name: "WAVE", URL: "https://files.kick.com/emotes/7/fullsize", Start: 0, End: 13}},
		},
		{
			ID: "twitch-one", Platform: chat.PlatformTwitch, AuthorDisplayName: "twitch-user", Text: "Kappa",
			Emotes: []chat.Emote{{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 0, End: 4}},
		},
		{
			ID: "twitch-mixed", Platform: chat.PlatformTwitch, AuthorDisplayName: "twitch-user", Text: "hello KappaKappa",
			Emotes: []chat.Emote{
				{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 6, End: 10},
				{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 11, End: 15},
			},
		},
		{
			ID: "kick-mixed", Platform: chat.PlatformKick, AuthorDisplayName: "kick-user", Text: "text [emote:8:CLAP][emote:8:CLAP]",
			Emotes: []chat.Emote{
				{ID: "8", Name: "CLAP", URL: "https://files.kick.com/emotes/8/fullsize", Start: 5, End: 18},
				{ID: "8", Name: "CLAP", URL: "https://files.kick.com/emotes/8/fullsize", Start: 19, End: 32},
			},
		},
	}
	for _, message := range history {
		message := message
		if err := screen.AppendMessage(DisplayMessage{ID: message.ID, Platform: string(message.Platform), Author: message.AuthorDisplayName, Render: func() emote.Line {
			return formatter.Format(message)
		}}); err != nil {
			t.Fatal(err)
		}
		if placements := backend.latest(); len(placements) != 0 {
			t.Fatalf("%s produced placements: %+v", message.ID, placements)
		}
	}

	screen.mu.Lock()
	rendered := make([]string, len(screen.messages))
	for index := range screen.messages {
		line := screen.messages[index].rendered()
		rendered[index] = render.Sanitize(line.Text)
		if line.GraphicalText != "" || len(line.Images) != 0 {
			t.Fatalf("message %d retained graphical state: %+v", index, line)
		}
	}
	screen.mu.Unlock()
	for _, want := range []string{":WAVE:", "Kappa", "hello KappaKappa", "text :CLAP::CLAP:"} {
		if !slices.ContainsFunc(rendered, func(line string) bool { return strings.Contains(line, want) }) {
			t.Fatalf("missing readable %q in %v", want, rendered)
		}
	}

	// Exercise the viewport lifecycle used by scrolling, resize, target
	// switching, and local provider cleaning. Text-only rows must never create
	// image state, even if a test backend is deliberately attached.
	for index := range 8 {
		if err := screen.AppendMessage(DisplayMessage{ID: fmt.Sprintf("tail-%d", index), Platform: "kick", Line: "ordinary text"}); err != nil {
			t.Fatal(err)
		}
	}
	width = 30
	screen.Redraw()
	screen.SetTarget("twitch")
	screen.SetTarget("kick")
	if _, err := screen.CleanPlatform("twitch"); err != nil {
		t.Fatal(err)
	}
	if placements := backend.latest(); len(placements) != 0 {
		t.Fatalf("text-only lifecycle produced placements: %+v", placements)
	}
	if err := screen.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImageCleanupOnCleanResizeAndAlternateScreenExit(t *testing.T) {
	var output bytes.Buffer
	width := 40
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return width }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(imageMessage("one", 30)); err != nil {
		t.Fatal(err)
	}
	width = 20
	screen.Redraw()
	placement := backend.latest()
	if len(placement) != 1 || placement[0].X != 10 || placement[0].Y < 4 || placement[0].Y > 7 {
		t.Fatalf("resized placement=%+v", placement)
	}
	if removed, err := screen.CleanPlatform("kick"); err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if placement = backend.latest(); len(placement) != 0 {
		t.Fatalf("clean left overlays: %+v", placement)
	}
	if err := screen.AppendMessage(imageMessage("two", 10)); err != nil {
		t.Fatal(err)
	}
	if err := screen.Close(); err != nil {
		t.Fatal(err)
	}
	if placement = backend.latest(); len(placement) != 0 {
		t.Fatalf("alternate-screen exit left overlays: %+v", placement)
	}
}

func TestCleanTwitchRemovesOnlyLocalTwitchMessageAndOverlay(t *testing.T) {
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []DisplayMessage{
		{ID: "twitch-emote", Platform: "twitch", Author: "viewer", Render: func() emote.Line {
			return emote.Line{Text: "[Twitch] viewer Kappa", GraphicalText: "[Twitch] viewer    ", Images: []emote.InlineImage{{Path: "/cache/twitch/25.img", Column: 16, Width: 3}}}
		}},
		{ID: "kick-emote", Platform: "kick", Author: "viewer", Render: func() emote.Line {
			return emote.Line{Text: "[KICK] viewer :wave:", GraphicalText: "[KICK] viewer    ", Images: []emote.InlineImage{{Path: "/cache/kick/7.img", Column: 14, Width: 3}}}
		}},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if placements := backend.latest(); len(placements) != 2 {
		t.Fatalf("initial placements=%+v", placements)
	}
	removed, err := screen.CleanPlatform("TWITCH")
	if err != nil || removed != 1 || len(screen.messages) != 1 || screen.messages[0].Platform != "kick" {
		t.Fatalf("removed=%d messages=%+v err=%v", removed, screen.messages, err)
	}
	placements := backend.latest()
	if len(placements) != 1 || placements[0].Path != "/cache/kick/7.img" {
		t.Fatalf("remaining placements=%+v", placements)
	}
}

func TestTwitchOverlayRepositionsScrollsOutAndCleansUpOnShutdown(t *testing.T) {
	width := 40
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return width }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	twitchImage := func(id string) DisplayMessage {
		return DisplayMessage{ID: id, Platform: "twitch", Render: func() emote.Line {
			return emote.Line{Text: strings.Repeat("x", 35), Images: []emote.InlineImage{{Path: "/cache/twitch/" + id + ".img", Column: 30, Width: 3}}}
		}}
	}
	if err := screen.AppendMessage(twitchImage("first")); err != nil {
		t.Fatal(err)
	}
	before := backend.latest()
	if len(before) != 1 || before[0].X != 30 {
		t.Fatalf("initial placement=%+v", before)
	}
	width = 20
	screen.Redraw()
	afterResize := backend.latest()
	if len(afterResize) != 1 || afterResize[0].X != 10 || afterResize[0].Y < 4 || afterResize[0].Y > 7 {
		t.Fatalf("resized placement=%+v", afterResize)
	}
	for index := range 4 {
		if err := screen.AppendMessage(DisplayMessage{ID: fmt.Sprintf("text-%d", index), Platform: "twitch", Line: "one row"}); err != nil {
			t.Fatal(err)
		}
	}
	if placements := backend.latest(); len(placements) != 0 {
		t.Fatalf("scrolled-out Twitch overlay remained=%+v", placements)
	}
	if err := screen.AppendMessage(twitchImage("second")); err != nil {
		t.Fatal(err)
	}
	if placements := backend.latest(); len(placements) != 1 {
		t.Fatalf("redrawn Twitch overlay=%+v", placements)
	}
	if err := screen.Close(); err != nil {
		t.Fatal(err)
	}
	if placements := backend.latest(); len(placements) != 0 {
		t.Fatalf("shutdown left Twitch overlay=%+v", placements)
	}
}

func TestTransientOutputRepositionsImagesWithoutTearing(t *testing.T) {
	var output bytes.Buffer
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(imageMessage("one", 20)); err != nil {
		t.Fatal(err)
	}
	before := backend.latest()[0].Y
	if _, err := screen.Writer(&output).Write([]byte("local command result\n")); err != nil {
		t.Fatal(err)
	}
	after := backend.latest()[0].Y
	if after != before-1 {
		t.Fatalf("overlay did not follow scrolling output: before=%d after=%d", before, after)
	}
}

func TestConcurrentImageMessagesRedrawAndCleanAreSynchronized(t *testing.T) {
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(io.Discard, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := 0; index < 25; index++ {
				_ = screen.AppendMessage(imageMessage(fmt.Sprintf("%d-%d", worker, index), 20))
				screen.Redraw()
				_, _ = screen.CleanAuthor("nobody")
			}
		}()
	}
	workers.Wait()
	if len(screen.messages) != 100 {
		t.Fatalf("messages=%d", len(screen.messages))
	}
}

func TestTerminalCloseStopsImageBackendOnceBeforeRestoringScreen(t *testing.T) {
	var output bytes.Buffer
	backend := &recordingImageBackend{}
	screen := newScreenWithBackend(&output, func() int { return 40 }, func() int { return 10 }, backend)
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	restored := 0
	terminal := &Terminal{screen: screen, restore: func() error { restored++; return nil }, stop: make(chan struct{}), resize: make(chan os.Signal, 1)}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	closed := backend.closed
	backend.mu.Unlock()
	if closed != 1 || restored != 1 || !strings.Contains(output.String(), leaveAlternateScreen) {
		t.Fatalf("backend closes=%d restores=%d output=%q", closed, restored, output.String())
	}
}
