package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SleepyMario/streamchat/internal/chat"
)

const (
	defaultBTTVAPI    = "https://api.betterttv.net/3/cached"
	defaultSevenTVAPI = "https://7tv.io/v3"
	maxCatalogueBytes = int64(8 << 20)
)

type thirdPartyEmote struct {
	id, name, asset string
}

// thirdPartyEmotes is an immutable-after-load catalogue. Loading is done once
// before EventSub starts; message enrichment is therefore local and cheap.
type thirdPartyEmotes struct {
	http                *http.Client
	bttvAPI, sevenTVAPI string
	mu                  sync.RWMutex
	byName              map[string]thirdPartyEmote
}

func newThirdPartyEmotes(client *http.Client, bttvAPI, sevenTVAPI string) *thirdPartyEmotes {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if bttvAPI == "" {
		bttvAPI = defaultBTTVAPI
	}
	if sevenTVAPI == "" {
		sevenTVAPI = defaultSevenTVAPI
	}
	return &thirdPartyEmotes{http: client, bttvAPI: strings.TrimRight(bttvAPI, "/"), sevenTVAPI: strings.TrimRight(sevenTVAPI, "/"), byName: map[string]thirdPartyEmote{}}
}

// Load fetches the global and channel catalogues concurrently. Later sources
// win on an exact name collision: channel catalogues over global catalogues,
// and 7TV over BTTV at each level. Partial success is accepted.
func (c *thirdPartyEmotes) Load(ctx context.Context, twitchChannelID string) error {
	twitchChannelID = strings.TrimSpace(twitchChannelID)
	if twitchChannelID == "" {
		return errors.New("Twitch channel ID is required for third-party emotes")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type result struct {
		order  int
		emotes []thirdPartyEmote
		err    error
	}
	results := make(chan result, 4)
	go func() {
		emotes, err := c.loadBTTVGlobal(ctx)
		results <- result{order: 0, emotes: emotes, err: err}
	}()
	go func() {
		emotes, err := c.loadSevenTVGlobal(ctx)
		results <- result{order: 1, emotes: emotes, err: err}
	}()
	go func() {
		emotes, err := c.loadBTTVChannel(ctx, twitchChannelID)
		results <- result{order: 2, emotes: emotes, err: err}
	}()
	go func() {
		emotes, err := c.loadSevenTVChannel(ctx, twitchChannelID)
		results <- result{order: 3, emotes: emotes, err: err}
	}()

	loaded := make([]result, 0, 4)
	var failures []error
	for range 4 {
		item := <-results
		if item.err != nil {
			failures = append(failures, item.err)
			continue
		}
		loaded = append(loaded, item)
	}
	slices.SortFunc(loaded, func(a, b result) int { return a.order - b.order })
	catalogue := make(map[string]thirdPartyEmote)
	for _, source := range loaded {
		for _, item := range source.emotes {
			if item.name != "" && item.id != "" && item.asset != "" {
				catalogue[item.name] = item
			}
		}
	}
	if len(catalogue) == 0 {
		return errors.Join(failures...)
	}
	c.mu.Lock()
	c.byName = catalogue
	c.mu.Unlock()
	return nil
}

type bttvPayloadEmote struct {
	ID, Code string
}

func (c *thirdPartyEmotes) loadBTTVGlobal(ctx context.Context) ([]thirdPartyEmote, error) {
	var payload []bttvPayloadEmote
	if err := c.getJSON(ctx, c.bttvAPI+"/emotes/global", &payload); err != nil {
		return nil, fmt.Errorf("load BetterTTV global emotes: %w", err)
	}
	return bttvEmotes(payload), nil
}

func (c *thirdPartyEmotes) loadBTTVChannel(ctx context.Context, channelID string) ([]thirdPartyEmote, error) {
	var payload struct {
		ChannelEmotes []bttvPayloadEmote `json:"channelEmotes"`
		SharedEmotes  []bttvPayloadEmote `json:"sharedEmotes"`
	}
	if err := c.getJSON(ctx, c.bttvAPI+"/users/twitch/"+url.PathEscape(channelID), &payload); err != nil {
		return nil, fmt.Errorf("load BetterTTV channel emotes: %w", err)
	}
	items := bttvEmotes(payload.SharedEmotes)
	return append(items, bttvEmotes(payload.ChannelEmotes)...), nil
}

func bttvEmotes(payload []bttvPayloadEmote) []thirdPartyEmote {
	items := make([]thirdPartyEmote, 0, len(payload))
	for _, item := range payload {
		items = append(items, thirdPartyEmote{id: "bttv-" + item.ID, name: item.Code, asset: "https://cdn.betterttv.net/emote/" + url.PathEscape(item.ID) + "/3x"})
	}
	return items
}

type sevenTVPayloadEmote struct {
	ID, Name string
	Data     struct {
		ID, Name string
		Animated bool
	} `json:"data"`
}

type sevenTVEmoteSet struct {
	Emotes []sevenTVPayloadEmote `json:"emotes"`
}

func (c *thirdPartyEmotes) loadSevenTVGlobal(ctx context.Context) ([]thirdPartyEmote, error) {
	var payload sevenTVEmoteSet
	if err := c.getJSON(ctx, c.sevenTVAPI+"/emote-sets/global", &payload); err != nil {
		return nil, fmt.Errorf("load 7TV global emotes: %w", err)
	}
	return sevenTVEmotes(payload.Emotes), nil
}

func (c *thirdPartyEmotes) loadSevenTVChannel(ctx context.Context, channelID string) ([]thirdPartyEmote, error) {
	var payload struct {
		EmoteSet sevenTVEmoteSet `json:"emote_set"`
	}
	if err := c.getJSON(ctx, c.sevenTVAPI+"/users/twitch/"+url.PathEscape(channelID), &payload); err != nil {
		return nil, fmt.Errorf("load 7TV channel emotes: %w", err)
	}
	return sevenTVEmotes(payload.EmoteSet.Emotes), nil
}

func sevenTVEmotes(payload []sevenTVPayloadEmote) []thirdPartyEmote {
	items := make([]thirdPartyEmote, 0, len(payload))
	for _, raw := range payload {
		id, name := raw.Data.ID, raw.Name
		if id == "" {
			id = raw.ID
		}
		if name == "" {
			name = raw.Data.Name
		}
		extension := ".png"
		if raw.Data.Animated {
			extension = ".gif"
		}
		items = append(items, thirdPartyEmote{id: "7tv-" + id, name: name, asset: "https://cdn.7tv.app/emote/" + url.PathEscape(id) + "/4x" + extension})
	}
	return items
}

func (c *thirdPartyEmotes) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Streamchat/2.0")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxCatalogueBytes)).Decode(target)
}

