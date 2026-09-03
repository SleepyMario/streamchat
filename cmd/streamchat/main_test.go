package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	archivepkg "github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientstate"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/outbound"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/render"
	"github.com/SleepyMario/streamchat/internal/terminalui"
)

func TestInteractiveEmotePresentationKeepsReadableFallbackWithoutBackend(t *testing.T) {
	c := config.Defaults()
	c.Emotes.Mode = "text"
	backend, resolver := interactiveEmotePresentation(c, io.Discard)
	if backend != nil || resolver != nil {
		t.Fatalf("interactive presentation backend=%T resolver=%v", backend, resolver != nil)
	}

	formatter := render.New(io.Discard, render.Options{Emotes: resolver})
	tests := []struct {
		name    string
		message chat.Message
		want    string
	}{
		{
			name: "kick repeated adjacent and text",
			message: chat.Message{
				Platform: chat.PlatformKick,
				Text:     "hi [emote:7:WAVE][emote:7:WAVE] there",
				Emotes: []chat.Emote{
					{ID: "7", Name: "WAVE", URL: "https://files.kick.com/emotes/7/fullsize", Start: 3, End: 16},
					{ID: "7", Name: "WAVE", URL: "https://files.kick.com/emotes/7/fullsize", Start: 17, End: 30},
				},
			},
			want: ":WAVE::WAVE: there",
		},
		{
			name: "twitch repeated adjacent and text",
			message: chat.Message{
				Platform: chat.PlatformTwitch,
				Text:     "hi KappaKappa there",
				Emotes: []chat.Emote{
					{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 3, End: 7},
					{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 8, End: 12},
				},
			},
			want: "KappaKappa there",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := append([]chat.Emote(nil), test.message.Emotes...)
			line := formatter.Format(test.message)
			if !strings.Contains(render.Sanitize(line.Text), test.want) || line.GraphicalText != "" || len(line.Images) != 0 {
				t.Fatalf("formatted line=%+v", line)
			}
			if !reflect.DeepEqual(test.message.Emotes, metadata) {
				t.Fatalf("emote metadata changed: got=%+v want=%+v", test.message.Emotes, metadata)
			}
		})
	}
}

func TestDemoOfflineAndHelp(t *testing.T) {
	var out, err bytes.Buffer
	if c := run([]string{"demo", "--no-color"}, &out, &err); c != 0 {
		t.Fatalf("%d %s", c, err.String())
	}
	s := out.String()
	if strings.Count(s, "duplicate") != 0 || !strings.Contains(s, "[YouTube]") || !strings.Contains(s, "[KICK]") || !strings.Contains(s, "↪") || !strings.Contains(s, "[PAID") {
		t.Fatal(s)
	}
	out.Reset()
	if c := run([]string{"--help"}, &out, &err); c != 0 || !strings.Contains(out.String(), "kick subscribe") || !strings.Contains(out.String(), "streamchat serve") || !strings.Contains(out.String(), "/kick hello") || !strings.Contains(out.String(), "/twitch hello") || !strings.Contains(out.String(), "/title New stream title") || !strings.Contains(out.String(), "/category Just Chatting") || !strings.Contains(out.String(), "/ban kick USER") || !strings.Contains(out.String(), "/ban twitch USER") || !strings.Contains(out.String(), "/timeout kick USER 10m") || !strings.Contains(out.String(), "/timeout twitch USER 30s") || !strings.Contains(out.String(), "/clean streamchat") || !strings.Contains(out.String(), "/clean kick") || !strings.Contains(out.String(), "/clean twitch") || !strings.Contains(out.String(), "/clean USER") || !strings.Contains(out.String(), "/clear kick 3d") || !strings.Contains(out.String(), "/clear twitch") || !strings.Contains(out.String(), "/open kick") || !strings.Contains(out.String(), "/open youtube") || !strings.Contains(out.String(), "/open twitch") || !strings.Contains(out.String(), "/exit") || !strings.Contains(out.String(), "/quit") || !strings.Contains(out.String(), "last selected outbound target is restored") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "/kk") {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "Streamchat 3.9") || !strings.Contains(out.String(), "All three platforms support reading, sending") {
		t.Fatalf("help does not describe stable platform support accurately: %s", out.String())
	}
}

func TestVersionReportsBuildValue(t *testing.T) {
	original := version
	version = "0.1.0-beta.1"
	t.Cleanup(func() { version = original })
	var out, errw bytes.Buffer
	if code := run([]string{"version"}, &out, &errw); code != 0 || errw.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw.String())
	}
	if got := out.String(); got != "streamchat 0.1.0-beta.1\n" {
		t.Fatalf("version output=%q", got)
	}
}

func TestOutboundTargetStatePersistsAcrossSimulatedSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streamchat", "client.json")
	state := clientstate.New(path)
	first := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}})
	configureOutboundState(first, state)
	if _, err := first.Process(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	if first.Selected() != "kick" || state.Load().LastOutboundTarget != "kick" {
		t.Fatalf("selected=%q state=%+v", first.Selected(), state.Load())
	}
	second := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}})
	configureOutboundState(second, state)
	if second.Selected() != "kick" || terminalui.TargetLabel(second.Selected()) != "KICK" {
		t.Fatalf("restored=%q label=%q", second.Selected(), terminalui.TargetLabel(second.Selected()))
	}
}

func TestTwitchTargetStateRestoresOnlyWhenAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streamchat", "client.json")
	state := clientstate.New(path)
	twitchSender := &recordingOutboundSender{}
	first := outbound.NewTargets(outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: twitchSender})
	configureOutboundState(first, state)
	if _, err := first.Process(context.Background(), "/twitch hello"); err != nil {
		t.Fatal(err)
	}
	if state.Load().LastOutboundTarget != "twitch" || !reflect.DeepEqual(twitchSender.messages, []string{"hello"}) {
		t.Fatalf("state=%+v messages=%v", state.Load(), twitchSender.messages)
	}
	restoredSender := &recordingOutboundSender{}
	restored := outbound.NewTargets(outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: restoredSender})
	configureOutboundState(restored, state)
	if restored.Selected() != "twitch" || terminalui.TargetLabel(restored.Selected()) != "Twitch" {
		t.Fatalf("selected=%q label=%q", restored.Selected(), terminalui.TargetLabel(restored.Selected()))
	}
	if _, err := restored.Process(context.Background(), "after restart"); err != nil || !reflect.DeepEqual(restoredSender.messages, []string{"after restart"}) {
		t.Fatalf("messages=%v err=%v", restoredSender.messages, err)
	}
	unavailable := outbound.NewTargets(outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: &recordingOutboundSender{}, Unavailable: true})
	configureOutboundState(unavailable, state)
	if unavailable.Selected() != "" || terminalui.TargetLabel(unavailable.Selected()) != "NONE" {
		t.Fatalf("unavailable selected=%q", unavailable.Selected())
	}
}

