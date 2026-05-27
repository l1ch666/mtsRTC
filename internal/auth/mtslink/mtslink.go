package mtslink

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openlibrecommunity/olcrtc/internal/auth"
)

const defaultServiceURL = "https://my.mts-link.ru"

type Provider struct{}

func (Provider) Engine() string { return "mtslink" }

func (Provider) DefaultServiceURL() string { return defaultServiceURL }

func (Provider) Issue(ctx context.Context, cfg auth.Config) (auth.Credentials, error) {
	roomURL := strings.TrimSpace(cfg.RoomURL)
	if roomURL == "" {
		return auth.Credentials{}, auth.ErrRoomIDRequired
	}
	if !strings.Contains(roomURL, "://") {
		roomURL = "https://" + strings.TrimPrefix(roomURL, "//")
	}

	client := NewClient(os.Getenv("MTS_COOKIE"))
	client.GuestName = cfg.Name

	bootstrap, err := client.Bootstrap(ctx, roomURL, "", "", "")
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("mtslink bootstrap: %w", err)
	}
	return auth.Credentials{
		URL:   defaultServiceURL,
		Token: bootstrap.JoinToken,
		Extra: map[string]string{
			ExtraSessionID:    bootstrap.Room.SessionID,
			ExtraEventID:      bootstrap.Room.EventID,
			ExtraUserID:       bootstrap.UserID,
			ExtraJoinToken:    bootstrap.JoinToken,
			ExtraPublishToken: bootstrap.PublishToken,
			ExtraConferenceID: bootstrap.ConferenceID,
			ExtraConnectionID: bootstrap.ConnectionID,
			ExtraRoomURL:      appendSessionID(roomURL, bootstrap.Room.SessionID),
			ExtraDID:          bootstrap.DID,
			ExtraPID:          bootstrap.PID,
		},
	}, nil
}

func init() { //nolint:gochecknoinits // auth registration is the canonical Go pattern for plugins
	auth.Register("mtslink", Provider{})
}