// Enrich recognizes exact whitespace-delimited emote names and preserves all
// native Twitch ranges. This matches the providers' token behavior without
// turning ordinary substrings or punctuation into images.
func (c *thirdPartyEmotes) Enrich(message chat.Message) chat.Message {
	if message.Platform != chat.PlatformTwitch || message.Text == "" {
		return message
	}
	c.mu.RLock()
	catalogue := c.byName
	c.mu.RUnlock()
	if len(catalogue) == 0 {
		return message
	}
	runes := []rune(message.Text)
	for start := 0; start < len(runes); {
		for start < len(runes) && unicode.IsSpace(runes[start]) {
			start++
		}
		if start == len(runes) {
			break
		}
		end := start
		for end < len(runes) && !unicode.IsSpace(runes[end]) {
			end++
		}
		item, ok := catalogue[string(runes[start:end])]
		if ok && !overlaps(message.Emotes, start, end-1) {
			message.Emotes = append(message.Emotes, chat.Emote{ID: item.id, Name: item.name, URL: item.asset, Start: start, End: end - 1})
		}
		start = end
	}
	slices.SortStableFunc(message.Emotes, func(a, b chat.Emote) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return a.End - b.End
	})
	return message
}

func overlaps(existing []chat.Emote, start, end int) bool {
	for _, item := range existing {
		if item.Start <= end && item.End >= start {
			return true
		}
	}
	return false
}