func TestPrepareTwitchRejectsReadOnlyAuthorizationForSending(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"client_id":"client","user_id":"sender","login":"sender","scopes":["user:read:chat"],"expires_in":3600}`))
	}))
	defer s.Close()
	c := config.Defaults()
	c.Twitch.ClientID = "client"
	c.Twitch.ClientSecret = "client-secret"
	c.Twitch.AccessToken = "access-token"
	c.Twitch.Channel = "channel"
	c.Twitch.APIBaseURL = s.URL
	c.Twitch.OAuthBaseURL = s.URL
	_, err := prepareTwitch(context.Background(), &c, filepath.Join(t.TempDir(), "config.json"))
	if !errors.Is(err, twitch.ErrWriteScope) || !strings.Contains(err.Error(), "streamchat setup twitch") {
		t.Fatalf("err=%v", err)
	}
	for _, secret := range []string{"client-secret", "access-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked: %v", err)
		}
	}
}

func TestPrepareTwitchRefreshPreservesRuntimeAuthorizationWithoutManagementScope(t *testing.T) {
	requests := map[string]int{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "old-refresh" {
				t.Fatalf("form=%v err=%v", r.Form, err)
			}
			_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600,"scope":["user:read:chat","user:write:chat","moderator:read:followers","channel:read:subscriptions"]}`))
		case "/validate":
			if r.Header.Get("Authorization") != "OAuth new-access" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"client_id":"client","user_id":"sender","login":"sender","scopes":["user:read:chat","user:write:chat","moderator:read:followers","channel:read:subscriptions"],"expires_in":3600}`))
		case "/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"broadcaster","login":"channel","display_name":"Channel"}]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Twitch.ClientID = "client"
	c.Twitch.ClientSecret = "secret"
	c.Twitch.AccessToken = "old-access"
	c.Twitch.RefreshToken = "old-refresh"
	c.Twitch.TokenExpiry = time.Now().Add(-time.Minute)
	c.Twitch.Channel = "channel"
	c.Twitch.APIBaseURL = s.URL
	c.Twitch.OAuthBaseURL = s.URL
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	runtime, err := prepareTwitch(context.Background(), &c, path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Identity.UserID != "sender" || runtime.Channel.ID != "broadcaster" || c.Twitch.AccessToken != "new-access" || c.Twitch.RefreshToken != "old-refresh" || requests["/token"] != 1 || requests["/validate"] != 1 || requests["/users"] != 1 {
		t.Fatalf("runtime=%+v config=%+v requests=%v", runtime, c.Twitch, requests)
	}
	persisted, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Twitch.AccessToken != "new-access" || persisted.Twitch.RefreshToken != "old-refresh" {
		t.Fatalf("persisted tokens access=%q refresh=%q", persisted.Twitch.AccessToken, persisted.Twitch.RefreshToken)
	}
}

func TestOutboundTargetStateInvalidOrUnavailableFallsBackToNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	state := clientstate.New(path)
	missing := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}})
	configureOutboundState(missing, state)
	if missing.Selected() != "" {
		t.Fatalf("missing state selected=%q", missing.Selected())
	}
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}})
	configureOutboundState(corrupt, state)
	if corrupt.Selected() != "" {
		t.Fatalf("corrupt state selected=%q", corrupt.Selected())
	}
	if err := state.Save(clientstate.State{LastOutboundTarget: "youtube"}); err != nil {
		t.Fatal(err)
	}
	invalid := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}})
	configureOutboundState(invalid, state)
	if invalid.Selected() != "" || terminalui.TargetLabel(invalid.Selected()) != "NONE" {
		t.Fatalf("invalid selected=%q", invalid.Selected())
	}
	if err := state.Save(clientstate.State{LastOutboundTarget: "kick"}); err != nil {
		t.Fatal(err)
	}
	unavailable := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}, Unavailable: true})
	configureOutboundState(unavailable, state)
	if unavailable.Selected() != "" {
		t.Fatalf("unavailable selected=%q", unavailable.Selected())
	}
}

func TestRetiredKickAliasIsRejectedAfterRestoredSelection(t *testing.T) {
	sender := &recordingOutboundSender{}
	targets := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: sender})
	if !targets.Restore("kick") {
		t.Fatal("failed to restore canonical Kick target")
	}
	registerRetiredTargetCommands(targets)
	var errw bytes.Buffer
	processInputLine(context.Background(), "/kk hello", targets, io.Discard, &errw)
	if len(sender.messages) != 0 {
		t.Fatalf("retired command reached chat: %v", sender.messages)
	}
	if targets.Selected() != "kick" || !strings.Contains(errw.String(), "use /kick") {
		t.Fatalf("selected=%q error=%q", targets.Selected(), errw.String())
	}
}

func TestSetupArgumentsAcceptConfigAfterPlatform(t *testing.T) {
	selected, path, err := setupArguments([]string{"youtube-server", "--config", "/etc/streamchat/config.json"})
	if err != nil || len(selected) != 1 || selected[0] != "youtube-server" || path != "/etc/streamchat/config.json" {
		t.Fatalf("selected=%v path=%q err=%v", selected, path, err)
	}
}

func TestSetupArgumentsAcceptDedicatedTwitchBot(t *testing.T) {
	selected, path, err := setupArguments([]string{"twitch-bot", "--config=/tmp/streamchat.json"})
	if err != nil || len(selected) != 1 || selected[0] != "twitch-bot" || path != "/tmp/streamchat.json" {
		t.Fatalf("selected=%v path=%q err=%v", selected, path, err)
	}
}

func TestSetupArgumentsAcceptDedicatedKickBot(t *testing.T) {
	selected, path, err := setupArguments([]string{"kick-bot", "--config=/tmp/streamchat.json"})
	if err != nil || len(selected) != 1 || selected[0] != "kick-bot" || path != "/tmp/streamchat.json" {
		t.Fatalf("selected=%v path=%q err=%v", selected, path, err)
	}
}

func TestSetupArgumentsAcceptDedicatedYouTubeBot(t *testing.T) {
	selected, path, err := setupArguments([]string{"youtube-bot", "--config=/tmp/streamchat.json"})
	if err != nil || len(selected) != 1 || selected[0] != "youtube-bot" || path != "/tmp/streamchat.json" {
		t.Fatalf("selected=%v path=%q err=%v", selected, path, err)
	}
}

type fakeAdapter struct {
	name    string
	message chat.Message
}

func (f fakeAdapter) Name() string { return f.name }
func (f fakeAdapter) Run(ctx context.Context, out chan<- chat.Message) error {
	select {
	case out <- f.message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRunMultipleAdapters(t *testing.T) {
	now := time.Now()
	adapters := []chat.Adapter{fakeAdapter{"youtube", chat.Message{ID: "1", Platform: chat.PlatformYouTube, Timestamp: now, AuthorDisplayName: "one", Text: "hello", EventType: chat.EventMessage}}, fakeAdapter{"twitch", chat.Message{ID: "2", Platform: chat.PlatformTwitch, Timestamp: now.Add(time.Millisecond), AuthorDisplayName: "two", Text: "world", EventType: chat.EventMessage}}}
	c := config.Defaults()
	c.NoColor = true
	var out, errw bytes.Buffer
	if err := runAdapters(context.Background(), adapters, c, nil, nil, nil, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[YouTube]") || !strings.Contains(out.String(), "[Twitch]") {
		t.Fatalf("%s / %s", out.String(), errw.String())
	}
}

func TestRunAdaptersDisplaysKickHumanAndBotMessages(t *testing.T) {
	now := time.Now()
	var botRoles chat.RoleSet
	botRoles.Add(chat.RoleModerator)
	adapters := []chat.Adapter{
		fakeAdapter{"kick-human", chat.Message{ID: "kick-human", Platform: chat.PlatformKick, Timestamp: now, AuthorID: "2", AuthorDisplayName: "Viewer", Text: "human message", EventType: chat.EventMessage}},
		fakeAdapter{"kick-bot", chat.Message{ID: "kick-bot", Platform: chat.PlatformKick, Timestamp: now.Add(time.Millisecond), AuthorID: "22", AuthorDisplayName: "BotRix", Badges: []chat.Badge{{Type: "bot", Text: "Bot"}, {Type: "moderator", Text: "Moderator"}}, Roles: botRoles, Text: "bot message", EventType: chat.EventMessage}},
	}
	c := config.Defaults()
	c.NoColor = true
	var out, errw bytes.Buffer
	if err := runAdapters(context.Background(), adapters, c, nil, nil, nil, &out, &errw); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Viewer") || !strings.Contains(rendered, "human message") {
		t.Fatalf("ordinary Kick message missing: %q / %q", rendered, errw.String())
	}
	if !strings.Contains(rendered, "🛡️") || !strings.Contains(rendered, "BotRix") || !strings.Contains(rendered, "bot message") {
		t.Fatalf("Kick bot message missing or metadata changed: %q / %q", rendered, errw.String())
	}
}

type blockingSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSender) Send(ctx context.Context, _ string) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type triggeredAdapter struct{ trigger <-chan struct{} }

func (a triggeredAdapter) Name() string { return "kick" }
func (a triggeredAdapter) Run(ctx context.Context, out chan<- chat.Message) error {
	select {
	case <-a.trigger:
	case <-ctx.Done():
		return ctx.Err()
	}
	message := chat.Message{ID: "incoming", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "viewer", Text: "still live", EventType: chat.EventMessage}
	select {
	case out <- message:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

type channelWriter struct{ writes chan string }

func (w channelWriter) Write(p []byte) (int, error) {
	w.writes <- string(p)
	return len(p), nil
}

func TestIncomingRendersWhileOutboundSendIsActive(t *testing.T) {
	sender := &blockingSender{started: make(chan struct{}), release: make(chan struct{})}
	targets := outbound.New(map[string]outbound.Sender{"kick": sender})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan string, 2)
	done := make(chan error, 1)
	c := config.Defaults()
	c.NoColor = true
	go func() {
		done <- runAdapters(ctx, []chat.Adapter{triggeredAdapter{trigger: sender.started}}, c, strings.NewReader("/kick sending\n"), targets, nil, channelWriter{writes: writes}, io.Discard)
	}()
	select {
	case line := <-writes:
		if !strings.Contains(line, "still live") {
			t.Fatalf("unexpected render: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incoming chat was not rendered while outbound send was blocked")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAdapters did not stop")
	}
}

func TestNonTTYFallbackDisplaysNoTargetInstructionWithoutANSI(t *testing.T) {
	var errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("hello\n"), outbound.New(map[string]outbound.Sender{"kick": &recordingOutboundSender{}}), io.Discard, &errw, func() {})
	if got := strings.TrimSpace(errw.String()); got != outbound.NoTargetInstruction {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(errw.String(), "\x1b") {
		t.Fatalf("non-TTY fallback emitted terminal controls: %q", errw.String())
	}
}

func TestNonTTYRunDoesNotFetchOrRenderStatusArea(t *testing.T) {
	provider := &scriptedStatusProvider{statuses: []terminalui.StreamStatus{{Title: "should not render"}}}
	message := chat.Message{ID: "1", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "viewer", Text: "hello", EventType: chat.EventMessage}
	var out bytes.Buffer
	if err := runAdapters(context.Background(), []chat.Adapter{fakeAdapter{name: "kick", message: message}}, config.Defaults(), nil, nil, provider, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 0 || strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "Title:") || strings.Contains(out.String(), strings.Repeat("-", 20)) {
		t.Fatalf("calls=%d output=%q", provider.callCount(), out.String())
	}
}

type scriptedStatusProvider struct {
	mu       sync.Mutex
	statuses []terminalui.StreamStatus
	errors   []error
	calls    int
}

func (p *scriptedStatusProvider) StreamStatus(context.Context) (terminalui.StreamStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	var status terminalui.StreamStatus
	if len(p.statuses) > 0 {
		status = p.statuses[min(index, len(p.statuses)-1)]
	}
	var err error
	if len(p.errors) > 0 {
		err = p.errors[min(index, len(p.errors)-1)]
	}
	return status, err
}

func (p *scriptedStatusProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type recordingStatusDisplay struct {
	mu       sync.Mutex
	statuses []terminalui.StreamStatus
}

func (d *recordingStatusDisplay) SetStatus(status terminalui.StreamStatus) {
	d.mu.Lock()
	d.statuses = append(d.statuses, status)
	d.mu.Unlock()
}

func (d *recordingStatusDisplay) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.statuses)
}

func (d *recordingStatusDisplay) last() terminalui.StreamStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.statuses) == 0 {
		return terminalui.StreamStatus{}
	}
	return d.statuses[len(d.statuses)-1]
}

type blockingStatusProvider struct {
	started chan struct{}
	release chan struct{}
	status  terminalui.StreamStatus
}

func (p *blockingStatusProvider) StreamStatus(context.Context) (terminalui.StreamStatus, error) {
	close(p.started)
	<-p.release
	return p.status, nil
}

func TestTargetStatusRouterSwitchesKickAndTwitchImmediately(t *testing.T) {
	kickStatus := terminalui.StreamStatus{Title: "Kick title", Category: "Kick category", Live: true, ViewerCount: 10}
	twitchStatus := terminalui.StreamStatus{Title: "Twitch title", Category: "Twitch category", Live: true, ViewerCount: 20}
	router := newTargetStatusRouter(map[string]statusProvider{
		"kick":   &scriptedStatusProvider{statuses: []terminalui.StreamStatus{kickStatus}},
		"twitch": &scriptedStatusProvider{statuses: []terminalui.StreamStatus{twitchStatus}},
	}, "kick")
	display := &recordingStatusDisplay{}
	refreshes := 0
	router.setStatusDisplay(display)
	router.setStatusRefresh(func() { refreshes++ })
	status, err := router.StreamStatus(context.Background())
	if err != nil || status != kickStatus {
		t.Fatalf("kick status=%+v err=%v", status, err)
	}
	router.Select("twitch")
	if refreshes != 1 || !display.last().Unavailable {
		t.Fatalf("Twitch selection refreshes=%d display=%+v", refreshes, display.last())
	}
	status, err = router.StreamStatus(context.Background())
	if err != nil || status != twitchStatus {
		t.Fatalf("Twitch status=%+v err=%v", status, err)
	}
	router.Select("kick")
	if refreshes != 2 || !display.last().Unavailable {
		t.Fatalf("Kick selection refreshes=%d display=%+v", refreshes, display.last())
	}
	status, err = router.StreamStatus(context.Background())
	if err != nil || status != kickStatus {
		t.Fatalf("restored Kick status=%+v err=%v", status, err)
	}
}

func TestSlashTargetCommandsSwitchStatusSource(t *testing.T) {
	targets := outbound.NewTargets(
		outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}},
		outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: &recordingOutboundSender{}},
	)
	router := newTargetStatusRouter(map[string]statusProvider{
		"kick":   &scriptedStatusProvider{statuses: []terminalui.StreamStatus{{Title: "Kick"}}},
		"twitch": &scriptedStatusProvider{statuses: []terminalui.StreamStatus{{Title: "Twitch"}}},
	}, targets.Selected())
	refreshes := 0
	router.setStatusRefresh(func() { refreshes++ })
	targets.AddSelectionChanged(router.Select)
	if err := targets.Handle(context.Background(), "/twitch"); err != nil {
		t.Fatal(err)
	}
	status, err := router.StreamStatus(context.Background())
	if err != nil || status.Title != "Twitch" {
		t.Fatalf("Twitch status=%+v err=%v", status, err)
	}
	if err = targets.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	status, err = router.StreamStatus(context.Background())
	if err != nil || status.Title != "Kick" || refreshes != 2 {
		t.Fatalf("Kick status=%+v refreshes=%d err=%v", status, refreshes, err)
	}
}

func TestTargetStatusRouterRejectsStaleCrossProviderResult(t *testing.T) {
	kick := &blockingStatusProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		status:  terminalui.StreamStatus{Title: "stale Kick"},
	}
	twitchStatus := terminalui.StreamStatus{Title: "current Twitch", Live: true, ViewerCount: 7}
	router := newTargetStatusRouter(map[string]statusProvider{
		"kick":   kick,
		"twitch": &scriptedStatusProvider{statuses: []terminalui.StreamStatus{twitchStatus}},
	}, "kick")
	display := &recordingStatusDisplay{}
	router.setStatusDisplay(display)
	type result struct {
		status terminalui.StreamStatus
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, err := router.StreamStatus(context.Background())
		done <- result{status: status, err: err}
	}()
	<-kick.started
	router.Select("twitch")
	close(kick.release)
	stale := <-done
	if !errors.Is(stale.err, errStaleStatus) || !display.last().Unavailable {
		t.Fatalf("stale result=%+v display=%+v", stale, display.last())
	}
	status, err := router.StreamStatus(context.Background())
	if err != nil || status != twitchStatus {
		t.Fatalf("Twitch status=%+v err=%v", status, err)
	}
}

func TestPersistedTwitchTargetStartsWithTwitchStatusProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	state := clientstate.New(path)
	if err := state.Save(clientstate.State{LastOutboundTarget: "twitch"}); err != nil {
		t.Fatal(err)
	}
	targets := outbound.NewTargets(
		outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingOutboundSender{}},
		outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: &recordingOutboundSender{}},
	)
	configureOutboundState(targets, state)
	want := terminalui.StreamStatus{Title: "Persisted Twitch", Live: false}
	router := newTargetStatusRouter(map[string]statusProvider{
		"kick":   &scriptedStatusProvider{statuses: []terminalui.StreamStatus{{Title: "wrong Kick"}}},
		"twitch": &scriptedStatusProvider{statuses: []terminalui.StreamStatus{want}},
	}, targets.Selected())
	got, err := router.StreamStatus(context.Background())
	if err != nil || got != want {
		t.Fatalf("selected=%q status=%+v err=%v", targets.Selected(), got, err)
	}
}

func TestStatusRefresherRunsImmediatelyPeriodicallyAndPreservesPriorOnFailure(t *testing.T) {
	known := terminalui.StreamStatus{Title: "Known", Category: "Games", Live: true, ViewerCount: 9}
	provider := &scriptedStatusProvider{statuses: []terminalui.StreamStatus{known}, errors: []error{nil, errors.New("temporary failure")}}
	display := &recordingStatusDisplay{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runStatusRefresher(ctx, 5*time.Millisecond, make(chan struct{}), provider, display)
		close(done)
	}()
	deadline := time.After(time.Second)
	for provider.callCount() < 2 {
		select {
		case <-deadline:
			t.Fatal("periodic status refresh did not run")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	if display.count() != 1 {
		t.Fatalf("transient failure replaced prior status; updates=%d", display.count())
	}
}

func TestSuccessfulTitleAndCategoryTriggerImmediateStatusRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/categories":
			_, _ = io.WriteString(w, `{"data":[{"id":123,"name":"Just Chatting"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/channels":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "token", APIBaseURL: server.URL}, http: server.Client()}
	refreshes := make(chan struct{}, 2)
	client.setStatusRefresh(func() { refreshes <- struct{}{} })
	if _, err := client.Title(context.Background(), "New title"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Category(context.Background(), "Just Chatting"); err != nil {
		t.Fatal(err)
	}
	if len(refreshes) != 2 {
		t.Fatalf("immediate refresh signals=%d", len(refreshes))
	}
}

func TestKickOutboundReceiptCarriesProviderIDAndStableChatterIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/chat":
			_, _ = io.WriteString(w, `{"data":{"is_sent":true,"message_id":"kick-message-id"}}`)
		case "/users":
			_, _ = io.WriteString(w, `{"data":[{"user_id":100,"name":"Streamer"}]}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	sender := &kickOutboundSender{config: config.Kick{AccessToken: "token", BroadcasterID: "100", APIBaseURL: server.URL}, http: server.Client()}
	receipt, err := sender.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := outbound.SentMessage{ID: "kick-message-id", AuthorID: "100", AuthorDisplayName: "Streamer"}
	if receipt != want {
		t.Fatalf("receipt=%+v want=%+v", receipt, want)
	}
}

func TestTwitchOutboundReceiptUsesConfiguredAuthenticatedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"message_id":"twitch-message-id","is_sent":true}]}`)
	}))
	defer server.Close()
	auth := twitch.NewUserClient(&twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, ClientID: "client", AccessToken: "token"}, twitch.RequiredChatScopes)
	sender := &twitchOutboundSender{chat: twitch.NewChatSenderWithUserClient(auth, "channel", "200"), userID: "200", userLogin: "TwitchUser"}
	receipt, err := sender.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := outbound.SentMessage{ID: "twitch-message-id", AuthorID: "200", AuthorDisplayName: "TwitchUser"}
	if receipt != want {
		t.Fatalf("receipt=%+v want=%+v", receipt, want)
	}
}

