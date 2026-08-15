package main

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
)

type externalURLLauncher interface {
	Open(string) error
}

type openController struct {
	config   config.Config
	kick     *kickOutboundSender
	launcher externalURLLauncher
}

func (c openController) Open(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 1 {
		return "Usage: /open PLATFORM (kick, youtube, twitch)", nil
	}
	platform := strings.ToLower(fields[0])
	publicURL, err := c.platformURL(ctx, platform)
	if err != nil {
		return "", err
	}
	if c.launcher == nil {
		return "", errors.New("external stream launcher is unavailable")
	}
	if err = c.launcher.Open(publicURL); err != nil {
		return "", err
	}
	return "Opened " + displayPlatform(platform) + " externally.", nil
}

func (c openController) platformURL(ctx context.Context, platform string) (string, error) {
	switch platform {
	case "kick":
		return c.kickURL(ctx)
	case "youtube":
		if strings.TrimSpace(c.config.YouTube.VideoID) == "" {
			return "", errors.New("YouTube stream is not currently available/configured")
		}
		videoID, err := youtube.ParseVideoID(c.config.YouTube.VideoID)
		if err != nil {
			return "", errors.New("YouTube stream is not currently available/configured")
		}
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
	case "twitch":
		if strings.TrimSpace(c.config.Twitch.Channel) == "" {
			return "", errors.New("Twitch channel is not currently available/configured")
		}
		channel, err := twitch.ParseChannel(c.config.Twitch.Channel)
		if err != nil {
			return "", errors.New("Twitch channel is not currently available/configured")
		}
		return "https://www.twitch.tv/" + url.PathEscape(channel), nil
	default:
		return "", errors.New("unsupported platform; use kick, youtube, or twitch")
	}
}

func (c openController) kickURL(ctx context.Context) (string, error) {
	if c.kick == nil || c.config.Kick.BroadcasterID == "" || (c.config.Kick.AccessToken == "" && c.config.Kick.RefreshToken == "") {
		return "", errors.New("Kick channel is not currently available/configured; run: streamchat setup kick")
	}
	var status kick.ChannelStatus
	err := c.kick.withToken(ctx, func(accessToken string) error {
		client := kick.ChannelClient{HTTP: c.kick.http, BaseURL: c.kick.config.APIBaseURL, AccessToken: accessToken}
		var err error
		status, err = client.GetStatus(ctx)
		return err
	})
	if err != nil {
		return "", err
	}
	slug := strings.TrimSpace(status.Slug)
	if slug == "" || strings.ContainsAny(slug, "/?#") {
		return "", errors.New("Kick channel URL is not currently available")
	}
	return "https://kick.com/" + url.PathEscape(slug), nil
}

func displayPlatform(platform string) string {
	switch platform {
	case "kick":
		return "Kick"
	case "youtube":
		return "YouTube"
	case "twitch":
		return "Twitch"
	default:
		return platform
	}
}
