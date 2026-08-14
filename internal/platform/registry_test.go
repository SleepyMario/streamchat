package platform

import (
	"github.com/SleepyMario/streamchat/internal/config"
	"testing"
)

func TestPlatformSelectionUsesRegistryOrderAndTargets(t *testing.T) {
	c := config.Defaults()
	c.YouTube.APIKey = "key"
	c.YouTube.VideoID = "video"
	c.Twitch.ClientID = "client"
	c.Twitch.AccessToken = "token"
	c.Twitch.Channel = "channel"
	c.Twitch.UserID = "user"
	a, err := Default().Select(&c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || a[0].Name() != "youtube" || a[1].Name() != "twitch" {
		t.Fatalf("selection: %#v", a)
	}
	if _, err = Default().Select(&c, map[string]bool{"kick": true}); err == nil {
		t.Fatal("missing Kick config accepted")
	}
}
