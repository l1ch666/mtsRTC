package mtslink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	ExtraSessionID    = "sessionID"
	ExtraEventID      = "eventID"
	ExtraUserID       = "userID"
	ExtraJoinToken    = "joinToken"
	ExtraPublishToken = "publishToken"
	ExtraConferenceID = "conferenceID"
	ExtraRoomURL      = "roomURL"
	ExtraDID          = "did"
	ExtraPID          = "pid"
)

type Client struct {
	HTTP      *http.Client
	Cookie    string
	UA        string
	GuestName string
}

type Bootstrap struct {
	Room         RoomInfo
	UserID       string
	JoinToken    string
	PublishToken string
	ConferenceID string
	DID          string
	PID          string
	Raw          map[string]any
}

func NewClient(cookie string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		HTTP:   &http.Client{Timeout: 20 * time.Second, Jar: jar},
		Cookie: strings.TrimSpace(cookie),
		UA:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/148 Safari/537.36",
	}
}

func (c *Client) req(ctx context.Context, method, urlStr, ctype string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Origin", "https://my.mts-link.ru")
	req.Header.Set("Referer", "https://my.mts-link.ru/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}
	return c.HTTP.Do(req)
}

func (c *Client) jsonGET(ctx context.Context, urlStr string) (map[string]any, int, error) {
	resp, err := c.req(ctx, http.MethodGet, urlStr, "", nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: status=%d body=%s", urlStr, resp.StatusCode, string(body[:min(len(body), 512)]))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		out = map[string]any{"_raw": string(body)}
	}
	return out, resp.StatusCode, nil
}

func (c *Client) Bootstrap(ctx context.Context, roomURL, explicitUserID, explicitJoin, explicitPublish string) (Bootstrap, error) {
	room, err := ParseRoom(roomURL)
	if err != nil {
		return Bootstrap{}, err
	}
	b := Bootstrap{Room: room, UserID: explicitUserID, JoinToken: explicitJoin, PublishToken: explicitPublish}
	b.DID = RandHex(8)
	b.PID = RandHex(8)
	b.Raw = map[string]any{}

	resolved, err := c.ResolveRoom(ctx, roomURL, room)
	if err == nil {
		room = resolved
		b.Room = resolved
	} else if room.SessionID == "" {
		return b, err
	}
	b.Raw["resolvedRoom"] = room

	if cached, _, err := c.jsonGET(ctx, fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/cached", room.SessionID)); err == nil {
		b.Raw["cached"] = cached
		if b.UserID == "" {
			b.UserID = firstMeaningfulString(cached, "userId", "userID", "participantId", "id")
		}
	}

	if conn, _, err := c.postJSON(ctx,
		fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/connections", room.SessionID),
		map[string]any{"name": c.GuestName, "userName": c.GuestName, "platform": "Web", "referrer": roomURL},
	); err == nil {
		b.Raw["connection"] = conn
		if b.UserID == "" {
			b.UserID = firstMeaningfulString(conn, "userId", "userID", "participantId", "participationId", "id")
		}
	} else if conn, _, err2 := c.postJSON(ctx, fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/connections", room.SessionID), map[string]any{}); err2 == nil {
		b.Raw["connection"] = conn
		if b.UserID == "" {
			b.UserID = firstMeaningfulString(conn, "userId", "userID", "participantId", "participationId", "id")
		}
	} else {
		b.Raw["connectionError"] = err.Error()
	}

	if b.UserID == "" {
		b.UserID = "guest-" + RandHex(4)
	}

	jt, _, err := c.jsonGET(ctx, fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/join-token", room.SessionID))
	if err == nil {
		b.Raw["joinTokenResponse"] = jt
		if b.JoinToken == "" {
			b.JoinToken = findFirstKey(jt, "joinToken", "join_token", "token", "value")
		}
		if b.PublishToken == "" {
			b.PublishToken = findFirstKey(jt, "publishToken", "publish_token", "publish")
		}
	} else if b.JoinToken == "" {
		return b, fmt.Errorf("mtslink: guest join-token failed and no join token was provided: %w", err)
	}

	conf, _, err := c.postForm(ctx,
		fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/conferences", room.SessionID),
		url.Values{"hasAudio": {"false"}, "hasVideo": {"false"}, "name": {c.GuestName}, "userName": {c.GuestName}},
	)
	if err == nil {
		b.ConferenceID = firstString(conf, "id", "conferenceId")
		b.Raw["conference"] = conf
		if b.UserID == "" || strings.HasPrefix(b.UserID, "guest-") {
			if v := firstMeaningfulString(conf, "userId", "userID", "participantId", "participationId"); v != "" {
				b.UserID = v
			}
		}
		if b.PublishToken == "" {
			b.PublishToken = findFirstKey(conf, "publishToken", "publish_token", "publish")
		}
	} else {
		b.Raw["conferenceError"] = err.Error()
	}

	if b.JoinToken == "" {
		return b, errors.New("mtslink: join token not found automatically in guest flow")
	}
	if b.PublishToken == "" {
		return b, errors.New("mtslink: publish token not found automatically in guest flow")
	}
	return b, nil
}

func (c *Client) ResolveRoom(ctx context.Context, roomURL string, room RoomInfo) (RoomInfo, error) {
	if room.SessionID != "" {
		return room, nil
	}
	resp, err := c.req(ctx, http.MethodGet, roomURL, "", nil)
	if err == nil {
		_ = resp.Body.Close()
		if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != roomURL {
			if r2, e := ParseRoom(resp.Request.URL.String()); e == nil && r2.SessionID != "" {
				if r2.EventID == "" {
					r2.EventID = room.EventID
				}
				if r2.UserID == "" {
					r2.UserID = room.UserID
				}
				return r2, nil
			}
		}
	}

	candidates := []string{
		fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s/cached", room.EventID),
		fmt.Sprintf("https://gw.mts-link.ru/api/eventsessions/%s", room.EventID),
		fmt.Sprintf("https://gw.mts-link.ru/api/events/%s", room.EventID),
		fmt.Sprintf("https://gw.mts-link.ru/api/users/%s/events/%s", room.UserID, room.EventID),
	}
	for _, u := range candidates {
		js, _, err := c.jsonGET(ctx, u)
		if err != nil {
			continue
		}
		if sid := findFirstKey(js, "eventSessionId", "eventsessionId", "sessionId", "currentSessionId", "activeSessionId", "id"); sid != "" && sid != room.EventID {
			room.SessionID = sid
			return room, nil
		}
	}
	return room, fmt.Errorf("mtslink: cannot resolve event session id from %q; pass a /stream-new/{sessionId} URL", roomURL)
}

func (c *Client) JoinSFU(ctx context.Context, b Bootstrap, offerSDP string, name string) (answer string, peerID string, err error) {
	q := url.Values{}
	q.Set("userId", b.UserID)
	q.Set("joinToken", b.JoinToken)
	q.Set("disableRecording", "false")
	q.Set("userName", name)
	q.Set("publishToken", b.PublishToken)
	q.Set("twcc", "true")
	q.Set("originMid", "0")
	q.Set("did", b.DID)
	q.Set("pid", b.PID)
	u := fmt.Sprintf("https://sfu.mts-link.ru/rtc/room/%s/join?%s", b.Room.SessionID, q.Encode())
	resp, err := c.req(ctx, http.MethodPost, u, "application/sdp", bytes.NewBufferString(offerSDP))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("mtslink: SFU join status=%d body=%s", resp.StatusCode, string(body[:min(len(body), 1024)]))
	}
	answer = string(body)
	peerID = ParsePeerIDFromSDP(answer)
	return answer, peerID, nil
}

func (c *Client) Pin(ctx context.Context, peerID string) error {
	if peerID == "" {
		return nil
	}
	u := fmt.Sprintf("https://sfu.mts-link.ru/rtc/peer/%s/pin", peerID)
	resp, err := c.req(ctx, http.MethodPost, u, "application/json", strings.NewReader(`{"streams":[""]}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mtslink: pin status=%d", resp.StatusCode)
	}
	return nil
}

func (c *Client) AudioVideoControl(ctx context.Context, conferenceID string, hasVideo bool) error {
	if conferenceID == "" {
		return nil
	}
	u := fmt.Sprintf("https://gw.mts-link.ru/api/conferences/%s/audioVideoControl", conferenceID)
	body := fmt.Sprintf(`{"hasAudio":false,"hasVideo":%v,"syncVersion":1}`, hasVideo)
	resp, err := c.req(ctx, http.MethodPost, u, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mtslink: audioVideoControl status=%d", resp.StatusCode)
	}
	return nil
}

func (c *Client) postForm(ctx context.Context, urlStr string, vals url.Values) (map[string]any, int, error) {
	resp, err := c.req(ctx, http.MethodPost, urlStr, "application/x-www-form-urlencoded", strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("POST %s: status=%d body=%s", urlStr, resp.StatusCode, string(body[:min(len(body), 512)]))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		out = map[string]any{"_raw": string(body)}
	}
	return out, resp.StatusCode, nil
}

func (c *Client) postJSON(ctx context.Context, urlStr string, v any) (map[string]any, int, error) {
	buf, _ := json.Marshal(v)
	resp, err := c.req(ctx, http.MethodPost, urlStr, "application/json", bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("POST %s: status=%d body=%s", urlStr, resp.StatusCode, string(body[:min(len(body), 512)]))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		out = map[string]any{"_raw": string(body)}
	}
	return out, resp.StatusCode, nil
}

func ParsePeerIDFromSDP(sdp string) string {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "s=") {
			v := strings.TrimPrefix(line, "s=")
			if v != "-" && len(v) > 8 {
				return v
			}
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64:
				return fmt.Sprintf("%.0f", x)
			}
		}
	}
	return ""
}

func findFirstKey(v any, keys ...string) string {
	want := map[string]bool{}
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			for k, v := range t {
				if want[strings.ToLower(k)] {
					switch vv := v.(type) {
					case string:
						return vv
					case float64:
						return fmt.Sprintf("%.0f", vv)
					}
				}
			}
			for _, v := range t {
				if r := walk(v); r != "" {
					return r
				}
			}
		case []any:
			for _, v := range t {
				if r := walk(v); r != "" {
					return r
				}
			}
		}
		return ""
	}
	return walk(v)
}

func firstMeaningfulString(m map[string]any, keys ...string) string {
	v := findFirstKey(m, keys...)
	if v == "" || v == "0" || strings.EqualFold(v, "null") {
		return ""
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