func TestSuccessfulTwitchTitleAndCategoryUseCanonicalResultAndRefreshStatus(t *testing.T) {
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/search/categories":
			_, _ = io.WriteString(w, `{"data":[{"id":"509658","name":"Just Chatting"}]}`)
		case request.Method == http.MethodPatch && request.URL.Path == "/channels":
			patches++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	api := &twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "token"}
	auth := twitch.NewUserClient(api, twitch.SetupScopes)
	client := &twitchOutboundSender{
		chat:    twitch.NewChatSenderWithUserClient(auth, "100", "100"),
		channel: twitch.NewChannelClient(auth, "100", "100"),
	}
	refreshes := 0
	client.setStatusRefresh(func() { refreshes++ })
	result, err := client.Title(context.Background(), "Twitch title")
	if err != nil || result != "Title updated: Twitch title" {
		t.Fatalf("title result=%q err=%v", result, err)
	}
	result, err = client.Category(context.Background(), "just chatting")
	if err != nil || result != "Category updated: Just Chatting" {
		t.Fatalf("category result=%q err=%v", result, err)
	}
	if patches != 2 || refreshes != 2 {
		t.Fatalf("patches=%d refreshes=%d", patches, refreshes)
	}
}

func TestTwitchCategoryChecksManagementScopeBeforeLookup(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	api := &twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, ClientID: "client", AccessToken: "token"}
	auth := twitch.NewUserClient(api, twitch.RequiredChatScopes)
	client := &twitchOutboundSender{channel: twitch.NewChannelClient(auth, "100", "100")}
	_, err := client.Category(context.Background(), "Just Chatting")
	if !errors.Is(err, twitch.ErrManageScope) || !strings.Contains(err.Error(), "streamchat setup twitch") || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestEmptyTitlePrintsUsageWithoutAuthorization(t *testing.T) {
	client := &kickOutboundSender{}
	targets := outbound.New(map[string]outbound.Sender{"kick": client})
	targets.RegisterControl("title", outbound.ControlFunc(client.Title))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/title\n"), targets, &out, &errw, func() {})
	if got := strings.TrimSpace(out.String()); got != "Usage: /title NEW STREAM TITLE" {
		t.Fatalf("output=%q", got)
	}
	if errw.Len() != 0 {
		t.Fatalf("unexpected error: %s", errw.String())
	}
}

