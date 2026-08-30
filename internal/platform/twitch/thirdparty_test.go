package twitch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func TestThirdPartyEmotesLoadAndEnrich(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bttv/emotes/global":
			_, _ = w.Write([]byte(`[{"id":"global","code":"GlobalThing"},{"id":"old","code":"SameName"}]`))
		case "/bttv/users/twitch/100":
			_, _ = w.Write([]byte(`{"sharedEmotes":[{"id":"shared","code":"SharedThing"}],"channelEmotes":[{"id":"channel","code":"ChannelThing"}]}`))
		case "/ffz/set/global":
			_, _ = w.Write([]byte(`{"default_sets":[3],"sets":{"3":{"emoticons":[{"id":30,"name":"FFZGlobal","urls":{"1":"//cdn.frankerfacez.com/emote/30/1","4":"//cdn.frankerfacez.com/emote/30/4"}}]}}}`))
		case "/ffz/room/id/100":
			_, _ = w.Write([]byte(`{"sets":{"40":{"emoticons":[{"id":40,"name":"FFZThing","urls":{"1":"https://cdn.frankerfacez.com/emote/40/1","2":"https://cdn.frankerfacez.com/emote/40/2"}}]}}}`))
		case "/7tv/users/twitch/100":
			_, _ = w.Write([]byte(`{"emote_set":{"emotes":[{"id":"binding","name":"SevenThing","data":{"id":"seven","name":"SevenThing","animated":false}},{"id":"binding2","name":"SameName","data":{"id":"new","name":"SameName","animated":true}}]}}`))
		case "/7tv/emote-sets/global":
			_, _ = w.Write([]byte(`{"emotes":[{"id":"global-binding","name":"SevenGlobal","data":{"id":"seven-global","name":"SevenGlobal","animated":false}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	catalogue := newThirdPartyEmotes(server.Client(), server.URL+"/bttv", server.URL+"/ffz", server.URL+"/7tv")
	if err := catalogue.Load(context.Background(), "100"); err != nil {
		t.Fatal(err)
	}
	message := chat.Message{Platform: chat.PlatformTwitch, Text: "你好 GlobalThing ChannelThing FFZGlobal FFZThing SevenThing SameName"}
	message = catalogue.Enrich(message)
	if len(message.Emotes) != 6 {
		t.Fatalf("emotes=%+v", message.Emotes)
	}
	want := []struct {
		id, name, asset string
		start, end      int
	}{
		{"bttv-global", "GlobalThing", "https://cdn.betterttv.net/emote/global/3x", 3, 13},
		{"bttv-channel", "ChannelThing", "https://cdn.betterttv.net/emote/channel/3x", 15, 26},
		{"ffz-30", "FFZGlobal", "https://cdn.frankerfacez.com/emote/30/4", 28, 36},
		{"ffz-40", "FFZThing", "https://cdn.frankerfacez.com/emote/40/2", 38, 45},
		{"7tv-seven", "SevenThing", "https://cdn.7tv.app/emote/seven/4x.png", 47, 56},
		{"7tv-new", "SameName", "https://cdn.7tv.app/emote/new/4x.gif", 58, 65},
	}
	for index, expected := range want {
		got := message.Emotes[index]
		if got.ID != expected.id || got.Name != expected.name || got.URL != expected.asset || got.Start != expected.start || got.End != expected.end {
			t.Fatalf("emote[%d]=%+v want=%+v", index, got, expected)
		}
	}
}

func TestThirdPartyEmotesPreserveNativeRangesAndExactTokens(t *testing.T) {
	catalogue := newThirdPartyEmotes(nil, "", "", "")
	catalogue.byName = map[string]thirdPartyEmote{
		"Kappa": {id: "bttv-kappa", name: "Kappa", asset: "https://cdn.betterttv.net/emote/kappa/3x"},
		"KEKW":  {id: "7tv-kekw", name: "KEKW", asset: "https://cdn.7tv.app/emote/kekw/4x.png"},
	}
	message := chat.Message{Platform: chat.PlatformTwitch, Text: "Kappa KEKW! KEKW", Emotes: []chat.Emote{{ID: "25", Name: "Kappa", Start: 0, End: 4}}}
	message = catalogue.Enrich(message)
	if len(message.Emotes) != 2 || message.Emotes[0].ID != "25" || message.Emotes[1].ID != "7tv-kekw" || message.Emotes[1].Start != 12 || message.Emotes[1].End != 15 {
		t.Fatalf("emotes=%+v", message.Emotes)
	}
}

func TestThirdPartyEmotesPartialProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bttv/emotes/global" {
			_, _ = w.Write([]byte(`[{"id":"global","code":"GlobalThing"}]`))
			return
		}
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	catalogue := newThirdPartyEmotes(&http.Client{Timeout: time.Second}, server.URL+"/bttv", server.URL+"/ffz", server.URL+"/7tv")
	if err := catalogue.Load(context.Background(), "100"); err != nil {
		t.Fatalf("partial provider failure should retain successful catalogue: %v", err)
	}
	message := catalogue.Enrich(chat.Message{Platform: chat.PlatformTwitch, Text: "GlobalThing"})
	if len(message.Emotes) != 1 || message.Emotes[0].ID != "bttv-global" {
		t.Fatalf("emotes=%+v", message.Emotes)
	}
}
