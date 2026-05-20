package mtslink

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type RoomInfo struct {
	RawURL    string
	SessionID string
	EventID   string
	UserID    string
}

func ParseRoom(raw string) (RoomInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RoomInfo{}, authErrRoomRequired()
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + strings.TrimPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RoomInfo{}, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	info := RoomInfo{RawURL: raw}

	for i := 0; i < len(parts); i++ {
		if parts[i] == "j" && i+2 < len(parts) {
			info.UserID = parts[i+1]
			info.EventID = parts[i+2]
		}
		if (parts[i] == "stream-new" || parts[i] == "session") && i+1 < len(parts) {
			info.SessionID = parts[i+1]
		}
	}

	if info.SessionID == "" && info.EventID == "" {
		re := regexp.MustCompile(`^[0-9]{6,}$`)
		for i := len(parts) - 1; i >= 0; i-- {
			if re.MatchString(parts[i]) {
				info.SessionID = parts[i]
				break
			}
		}
	}
	if info.SessionID == "" && info.EventID == "" {
		return info, fmt.Errorf("mtslink: cannot detect event/session id from %q", raw)
	}
	return info, nil
}

func appendSessionID(roomURL, sessionID string) string {
	if sessionID == "" || strings.Contains(roomURL, "/stream-new/") || strings.Contains(roomURL, "/session/") {
		return roomURL
	}
	return strings.TrimRight(roomURL, "/") + "/stream-new/" + sessionID
}

func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func authErrRoomRequired() error {
	return fmt.Errorf("mtslink: room URL required")
}