func TestTitlePrintsSuccessOnlyAfterKickConfirms(t *testing.T) {
	confirmed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/channels" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		confirmed = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", APIBaseURL: server.URL}, http: server.Client()}
	targets := outbound.New(map[string]outbound.Sender{"kick": client})
	targets.RegisterControl("title", outbound.ControlFunc(client.Title))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/title New title\n"), targets, &out, &errw, func() {})
	if !confirmed || strings.TrimSpace(out.String()) != "Title updated: New title" || errw.Len() != 0 {
		t.Fatalf("confirmed=%v out=%q err=%q", confirmed, out.String(), errw.String())
	}
}

func TestCategoryPrintsResolvedNameAfterKickConfirms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/categories":
			_, _ = io.WriteString(w, `{"data":[{"id":123,"name":"Just Chatting"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/channels":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", APIBaseURL: server.URL}, http: server.Client()}
	targets := outbound.New(map[string]outbound.Sender{"kick": client})
	targets.RegisterControl("category", outbound.ControlFunc(client.Category))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/category Just Chatting\n"), targets, &out, &errw, func() {})
	if strings.TrimSpace(out.String()) != "Category updated: Just Chatting" || errw.Len() != 0 {
		t.Fatalf("out=%q err=%q", out.String(), errw.String())
	}
}

func TestModerationCommandsRequireExplicitPlatform(t *testing.T) {
	client := &kickOutboundSender{}
	moderation := newModerationControls(client)
	targets := outbound.New(map[string]outbound.Sender{"kick": client})
	targets.RegisterControl("ban", outbound.ControlFunc(moderation.Ban))
	targets.RegisterControl("timeout", outbound.ControlFunc(moderation.Timeout))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/ban\n/ban user\n/timeout user 10m\n"), targets, &out, &errw, func() {})
	if got := out.String(); strings.Count(got, "Usage: /ban PLATFORM USER") != 2 || !strings.Contains(got, "Usage: /timeout PLATFORM USER DURATION") {
		t.Fatalf("output=%q", got)
	}
	if errw.Len() != 0 {
		t.Fatalf("unexpected error=%q", errw.String())
	}
}

func TestModerationRejectsUnsupportedPlatformWithoutAPIRequestOrTargetInference(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", BroadcasterID: "123", APIBaseURL: server.URL}, http: server.Client()}
	moderation := newModerationControls(client)
	sender := &recordingOutboundSender{}
	targets := outbound.New(map[string]outbound.Sender{"kick": sender})
	targets.RegisterControl("ban", outbound.ControlFunc(moderation.Ban))
	targets.RegisterControl("timeout", outbound.ControlFunc(moderation.Timeout))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/kick\n/ban user\n/timeout user 10m\n/ban foo user\n/timeout foo user 10m\n"), targets, &out, &errw, func() {})
	if requests != 0 || len(sender.messages) != 0 {
		t.Fatalf("API requests=%d chat=%v", requests, sender.messages)
	}
	if got := out.String(); !strings.Contains(got, "Usage: /ban PLATFORM USER") || !strings.Contains(got, "Usage: /timeout PLATFORM USER DURATION") || strings.Count(got, "Unsupported moderation platform: foo. Supported: kick, twitch.") != 2 {
		t.Fatalf("output=%q", got)
	}
	if targets.Selected() != "kick" || errw.Len() != 0 {
		t.Fatalf("selected=%q err=%q", targets.Selected(), errw.String())
	}
}

func TestModerationControlsSucceedAndNeverBecomeChat(t *testing.T) {
	moderationRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/channels":
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"targetuser"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/moderation/bans":
			moderationRequests++
			_, _ = io.WriteString(w, `{"data":{},"message":"OK"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", BroadcasterID: "123", APIBaseURL: server.URL}, http: server.Client()}
	moderation := newModerationControls(client)
	sender := &recordingOutboundSender{}
	targets := outbound.New(map[string]outbound.Sender{"kick": sender})
	targets.RegisterControl("ban", outbound.ControlFunc(moderation.Ban))
	targets.RegisterControl("timeout", outbound.ControlFunc(moderation.Timeout))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/ban kick TargetUser\n/kick\n/timeout kick TargetUser 10m\nhello\n"), targets, &out, &errw, func() {})
	if moderationRequests != 2 || !reflect.DeepEqual(sender.messages, []string{"hello"}) {
		t.Fatalf("moderation=%d chat=%v", moderationRequests, sender.messages)
	}
	if got := out.String(); !strings.Contains(got, "Banned: targetuser") || !strings.Contains(got, "Timed out: targetuser for 10m") {
		t.Fatalf("output=%q", got)
	}
	if errw.Len() != 0 || targets.Selected() != "kick" {
		t.Fatalf("selected=%q err=%q", targets.Selected(), errw.String())
	}
}

func TestModerationCommandDoesNotDeleteArchiveRows(t *testing.T) {
	store, err := archivepkg.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archived := chat.Message{ID: "kick-message", Platform: chat.PlatformKick, ChannelID: "123", Timestamp: time.Now(), AuthorID: "456", AuthorDisplayName: "targetuser", Text: "historical message", EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), archived); storeErr != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, storeErr)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"targetuser"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{},"message":"OK"}`)
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", BroadcasterID: "123", APIBaseURL: server.URL}, http: server.Client()}
	moderation := newModerationControls(client)
	if result, moderationErr := moderation.Ban(context.Background(), "kick targetuser"); moderationErr != nil || result != "Banned: targetuser" {
		t.Fatalf("result=%q err=%v", result, moderationErr)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Total != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

type recordingLocalDisplay struct {
	all       int
	platforms []string
	authors   []string
	removed   int
}

func (d *recordingLocalDisplay) CleanAll() (int, error) {
	d.all++
	return d.removed, nil
}

func (d *recordingLocalDisplay) CleanPlatform(platform string) (int, error) {
	d.platforms = append(d.platforms, platform)
	return d.removed, nil
}

func (d *recordingLocalDisplay) CleanAuthor(author string) (int, error) {
	d.authors = append(d.authors, author)
	return d.removed, nil
}

func TestCleanCommandsAreLocalAndPreserveSelectedTarget(t *testing.T) {
	display := &recordingLocalDisplay{removed: 1}
	sender := &recordingOutboundSender{}
	targets := outbound.New(map[string]outbound.Sender{"kick": sender})
	targets.RegisterControl("clean", outbound.ControlFunc(cleanController{display: display}.Clean))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/kick\n/clean streamchat\n/clean kick\n/clean twitch\n/clean bOtRiX\nhello\n"), targets, &out, &errw, func() {})
	if display.all != 1 || !reflect.DeepEqual(display.platforms, []string{"kick", "twitch"}) || !reflect.DeepEqual(display.authors, []string{"bOtRiX"}) {
		t.Fatalf("display=%+v", display)
	}
	if !reflect.DeepEqual(sender.messages, []string{"hello"}) || targets.Selected() != "kick" {
		t.Fatalf("chat=%v selected=%q", sender.messages, targets.Selected())
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Fatalf("out=%q err=%q", out.String(), errw.String())
	}
}

func TestCleanNoMatchReservedTargetsAndUsage(t *testing.T) {
	display := &recordingLocalDisplay{}
	targets := outbound.New(map[string]outbound.Sender{"kick": &recordingOutboundSender{}})
	targets.RegisterControl("clean", outbound.ControlFunc(cleanController{display: display}.Clean))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/clean\n/clean MissingUser\n/clean youtube\n/clean twitch\n"), targets, &out, &errw, func() {})
	got := out.String()
	for _, want := range []string{
		"Usage: /clean streamchat|kick|twitch|youtube|USER",
		"No displayed messages matched: MissingUser",
		"No displayed messages matched: youtube",
		"No displayed messages matched: twitch",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !reflect.DeepEqual(display.authors, []string{"MissingUser"}) || !reflect.DeepEqual(display.platforms, []string{"youtube", "twitch"}) || errw.Len() != 0 {
		t.Fatalf("display=%+v err=%q", display, errw.String())
	}
}

func TestCleanNeverInvokesProviderAPIAndNonTTYFallbackIsSane(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	provider := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", APIBaseURL: server.URL}, http: server.Client()}
	targets := outbound.New(map[string]outbound.Sender{"kick": provider})
	targets.RegisterControl("clean", outbound.ControlFunc(cleanController{}.Clean))
	var out, errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("/kick\n/clean streamchat\n"), targets, &out, &errw, func() {})
	if requests != 0 || targets.Selected() != "kick" || errw.Len() != 0 {
		t.Fatalf("requests=%d selected=%q err=%q", requests, targets.Selected(), errw.String())
	}
	if got := strings.TrimSpace(out.String()); got != "Local display cleaning requires an interactive terminal." || strings.Contains(got, "\x1b") {
		t.Fatalf("output=%q", got)
	}
}

func TestEveryCleanTargetPreservesArchiveRows(t *testing.T) {
	store, err := archivepkg.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, archived := range []chat.Message{
		{ID: "kick-historical", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "BotRix", Text: "keep forever", EventType: chat.EventMessage},
		{ID: "twitch-historical", Platform: chat.PlatformTwitch, Timestamp: time.Now(), AuthorDisplayName: "Viewer", Text: "keep forever", EventType: chat.EventMessage},
	} {
		if inserted, storeErr := store.Store(context.Background(), archived); storeErr != nil || !inserted {
			t.Fatalf("inserted=%v err=%v", inserted, storeErr)
		}
	}
	display := &recordingLocalDisplay{removed: 1}
	controller := cleanController{display: display}
	for _, argument := range []string{"streamchat", "kick", "twitch", "BotRix"} {
		if result, cleanErr := controller.Clean(context.Background(), argument); cleanErr != nil || result != "" {
			t.Fatalf("argument=%q result=%q err=%v", argument, result, cleanErr)
		}
		stats, statsErr := store.Stats(context.Background())
		if statsErr != nil || stats.Total != 2 {
			t.Fatalf("argument=%q stats=%+v err=%v", argument, stats, statsErr)
		}
	}
}

type recordingArchiveSource struct {
	mu       sync.Mutex
	ids      []string
	refs     []archivepkg.MessageReference
	platform chat.Platform
	since    time.Time
	err      error
}

func (s *recordingArchiveSource) MessageReferencesSince(_ context.Context, platform chat.Platform, since time.Time) ([]archivepkg.MessageReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platform = platform
	s.since = since
	return append([]archivepkg.MessageReference(nil), s.refs...), s.err
}

func (s *recordingArchiveSource) MessageIDsSince(_ context.Context, platform chat.Platform, since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platform = platform
	s.since = since
	return append([]string(nil), s.ids...), s.err
}

type recordingRemoteClear struct {
	mu      sync.Mutex
	ids     []string
	result  remoteClearResult
	err     error
	started chan struct{}
	release chan struct{}
}

func (p *recordingRemoteClear) ClearMessages(_ context.Context, ids []string) (remoteClearResult, error) {
	p.mu.Lock()
	p.ids = append([]string(nil), ids...)
	p.mu.Unlock()
	if p.started != nil {
		close(p.started)
	}
	if p.release != nil {
		<-p.release
	}
	return p.result, p.err
}

type recordingTwitchClear struct {
	recordingRemoteClear
	clearAll int
	allErr   error
}

func (p *recordingTwitchClear) ClearAll(context.Context) error {
	p.clearAll++
	return p.allErr
}

func TestRemoteClearKickSyntaxNoMessagesAndUnsupportedPlatforms(t *testing.T) {
	fixedNow := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	controller := &remoteClearController{source: &recordingArchiveSource{}, platforms: map[string]remoteClearPlatform{"kick": &recordingRemoteClear{}}, now: func() time.Time { return fixedNow }}
	tests := []struct {
		argument string
		want     string
	}{
		{"", "Usage: /clear kick|twitch [Nd] (example: /clear twitch 1d)"},
		{"kick", "No archived Kick messages to clear in the last 1d."},
		{"youtube", "Remote chat clearing is not implemented for: youtube"},
		{"twitch", "Remote chat clearing is not implemented for: twitch"},
		{"foo", "Unsupported clear platform: foo. Supported: kick, twitch."},
	}
	for _, test := range tests {
		got, err := controller.Execute(context.Background(), test.argument)
		if err != nil || got != test.want {
			t.Fatalf("argument=%q got=%q err=%v", test.argument, got, err)
		}
	}
}

func TestRemoteClearKickParsesArchiveWindows(t *testing.T) {
	fixedNow := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		argument string
		days     int
	}{{"kick", 1}, {"kick 1d", 1}, {"kick 3d", 3}, {"kick 7d", 7}} {
		source := &recordingArchiveSource{}
		controller := &remoteClearController{source: source, platforms: map[string]remoteClearPlatform{"kick": &recordingRemoteClear{}}, now: func() time.Time { return fixedNow }}
		if _, err := controller.Execute(context.Background(), test.argument); err != nil {
			t.Fatalf("argument=%q err=%v", test.argument, err)
		}
		wantSince := fixedNow.Add(-time.Duration(test.days) * 24 * time.Hour)
		if source.platform != chat.PlatformKick || !source.since.Equal(wantSince) {
			t.Fatalf("argument=%q platform=%q since=%s want=%s", test.argument, source.platform, source.since, wantSince)
		}
	}
}

func TestRemoteClearKickRejectsInvalidArchiveWindows(t *testing.T) {
	controller := &remoteClearController{source: &recordingArchiveSource{}, platforms: map[string]remoteClearPlatform{"kick": &recordingRemoteClear{}}}
	for _, argument := range []string{"kick 0d", "kick -1d", "kick 1h", "kick day", "kick 999999d"} {
		result, err := controller.Execute(context.Background(), argument)
		if err != nil || result != "clear window must be a positive number of days up to 3650 (example: 3d)" {
			t.Fatalf("argument=%q result=%q err=%v", argument, result, err)
		}
	}
}

func TestRemoteClearKickReportsPartialFailure(t *testing.T) {
	controller := &remoteClearController{
		source:    &recordingArchiveSource{ids: []string{"one", "two", "three"}},
		platforms: map[string]remoteClearPlatform{"kick": &recordingRemoteClear{result: remoteClearResult{Deleted: 2, Failed: 1}}},
	}
	result, err := controller.Execute(context.Background(), "kick")
	if err != nil || result != "Cleared Kick chat: 2 deleted, 1 failed" {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestRemoteClearKickUsesSnapshotAndPreservesOutboundTarget(t *testing.T) {
	fixedNow := time.Now().UTC()
	store, err := archivepkg.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, id := range []string{"first", "second"} {
		message := chat.Message{ID: id, Platform: chat.PlatformKick, Timestamp: fixedNow.Add(time.Duration(i-2) * time.Hour), EventType: chat.EventMessage}
		if inserted, storeErr := store.Store(context.Background(), message); storeErr != nil || !inserted {
			t.Fatalf("store %q inserted=%v err=%v", id, inserted, storeErr)
		}
	}
	provider := &recordingRemoteClear{result: remoteClearResult{Deleted: 2}, started: make(chan struct{}), release: make(chan struct{})}
	controller := &remoteClearController{source: store, platforms: map[string]remoteClearPlatform{"kick": provider}, now: func() time.Time { return fixedNow }}
	chatSender := &recordingOutboundSender{}
	targets := outbound.New(map[string]outbound.Sender{"kick": chatSender})
	targets.RegisterControl("clear", controller)
	if _, err := targets.Process(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := targets.Process(context.Background(), "/clear kick")
		if err == nil && result != "Cleared Kick chat: 2 messages" {
			err = errors.New("unexpected clear result: " + result)
		}
		done <- err
	}()
	<-provider.started
	newMessage := chat.Message{ID: "arrived-after-snapshot", Platform: chat.PlatformKick, Timestamp: fixedNow.Add(-30 * time.Minute), EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), newMessage); storeErr != nil || !inserted {
		t.Fatalf("concurrent store inserted=%v err=%v", inserted, storeErr)
	}
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.ids, []string{"first", "second"}) {
		t.Fatalf("clear IDs=%v", provider.ids)
	}
	if got, queryErr := store.MessageIDsSince(context.Background(), chat.PlatformKick, fixedNow.Add(-24*time.Hour)); queryErr != nil || !reflect.DeepEqual(got, []string{"first", "second", "arrived-after-snapshot"}) {
		t.Fatalf("archive IDs=%v err=%v", got, queryErr)
	}
	if targets.Selected() != "kick" {
		t.Fatalf("selected target changed: %q", targets.Selected())
	}
	if _, err := targets.Process(context.Background(), "still chat"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chatSender.messages, []string{"still chat"}) {
		t.Fatalf("control became chat: %v", chatSender.messages)
	}
}

func TestKickClearMultipleMessagesPartialFailureAndAlreadyDeleted(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		switch r.URL.Path {
		case "/chat/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/chat/fails":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"access_token=provider-secret"}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	sender := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", APIBaseURL: server.URL}, http: server.Client()}
	result, err := sender.ClearMessages(context.Background(), []string{"one", "gone", "fails"})
	if err != nil {
		t.Fatal(err)
	}
	if result != (remoteClearResult{Deleted: 2, Failed: 1}) {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(requested, []string{"/chat/one", "/chat/gone", "/chat/fails"}) {
		t.Fatalf("requests=%v", requested)
	}
}

func TestKickClearStopsBatchOnRateLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	sender := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", APIBaseURL: server.URL}, http: server.Client()}
	result, err := sender.ClearMessages(context.Background(), []string{"one", "two", "three"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result != (remoteClearResult{Failed: 3}) {
		t.Fatalf("requests=%d result=%+v", requests, result)
	}
}

func TestRemoteClearNeverDeletesArchiveRows(t *testing.T) {
	store, err := archivepkg.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message := chat.Message{ID: "historical", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "viewer", Text: "keep forever", EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), message); storeErr != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, storeErr)
	}
	controller := &remoteClearController{
		source:    store,
		platforms: map[string]remoteClearPlatform{"kick": &recordingRemoteClear{result: remoteClearResult{Deleted: 1}}},
		now:       time.Now,
	}
	if result, clearErr := controller.Execute(context.Background(), "kick"); clearErr != nil || result != "Cleared Kick chat: 1 message" {
		t.Fatalf("result=%q err=%v", result, clearErr)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Total != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestTwitchModerationRoutesWithoutDispatchingToKick(t *testing.T) {
	var banDurations []any
	twitchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users":
			_, _ = io.WriteString(w, `{"data":[{"id":"300","login":"targetuser","display_name":"TargetUser"}]}`)
		case "/moderation/bans":
			var payload struct {
				Data map[string]any `json:"data"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			banDurations = append(banDurations, payload.Data["duration"])
			_, _ = io.WriteString(w, `{"data":[{}]}`)
		default:
			t.Fatalf("unexpected Twitch request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer twitchServer.Close()
	kickRequests := 0
	kickServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { kickRequests++ }))
	defer kickServer.Close()
	auth := twitch.NewUserClient(&twitch.API{HTTP: twitchServer.Client(), APIBaseURL: twitchServer.URL, OAuthBaseURL: twitchServer.URL, ClientID: "client", AccessToken: "access"}, twitch.SetupScopes)
	twitchSender := &twitchOutboundSender{moderation: twitch.NewModerationClient(auth, "100", "200")}
	kickSender := &kickOutboundSender{config: config.Kick{AccessToken: "kick-access", BroadcasterID: "100", APIBaseURL: kickServer.URL}, http: kickServer.Client()}
	controls := newModerationControls(kickSender, twitchSender)
	if result, err := controls.Ban(context.Background(), "twitch TARGETUSER"); err != nil || result != "Banned: TargetUser" {
		t.Fatalf("ban result=%q err=%v", result, err)
	}
	if result, err := controls.Timeout(context.Background(), "twitch targetuser 30s"); err != nil || result != "Timed out: TargetUser for 30s" {
		t.Fatalf("timeout result=%q err=%v", result, err)
	}
	if kickRequests != 0 || len(banDurations) != 2 || banDurations[0] != nil || banDurations[1] != float64(30) {
		t.Fatalf("Kick requests=%d durations=%v", kickRequests, banDurations)
	}
}

func TestRemoteClearTwitchPlainUsesOneClearAllWithoutArchive(t *testing.T) {
	provider := &recordingTwitchClear{}
	source := &recordingArchiveSource{err: errors.New("archive must not be read")}
	controller := &remoteClearController{source: source, platforms: map[string]remoteClearPlatform{"twitch": provider}}
	result, err := controller.Execute(context.Background(), "twitch")
	if err != nil || result != "Twitch chat cleared." || provider.clearAll != 1 || source.platform != "" {
		t.Fatalf("result=%q clearAll=%d archivePlatform=%q err=%v", result, provider.clearAll, source.platform, err)
	}
}

func TestRemoteClearTwitchWindowUsesEligibleArchivedIDsAndReportsSixHourLimit(t *testing.T) {
	fixedNow := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	source := &recordingArchiveSource{refs: []archivepkg.MessageReference{
		{ID: "recent", Timestamp: fixedNow.Add(-time.Hour)},
		{ID: "boundary", Timestamp: fixedNow.Add(-6 * time.Hour)},
		{ID: "old", Timestamp: fixedNow.Add(-7 * time.Hour)},
	}}
	provider := &recordingTwitchClear{recordingRemoteClear: recordingRemoteClear{result: remoteClearResult{Deleted: 1}}}
	controller := &remoteClearController{source: source, platforms: map[string]remoteClearPlatform{"twitch": provider}, now: func() time.Time { return fixedNow }}
	result, err := controller.Execute(context.Background(), "twitch 1d")
	want := "Twitch: deleted 1 known messages; 2 archived messages were older than Twitch's 6-hour individual-delete limit."
	if err != nil || result != want || !reflect.DeepEqual(provider.ids, []string{"recent"}) || source.platform != chat.PlatformTwitch || !source.since.Equal(fixedNow.Add(-24*time.Hour)) {
		t.Fatalf("result=%q ids=%v platform=%q since=%s err=%v", result, provider.ids, source.platform, source.since, err)
	}
}

func TestTwitchClearBatchContinues404AndProtectedFailures(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		messageID := request.URL.Query().Get("message_id")
		requested = append(requested, messageID)
		switch messageID {
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "protected":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	auth := twitch.NewUserClient(&twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "access"}, twitch.SetupScopes)
	sender := &twitchOutboundSender{moderation: twitch.NewModerationClient(auth, "100", "200")}
	result, err := sender.ClearMessages(context.Background(), []string{"one", "gone", "protected", "two"})
	if err != nil || result != (remoteClearResult{Deleted: 2, Failed: 2}) || !reflect.DeepEqual(requested, []string{"one", "gone", "protected", "two"}) {
		t.Fatalf("result=%+v requested=%v err=%v", result, requested, err)
	}
}

func TestTwitchClearBatchStopsAuthorizationAndRateLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{{"unauthorized", http.StatusUnauthorized, twitch.ErrModerationAuthentication}, {"forbidden", http.StatusForbidden, twitch.ErrModerationPermission}, {"rate-limit", http.StatusTooManyRequests, twitch.ErrModerationRateLimit}} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			api := &twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "access"}
			auth := twitch.NewUserClient(api, twitch.SetupScopes)
			sender := &twitchOutboundSender{moderation: twitch.NewModerationClient(auth, "100", "200")}
			result, err := sender.ClearMessages(context.Background(), []string{"one", "two", "three"})
			if !errors.Is(err, test.want) || requests != 1 {
				t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
			}
			if test.status == http.StatusTooManyRequests && result.Failed != 3 {
				t.Fatalf("rate-limit result=%+v", result)
			}
		})
	}
}

func TestTwitchModerationAndClearNeverDeleteArchiveRows(t *testing.T) {
	fixedNow := time.Now().UTC()
	store, err := archivepkg.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message := chat.Message{ID: "historical", Platform: chat.PlatformTwitch, ChannelID: "100", Timestamp: fixedNow, AuthorID: "300", AuthorDisplayName: "TargetUser", Text: "keep forever", EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), message); storeErr != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, storeErr)
	}
	banCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users":
			_, _ = io.WriteString(w, `{"data":[{"id":"300","login":"targetuser","display_name":"TargetUser"}]}`)
		case "/moderation/bans":
			banCalls++
			if banCalls == 3 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{}]}`)
		case "/moderation/chat":
			if request.URL.Query().Get("message_id") == "fail" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	auth := twitch.NewUserClient(&twitch.API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "access"}, twitch.SetupScopes)
	sender := &twitchOutboundSender{moderation: twitch.NewModerationClient(auth, "100", "200")}
	controller := &remoteClearController{source: store, platforms: map[string]remoteClearPlatform{"twitch": sender}, now: func() time.Time { return fixedNow }}
	operations := []struct {
		name string
		run  func() error
	}{
		{"ban", func() error {
			_, operationErr := sender.BanUser(context.Background(), "targetuser")
			return operationErr
		}},
		{"timeout", func() error {
			_, operationErr := sender.TimeoutUser(context.Background(), "targetuser", "1s")
			return operationErr
		}},
		{"failed-ban", func() error {
			_, operationErr := sender.BanUser(context.Background(), "targetuser")
			return operationErr
		}},
		{"clear-all", func() error {
			_, operationErr := controller.Execute(context.Background(), "twitch")
			return operationErr
		}},
		{"batch-clear", func() error {
			_, operationErr := controller.Execute(context.Background(), "twitch 1d")
			return operationErr
		}},
		{"failed-clear", func() error {
			_, operationErr := sender.ClearMessages(context.Background(), []string{"fail"})
			return operationErr
		}},
	}
	for _, operation := range operations {
		_ = operation.run()
		stats, statsErr := store.Stats(context.Background())
		if statsErr != nil || stats.Total != 1 {
			t.Fatalf("operation=%s stats=%+v err=%v", operation.name, stats, statsErr)
		}
	}
}

func TestIncomingRendersWhileModerationRequestIsActive(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"targetuser"}]}`)
			return
		}
		close(started)
		<-release
		_, _ = io.WriteString(w, `{"data":{},"message":"OK"}`)
	}))
	defer server.Close()
	client := &kickOutboundSender{config: config.Kick{AccessToken: "access-token", BroadcasterID: "123", APIBaseURL: server.URL}, http: server.Client()}
	moderation := newModerationControls(client)
	targets := outbound.New(map[string]outbound.Sender{"kick": client})
	targets.RegisterControl("ban", outbound.ControlFunc(moderation.Ban))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan string, 2)
	done := make(chan error, 1)
	c := config.Defaults()
	c.NoColor = true
	go func() {
		done <- runAdapters(ctx, []chat.Adapter{triggeredAdapter{trigger: started}}, c, strings.NewReader("/ban kick targetuser\n"), targets, nil, channelWriter{writes: writes}, io.Discard)
	}()
	select {
	case line := <-writes:
		if !strings.Contains(line, "still live") {
			t.Fatalf("unexpected render: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incoming chat was not rendered while moderation was blocked")
	}
	close(release)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAdapters did not stop")
	}
}

type recordingOutboundSender struct{ messages []string }

func (s *recordingOutboundSender) Send(_ context.Context, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

type shutdownAdapter struct{ stopped chan struct{} }

func (a shutdownAdapter) Name() string { return "test" }
func (a shutdownAdapter) Run(ctx context.Context, _ chan<- chat.Message) error {
	<-ctx.Done()
	close(a.stopped)
	return nil
}

func TestInteractiveShutdownCommandsStopAdaptersAndDoNotSendChat(t *testing.T) {
	for _, command := range []string{"exit", "quit"} {
		for _, selected := range []bool{false, true} {
			name := command + "_without_target"
			if selected {
				name = command + "_after_kick_selection"
			}
			t.Run(name, func(t *testing.T) {
				sender := &recordingOutboundSender{}
				targets := outbound.New(map[string]outbound.Sender{"kick": sender})
				registerShutdownControls(targets)
				stopped := make(chan struct{})
				c := config.Defaults()
				var out, errw bytes.Buffer
				input := "/" + command + "\nnot sent\n"
				var wantMessages []string
				wantTarget := ""
				if selected {
					input = "/kick\nhello\n" + input
					wantMessages = []string{"hello"}
					wantTarget = "kick"
				}
				if err := runAdapters(context.Background(), []chat.Adapter{shutdownAdapter{stopped: stopped}}, c, strings.NewReader(input), targets, nil, &out, &errw); err != nil {
					t.Fatalf("shutdown returned an error: %v", err)
				}
				select {
				case <-stopped:
				default:
					t.Fatal("adapter was not stopped before shutdown returned")
				}
				if !reflect.DeepEqual(sender.messages, wantMessages) {
					t.Fatalf("chat messages=%v", sender.messages)
				}
				if targets.Selected() != wantTarget || out.Len() != 0 || errw.Len() != 0 {
					t.Fatalf("selected=%q out=%q err=%q", targets.Selected(), out.String(), errw.String())
				}
			})
		}
	}
}

func TestRemoteServerReplacesServerOwnedAdapters(t *testing.T) {
	c := config.Defaults()
	c.Client.ServerURL = "ws://127.0.0.1:8788/relay"
	c.RelayAuthToken = "0123456789abcdef0123456789abcdef"
	adapters := []chat.Adapter{
		fakeAdapter{name: "youtube"},
		fakeAdapter{name: "kick"},
		fakeAdapter{name: "twitch"},
	}
	got := useRemoteServer(adapters, c)
	names := make([]string, 0, len(got))
	for _, adapter := range got {
		names = append(names, adapter.Name())
	}
	if strings.Join(names, ",") != "server" {
		t.Fatalf("unexpected adapters: %v", names)
	}
}

func TestTwitchServerFailureDoesNotStopKickRelayAndCancellationShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	twitchErr := make(chan error, 1)
	twitchErr <- errors.New("EventSub disconnected")
	writes := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- waitForServer(ctx, cancel, serverErr, nil, twitchErr, channelWriter{writes: writes}) }()
	var output string
	select {
	case output = <-writes:
	case <-time.After(time.Second):
		t.Fatal("Twitch failure was not reported")
	}
	if !strings.Contains(output, "Kick and relay remain active") || !strings.Contains(output, "EventSub disconnected") {
		t.Fatalf("output=%q", output)
	}
	select {
	case err := <-done:
		t.Fatalf("Twitch failure stopped the server: %v", err)
	default:
	}
	cancel()
	serverErr <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server supervisor did not stop after cancellation")
	}
}

func TestSafeErrorRedactsSecrets(t *testing.T) {
	s := safeError(errors.New("request?access_token=secret-token&client_secret=secret-client relay_auth_token=raw-relay-secret Authorization: Bearer header-relay-secret next"))
	for _, secret := range []string{"secret-token", "secret-client", "raw-relay-secret", "header-relay-secret"} {
		if strings.Contains(s, secret) {
			t.Fatal(s)
		}
	}
}

func TestConfigShowRedactsEverySecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.YouTube.APIKey = "youtube-private"
	c.YouTube.ClientSecret = "youtube-client-private"
	c.YouTube.RefreshToken = "youtube-refresh-private"
	c.Kick.ClientSecret = "kick-private"
	c.Twitch.AccessToken = "twitch-private"
	c.RelayAuthToken = "relay-private"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "show", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, s := range []string{"youtube-private", "youtube-client-private", "youtube-refresh-private", "kick-private", "twitch-private", "relay-private"} {
		if strings.Contains(out.String(), s) {
			t.Fatalf("leaked %s", s)
		}
	}
	if !strings.Contains(out.String(), "<redacted>") {
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

func TestConfigCheckReportsDedicatedTwitchBotIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Bot.Twitch.AccessToken = "private-token"
	c.Bot.Twitch.UserLogin = "comradekip"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "check", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "Twitch bot:") || !strings.Contains(out.String(), "configured as comradekip") || strings.Contains(out.String(), "private-token") {
		t.Fatalf("unexpected config check output:\n%s", out.String())
	}
}

func TestConfigCheckReportsDedicatedKickBotIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Bot.Kick.AccessToken = "private-token"
	c.Bot.Kick.UserLogin = "ComradeKip"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "check", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "Kick bot:") || !strings.Contains(out.String(), "configured as ComradeKip") || strings.Contains(out.String(), "private-token") {
		t.Fatalf("unexpected config check output:\n%s", out.String())
	}
}

func TestConfigCheckReportsDedicatedYouTubeBotIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Bot.YouTube.AccessToken = "private-token"
	c.Bot.YouTube.UserLogin = "ComradeKip"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "check", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "YouTube bot:") || !strings.Contains(out.String(), "configured as ComradeKip") || strings.Contains(out.String(), "private-token") {
		t.Fatalf("unexpected config check output:\n%s", out.String())
	}
}

func TestConfigCheckExplainsKickPortalWebhookRelationship(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Kick.WebhookURL = "https://streamchat.sleepymario.com/webhooks/kick"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "check", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, want := range []string{
		"https://streamchat.sleepymario.com/webhooks/kick",
		"must match the webhook URL in the Kick developer portal",
		"local value is not sent to Kick",
		"streamchat kick subscribe",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("config check missing %q:\n%s", want, out.String())
		}
	}
}

func TestArchiveStatsCommandUsesSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "streamchat.db")
	store, err := archivepkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	message := chat.Message{ID: "yt-smoke", Platform: chat.PlatformYouTube, ChannelID: "broadcast", Timestamp: time.Now().UTC(), AuthorDisplayName: "viewer", Text: "persisted", EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), message); storeErr != nil || !inserted {
		t.Fatalf("store=%v, %v", inserted, storeErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	c := config.Defaults()
	c.Storage.SQLitePath = dbPath
	if err = config.Save(configPath, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"archive", "stats", "--config", configPath}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "Messages: 1") || !strings.Contains(out.String(), "youtube: 1") {
		t.Fatal(out.String())
	}
}
