package mtslink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
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
	ExtraConnectionID = "connectionID"
	ExtraRoomURL      = "roomURL"
	ExtraDID          = "did"
	ExtraPID          = "pid"
)

const (
	mtsWebBase = "https://my.mts-link.ru"
	mtsAPIBase = "https://gw.mts-link.ru"
	mtsSFUBase = "https://sfu.mts-link.ru"
)

type Client struct {
	HTTP      *http.Client
	Cookie    string
	UA        string
	GuestName string
	DeviceID  string
	UserID    string
}

type Bootstrap struct {
	Room         RoomInfo
	UserID       string
	JoinToken    string
	PublishToken string
	ConferenceID string
	ConnectionID string
	DID          string
	PID          string
	Raw          map[string]any
}

type responseData struct {
	URL    string
	Status int
	Body   string
	JSON   map[string]any
}

func NewClient(cookie string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		HTTP:   &http.Client{Timeout: 25 * time.Second, Jar: jar},
		Cookie: strings.TrimSpace(cookie),
		UA:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36",
	}
}

func (c *Client) Bootstrap(ctx context.Context, roomURL, explicitUserID, explicitJoin, explicitPublish string) (Bootstrap, error) {
	room, err := ParseRoom(roomURL)
	if err != nil {
		return Bootstrap{}, err
	}
	if strings.TrimSpace(c.GuestName) == "" {
		c.GuestName = "olcrtc-" + RandHex(3)
	}

	b := Bootstrap{Room: room, UserID: explicitUserID, JoinToken: explicitJoin, PublishToken: explicitPublish}
	b.DID = RandHex(8)
	b.PID = RandHex(8)
	b.Raw = map[string]any{"guestName": c.GuestName}

	resolved, err := c.ResolveRoom(ctx, roomURL, room)
	if err == nil {
		room = resolved
		b.Room = resolved
	} else if room.SessionID == "" {
		return b, err
	}
	b.Raw["resolvedRoom"] = room
	c.debugf("resolved room: user=%s event=%s session=%s", room.UserID, room.EventID, room.SessionID)

	// Open both the permanent page and the stream-new page. This is important for
	// guest mode because MTS Link sets anonymous visitor cookies from those pages
	// before the Join button triggers API calls.
	c.openPrejoinPages(ctx, roomURL, room, &b)

	// Browser trace from MTS Link shows this exact guest flow:
	//   GET  /api/eventsessions/{session}/cached
	//   POST /api/eventsessions/{session}/guestlogin
	//   GET  /api/login
	//   POST /api/eventsessions/{session}/connections
	//   POST /api/eventsessions/{session}/conferences
	//   GET  /api/eventsessions/{session}/join-token
	//
	// The first prototype skipped guestlogin and also misread the connection
	// token as joinToken. That made the SFU step fail. Keep this order close to
	// the browser sequence and only fall back to generic probes after it.
	c.DeviceID = b.DID
	c.UserID = b.UserID
	c.fetchCachedSession(ctx, room, &b)
	c.guestLogin(ctx, roomURL, room, &b)
	c.fetchCachedSession(ctx, room, &b)
	c.createGuestConnection(ctx, roomURL, room, &b)
	c.fetchConfiguration(ctx, room, &b)
	c.createConference(ctx, room, &b)
	c.fetchJoinTokens(ctx, room, &b)

	// Some deployments return the publish token only after the conference exists.
	if b.JoinToken == "" {
		if b.ConferenceID == "" {
			c.createConference(ctx, room, &b)
		}
		c.fetchJoinTokens(ctx, room, &b)
	}

	if b.UserID == "" {
		// Do not fail here: older MTS guests do not always expose the participant id
		// before the SFU join. The SFU still requires a non-empty value, so use a
		// deterministic-looking anonymous id and let the real error surface at SFU
		// join if this deployment rejects it.
		b.UserID = "guest-" + RandHex(4)
	}
	c.UserID = b.UserID
	if b.JoinToken == "" {
		return b, errors.New("mtslink: join token not found automatically in guest flow; enable MTS_DEBUG=1 and capture the browser Network requests after pressing Join")
	}
	if b.PublishToken == "" {
		// On some MTS Link builds the same token is accepted for both query fields.
		// Prefer trying that over failing before the SFU request, because a 403 from
		// /rtc/room/.../join is more diagnostic than a local bootstrap stop.
		b.PublishToken = b.JoinToken
		b.Raw["publishTokenFallback"] = "joinToken"
	}
	return b, nil
}

func (c *Client) ResolveRoom(ctx context.Context, roomURL string, room RoomInfo) (RoomInfo, error) {
	if room.SessionID != "" {
		return room, nil
	}
	if room.EventID == "" {
		return room, fmt.Errorf("mtslink: cannot resolve event session id without event id from %q", roomURL)
	}

	if res, err := c.getText(ctx, roomURL, roomURL); err == nil {
		if res.URL != "" && res.URL != roomURL {
			if r2, e := ParseRoom(res.URL); e == nil {
				room = mergeRoom(room, r2)
				if room.SessionID != "" {
					return room, nil
				}
			}
		}
		if r2 := ExtractRoomInfo(res.Body); r2.SessionID != "" || r2.EventID != "" {
			room = mergeRoom(room, r2)
			if room.SessionID != "" {
				return room, nil
			}
		}
	}

	candidates := []string{
		fmt.Sprintf("%s/api/eventsessions/%s/cached", mtsAPIBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/eventsessions/%s", mtsAPIBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/events/%s", mtsAPIBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/users/%s/events/%s", mtsAPIBase, url.PathEscape(room.UserID), url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/eventsessions/%s/cached", mtsWebBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/eventsessions/%s", mtsWebBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/events/%s", mtsWebBase, url.PathEscape(room.EventID)),
		fmt.Sprintf("%s/api/users/%s/events/%s", mtsWebBase, url.PathEscape(room.UserID), url.PathEscape(room.EventID)),
	}
	for _, u := range candidates {
		js, _, err := c.jsonGET(ctx, u)
		if err != nil {
			c.debugf("resolve miss %s: %v", u, err)
			continue
		}
		if sid := sessionIDFromAny(js, room.EventID); sid != "" {
			room.SessionID = sid
			return room, nil
		}
	}
	return room, fmt.Errorf("mtslink: cannot resolve event session id from %q; pass a /stream-new/{sessionId} URL", roomURL)
}

func (c *Client) openPrejoinPages(ctx context.Context, originalURL string, room RoomInfo, b *Bootstrap) {
	urls := []string{originalURL}
	if room.SessionID != "" {
		urls = append(urls, appendSessionID(originalURL, room.SessionID))
	}
	for i, u := range uniqueStrings(urls) {
		res, err := c.getText(ctx, u, originalURL)
		key := fmt.Sprintf("prejoinPage%d", i+1)
		if err != nil {
			b.Raw[key+"Error"] = err.Error()
			c.debugf("prejoin page %s failed: %v", u, err)
			continue
		}
		b.Raw[key] = map[string]any{"url": res.URL, "status": res.Status, "bodyBytes": len(res.Body)}
		if r2 := ExtractRoomInfo(res.Body); r2.SessionID != "" || r2.EventID != "" {
			b.Room = mergeRoom(b.Room, r2)
		}
		c.absorb(key, res.JSON, b)
	}
}

func (c *Client) fetchCachedSession(ctx context.Context, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	for i, u := range apiURLs(fmt.Sprintf("/api/eventsessions/%s/cached", url.PathEscape(room.SessionID))) {
		js, _, err := c.jsonGET(ctx, u)
		if err != nil {
			b.Raw[fmt.Sprintf("cachedError%d", i+1)] = err.Error()
			continue
		}
		c.absorb(fmt.Sprintf("cached%d", i+1), js, b)
	}
}

func (c *Client) guestLogin(ctx context.Context, roomURL string, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	referer := appendSessionID(roomURL, room.SessionID)
	form := url.Values{
		"nickname":   {c.GuestName},
		"name":       {""},
		"secondName": {""},
		"phone":      {""},
	}
	for i, endpoint := range apiURLs(fmt.Sprintf("/api/eventsessions/%s/guestlogin", url.PathEscape(room.SessionID))) {
		js, _, err := c.postFormWithReferer(ctx, endpoint, form, referer)
		if err != nil {
			b.Raw[fmt.Sprintf("guestLoginError%d", i+1)] = err.Error()
			c.debugf("guestlogin failed %s: %v", endpoint, err)
			continue
		}
		c.absorb(fmt.Sprintf("guestLogin%d", i+1), js, b)
		break
	}

	// Browser asks /api/login after guestlogin and receives the real guest user id.
	for i, endpoint := range apiURLs("/api/login") {
		js, _, err := c.jsonGETWithReferer(ctx, endpoint, referer)
		if err != nil {
			b.Raw[fmt.Sprintf("loginError%d", i+1)] = err.Error()
			continue
		}
		c.absorb(fmt.Sprintf("login%d", i+1), js, b)
		if b.UserID != "" {
			break
		}
	}
}

func (c *Client) fetchConfiguration(ctx context.Context, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	for i, endpoint := range apiURLs(fmt.Sprintf("/api/eventsession/%s/configuration", url.PathEscape(room.SessionID))) {
		js, _, err := c.jsonGET(ctx, endpoint)
		if err != nil {
			b.Raw[fmt.Sprintf("configurationError%d", i+1)] = err.Error()
			continue
		}
		c.absorb(fmt.Sprintf("configuration%d", i+1), js, b)
		break
	}
}

func (c *Client) createGuestConnection(ctx context.Context, roomURL string, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	referer := appendSessionID(roomURL, room.SessionID)

	// Browser trace: POST form to /connections, usually with an empty body.
	// The response id is participation/connection id; nested user.id is the SFU userId.
	formPayloads := []url.Values{
		{},
		{"nickname": {c.GuestName}},
	}
	attempt := 0
	for _, endpoint := range apiURLs(fmt.Sprintf("/api/eventsessions/%s/connections", url.PathEscape(room.SessionID))) {
		for _, form := range formPayloads {
			attempt++
			js, _, err := c.postFormWithReferer(ctx, endpoint, form, referer)
			if err != nil {
				b.Raw[fmt.Sprintf("connectionFormError%d", attempt)] = err.Error()
				continue
			}
			c.absorb(fmt.Sprintf("connectionForm%d", attempt), js, b)
			if b.UserID != "" && b.ConnectionID != "" {
				return
			}
		}
	}

	// Fallbacks for possible API variants.
	payload := map[string]any{
		"nickname":    c.GuestName,
		"name":        "",
		"secondName":  "",
		"phone":       "",
		"hasAudio":    false,
		"hasVideo":    true,
		"displayName": c.GuestName,
		"isGuest":     true,
	}
	paths := []string{
		fmt.Sprintf("/api/v1/eventsessions/%s/connections", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/participants", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/guest", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/join", url.PathEscape(room.SessionID)),
	}
	for _, endpoint := range apiURLs(paths...) {
		attempt++
		js, _, err := c.postJSONWithReferer(ctx, endpoint, payload, referer)
		if err != nil {
			b.Raw[fmt.Sprintf("connectionJSONError%d", attempt)] = err.Error()
			continue
		}
		c.absorb(fmt.Sprintf("connectionJSON%d", attempt), js, b)
		if b.UserID != "" && b.ConnectionID != "" {
			return
		}
	}
}

func (c *Client) createConference(ctx context.Context, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	referer := appendSessionID(b.Room.RawURL, room.SessionID)
	if referer == "" {
		referer = mtsWebBase + "/"
	}

	// For olcRTC we must publish a camera-like video stream. A normal browser
	// without a camera sends hasVideo=false, but then MTS creates an audio-only
	// conference and the SFU answer rejects our outgoing video m-line. Prefer
	// hasVideo=true and only keep the old audio-only shape as a last fallback.
	forceVideo := strings.TrimSpace(os.Getenv("MTS_FORCE_VIDEO"))
	if forceVideo == "" {
		forceVideo = "1"
	}
	formPayloads := []url.Values{}
	if forceVideo != "0" && !strings.EqualFold(forceVideo, "false") {
		formPayloads = append(formPayloads,
			// Browser with an active camera/microphone publishes audio+video. Use that
			// first because our initial SDP now contains one audio publisher.
			url.Values{"hasAudio": {"true"}, "hasVideo": {"true"}},
			url.Values{"hasAudio": {"false"}, "hasVideo": {"true"}},
		)
	}
	formPayloads = append(formPayloads,
		url.Values{"hasAudio": {"true"}, "hasVideo": {"false"}},
		url.Values{"hasAudio": {"false"}, "hasVideo": {"false"}},
	)
	attempt := 0
	for _, endpoint := range apiURLs(fmt.Sprintf("/api/eventsessions/%s/conferences", url.PathEscape(room.SessionID))) {
		for _, form := range formPayloads {
			attempt++
			js, _, err := c.postFormWithReferer(ctx, endpoint, form, referer)
			if err != nil {
				b.Raw[fmt.Sprintf("conferenceFormError%d", attempt)] = err.Error()
				continue
			}
			c.absorb(fmt.Sprintf("conferenceForm%d", attempt), js, b)
			if b.ConferenceID != "" {
				return
			}
		}
	}

	payload := map[string]any{
		"eventSessionId": room.SessionID,
		"sessionId":      room.SessionID,
		"nickname":       c.GuestName,
		"name":           c.GuestName,
		"userName":       c.GuestName,
		"displayName":    c.GuestName,
		"hasAudio":       false,
		"hasVideo":       true,
		"connectionId":   b.ConnectionID,
		"userId":         b.UserID,
		"platform":       "Web",
	}
	paths := []string{
		fmt.Sprintf("/api/v1/eventsessions/%s/conferences", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/conference", url.PathEscape(room.SessionID)),
		"/api/conferences",
		"/api/v1/conferences",
	}
	for _, endpoint := range apiURLs(paths...) {
		attempt++
		js, _, err := c.postJSONWithReferer(ctx, endpoint, payload, referer)
		if err == nil {
			c.absorb(fmt.Sprintf("conferenceJSON%d", attempt), js, b)
			if b.ConferenceID != "" {
				return
			}
		} else {
			b.Raw[fmt.Sprintf("conferenceJSONError%d", attempt)] = err.Error()
		}
	}
}

func (c *Client) fetchJoinTokens(ctx context.Context, room RoomInfo, b *Bootstrap) {
	if room.SessionID == "" {
		return
	}
	referer := appendSessionID(b.Room.RawURL, room.SessionID)
	if referer == "" {
		referer = mtsWebBase + "/"
	}

	// Browser trace: plain GET /join-token with only x-device-id/cookies.
	basePaths := []string{
		fmt.Sprintf("/api/eventsessions/%s/join-token", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/v1/eventsessions/%s/join-token", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/tokens", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/webrtc-token", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/sfu/token", url.PathEscape(room.SessionID)),
		fmt.Sprintf("/api/eventsessions/%s/sfu/tokens", url.PathEscape(room.SessionID)),
	}
	attempt := 0
	for _, path := range basePaths {
		for _, endpoint := range apiURLs(path) {
			attempt++
			js, _, err := c.jsonGETWithReferer(ctx, endpoint, referer)
			if err == nil {
				c.absorb(fmt.Sprintf("joinTokenGET%d", attempt), js, b)
				if b.JoinToken != "" {
					return
				}
			} else {
				b.Raw[fmt.Sprintf("joinTokenGETError%d", attempt)] = err.Error()
			}
		}
	}

	if b.ConferenceID == "" && b.UserID == "" {
		return
	}

	// Fallback: POST for variants that bind token to conference/user.
	payload := map[string]any{
		"eventSessionId": room.SessionID,
		"sessionId":      room.SessionID,
		"userId":         b.UserID,
		"conferenceId":   b.ConferenceID,
		"userName":       c.GuestName,
		"name":           c.GuestName,
		"hasAudio":       false,
		"hasVideo":       true,
	}
	for _, path := range basePaths {
		for _, endpoint := range apiURLs(path) {
			attempt++
			js, _, err := c.postJSONWithReferer(ctx, endpoint, payload, referer)
			if err == nil {
				c.absorb(fmt.Sprintf("joinTokenPOST%d", attempt), js, b)
				if b.JoinToken != "" {
					return
				}
			} else {
				b.Raw[fmt.Sprintf("joinTokenPOSTError%d", attempt)] = err.Error()
			}
		}
	}
}

func (c *Client) JoinSFU(ctx context.Context, b Bootstrap, offerSDP string, name string) (answer string, peerID string, err error) {
	q := url.Values{}
	q.Set("userId", b.UserID)
	q.Set("joinToken", b.JoinToken)
	q.Set("disableRecording", "true")
	q.Set("userName", name)
	q.Set("publishToken", b.PublishToken)
	q.Set("twcc", "true")
	q.Set("originMid", "1")
	q.Set("did", b.DID)
	q.Set("pid", b.PID)
	// Browser does not pass conferenceId to /rtc/room/{session}/join.
	u := fmt.Sprintf("%s/rtc/room/%s/join?%s", mtsSFUBase, url.PathEscape(b.Room.SessionID), q.Encode())
	resp, err := c.req(ctx, http.MethodPost, u, "application/sdp", bytes.NewBufferString(offerSDP), appendSessionID(b.Room.RawURL, b.Room.SessionID))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("mtslink: SFU join status=%d body=%s", resp.StatusCode, string(body[:min(len(body), 2048)]))
	}
	answer = string(body)
	peerID = ParsePeerIDFromSDP(answer)
	return answer, peerID, nil
}

func (c *Client) UpdatePeer(ctx context.Context, b Bootstrap, peerID string, offerSDP string) (answer string, err error) {
	if peerID == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("targetLanguage", "null")
	if b.PublishToken != "" {
		q.Set("publishToken", b.PublishToken)
	}
	u := fmt.Sprintf("%s/rtc/peer/%s/update?%s", mtsSFUBase, url.PathEscape(peerID), q.Encode())
	resp, err := c.req(ctx, http.MethodPost, u, "application/sdp", bytes.NewBufferString(offerSDP), appendSessionID(b.Room.RawURL, b.Room.SessionID))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("mtslink: peer update status=%d body=%s", resp.StatusCode, string(body[:min(len(body), 2048)]))
	}
	return string(body), nil
}

func (c *Client) Pin(ctx context.Context, peerID string) error {
	if peerID == "" {
		return nil
	}
	u := fmt.Sprintf("%s/rtc/peer/%s/pin", mtsSFUBase, url.PathEscape(peerID))
	resp, err := c.req(ctx, http.MethodPost, u, "application/json", strings.NewReader(`{"streams":[""]}`), mtsWebBase+"/")
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
	for _, u := range apiURLs(fmt.Sprintf("/api/conferences/%s/audioVideoControl", url.PathEscape(conferenceID))) {
		body := fmt.Sprintf(`{"hasAudio":false,"hasVideo":%v,"syncVersion":1}`, hasVideo)
		resp, err := c.req(ctx, http.MethodPost, u, "application/json", strings.NewReader(body), mtsWebBase+"/")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}
	}
	return nil
}

func (c *Client) jsonGET(ctx context.Context, urlStr string) (map[string]any, int, error) {
	res, err := c.requestData(ctx, http.MethodGet, urlStr, "", nil, mtsWebBase+"/")
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) postForm(ctx context.Context, urlStr string, vals url.Values) (map[string]any, int, error) {
	res, err := c.requestData(ctx, http.MethodPost, urlStr, "application/x-www-form-urlencoded", strings.NewReader(vals.Encode()), mtsWebBase+"/")
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) postJSON(ctx context.Context, urlStr string, v any) (map[string]any, int, error) {
	buf, _ := json.Marshal(v)
	res, err := c.requestData(ctx, http.MethodPost, urlStr, "application/json", bytes.NewReader(buf), mtsWebBase+"/")
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) jsonGETWithReferer(ctx context.Context, urlStr, referer string) (map[string]any, int, error) {
	res, err := c.requestData(ctx, http.MethodGet, urlStr, "", nil, referer)
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) postFormWithReferer(ctx context.Context, urlStr string, vals url.Values, referer string) (map[string]any, int, error) {
	res, err := c.requestData(ctx, http.MethodPost, urlStr, "application/x-www-form-urlencoded; charset=utf-8", strings.NewReader(vals.Encode()), referer)
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) postJSONWithReferer(ctx context.Context, urlStr string, v any, referer string) (map[string]any, int, error) {
	buf, _ := json.Marshal(v)
	res, err := c.requestData(ctx, http.MethodPost, urlStr, "application/json", bytes.NewReader(buf), referer)
	if err != nil {
		return nil, 0, err
	}
	return res.JSON, res.Status, nil
}

func (c *Client) getText(ctx context.Context, urlStr, referer string) (responseData, error) {
	return c.requestData(ctx, http.MethodGet, urlStr, "", nil, referer)
}

func (c *Client) requestData(ctx context.Context, method, urlStr, ctype string, body io.Reader, referer string) (responseData, error) {
	resp, err := c.req(ctx, method, urlStr, ctype, body, referer)
	if err != nil {
		return responseData{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	data := responseData{URL: urlStr, Status: resp.StatusCode, Body: string(raw)}
	if resp.Request != nil && resp.Request.URL != nil {
		data.URL = resp.Request.URL.String()
	}
	if len(raw) > 0 {
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err == nil {
			data.JSON = out
		} else {
			data.JSON = map[string]any{"_raw": data.Body}
		}
	} else {
		data.JSON = map[string]any{}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("%s %s: status=%d body=%s", method, urlStr, resp.StatusCode, string(raw[:min(len(raw), 1024)]))
	}
	return data, nil
}

func (c *Client) req(ctx context.Context, method, urlStr, ctype string, body io.Reader, referer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if referer == "" {
		referer = mtsWebBase + "/"
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("Origin", mtsWebBase)
	if c.DeviceID != "" {
		req.Header.Set("x-device-id", c.DeviceID)
	}
	if c.UserID != "" && !strings.HasPrefix(c.UserID, "guest-") {
		req.Header.Set("x-user-id", c.UserID)
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if token := c.csrfToken(); token != "" {
		req.Header.Set("X-CSRF-Token", token)
		req.Header.Set("X-XSRF-Token", token)
	}
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}
	return c.HTTP.Do(req)
}

func (c *Client) csrfToken() string {
	if c.HTTP == nil || c.HTTP.Jar == nil {
		return ""
	}
	for _, host := range []string{mtsWebBase, mtsAPIBase} {
		u, err := url.Parse(host)
		if err != nil {
			continue
		}
		for _, ck := range c.HTTP.Jar.Cookies(u) {
			name := strings.ToLower(ck.Name)
			if strings.Contains(name, "csrf") || strings.Contains(name, "xsrf") {
				v, _ := url.QueryUnescape(ck.Value)
				return v
			}
		}
	}
	return ""
}

func (c *Client) absorb(label string, js map[string]any, b *Bootstrap) {
	if js == nil {
		return
	}
	b.Raw[label] = js
	if sid := sessionIDFromAny(js, b.Room.EventID); sid != "" && b.Room.SessionID == "" {
		b.Room.SessionID = sid
	}

	lowerLabel := strings.ToLower(label)
	isConnection := strings.Contains(lowerLabel, "connection")
	isGuest := strings.Contains(lowerLabel, "guest")
	isLogin := strings.Contains(lowerLabel, "login")
	isConference := strings.Contains(lowerLabel, "conference")
	isJoinToken := strings.Contains(lowerLabel, "jointoken")

	if b.UserID == "" || strings.HasPrefix(b.UserID, "guest-") {
		if v := findFirstKey(js, "userId", "userID", "memberId", "guestId"); meaningful(v) {
			b.UserID = v
		} else if v := nestedUserID(js); meaningful(v) {
			b.UserID = v
		} else if isLogin && !isConnection {
			if v := topLevelID(js); meaningful(v) {
				b.UserID = v
			}
		}
	}

	if b.ConnectionID == "" && isConnection {
		if v := findFirstKey(js, "connectionId", "connectionID", "connection_id", "participationId", "id"); meaningful(v) {
			b.ConnectionID = v
		}
	}

	if isConference && b.ConferenceID == "" {
		if v := findFirstKey(js, "conferenceId", "conferenceID", "conference_id", "id"); meaningful(v) {
			b.ConferenceID = v
		}
	}

	// MTS conference response uses privateKey as SFU publishToken.
	if isConference && b.PublishToken == "" {
		if v := findFirstKey(js, "privateKey", "publishToken", "publish_token", "publish", "publisherToken", "streamToken"); meaningful(v) {
			b.PublishToken = v
		}
	}

	// Do not treat connection.token or guestlogin token as SFU joinToken.
	if isJoinToken {
		if v := findFirstKey(js, "joinToken", "join_token", "roomToken", "sfuToken", "value", "token"); meaningful(v) {
			b.JoinToken = v
		}
	} else if b.JoinToken == "" && !isConnection && !isGuest && !isLogin {
		if v := findFirstKey(js, "joinToken", "join_token", "roomToken", "sfuToken"); meaningful(v) {
			b.JoinToken = v
		}
	}

	if b.PublishToken == "" && !isConnection && !isGuest && !isLogin {
		if v := findFirstKey(js, "publishToken", "publish_token", "publish", "publisherToken", "streamToken"); meaningful(v) {
			b.PublishToken = v
		}
	}
	if meaningful(b.UserID) && !strings.HasPrefix(b.UserID, "guest-") {
		c.UserID = b.UserID
	}
}

func apiURLs(paths ...string) []string {
	out := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			out = append(out, p)
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		out = append(out, mtsAPIBase+p, mtsWebBase+p)
	}
	return uniqueStrings(out)
}

func ExtractRoomInfo(text string) RoomInfo {
	cleaned := html.UnescapeString(text)
	cleaned = strings.ReplaceAll(cleaned, `\/`, "/")
	cleaned = strings.ReplaceAll(cleaned, `\u002F`, "/")
	cleaned = strings.ReplaceAll(cleaned, `%2F`, "/")
	cleaned = strings.ReplaceAll(cleaned, `%2f`, "/")
	patterns := []string{
		`/j/([0-9]+)/([0-9]+)/(?:stream-new|session)/([0-9]+)`,
		`"eventSessionId"\s*:\s*"?([0-9]+)"?`,
		`"eventsessionId"\s*:\s*"?([0-9]+)"?`,
		`"currentSessionId"\s*:\s*"?([0-9]+)"?`,
		`"activeSessionId"\s*:\s*"?([0-9]+)"?`,
		`"sessionId"\s*:\s*"?([0-9]+)"?`,
		`/(?:stream-new|session)/([0-9]+)`,
	}
	var out RoomInfo
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		m := re.FindStringSubmatch(cleaned)
		if len(m) == 0 {
			continue
		}
		if len(m) == 4 {
			out.UserID, out.EventID, out.SessionID = m[1], m[2], m[3]
			return out
		}
		if out.SessionID == "" {
			out.SessionID = m[1]
		}
	}
	return out
}

func sessionIDFromAny(v any, eventID string) string {
	for _, k := range []string{"eventSessionId", "eventsessionId", "sessionId", "currentSessionId", "activeSessionId", "streamSessionId", "id"} {
		if sid := findFirstKey(v, k); meaningful(sid) && sid != eventID {
			return sid
		}
	}
	if m, ok := v.(map[string]any); ok {
		if raw, ok := m["_raw"].(string); ok {
			if room := ExtractRoomInfo(raw); room.SessionID != "" {
				return room.SessionID
			}
		}
	}
	return ""
}

func mergeRoom(base, next RoomInfo) RoomInfo {
	if next.RawURL != "" {
		base.RawURL = next.RawURL
	}
	if next.UserID != "" {
		base.UserID = next.UserID
	}
	if next.EventID != "" {
		base.EventID = next.EventID
	}
	if next.SessionID != "" {
		base.SessionID = next.SessionID
	}
	return base
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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

func topLevelID(m map[string]any) string {
	if m == nil {
		return ""
	}
	switch v := m["id"].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func nestedUserID(m map[string]any) string {
	if m == nil {
		return ""
	}
	raw, ok := m["user"]
	if !ok {
		return ""
	}
	user, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	return topLevelID(user)
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
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			for _, want := range keys {
				for k, v := range t {
					if strings.EqualFold(k, want) {
						if s := stringifyJSONScalar(v); s != "" {
							return s
						}
					}
				}
			}
			mapKeys := make([]string, 0, len(t))
			for k := range t {
				mapKeys = append(mapKeys, k)
			}
			sort.Strings(mapKeys)
			for _, k := range mapKeys {
				if r := walk(t[k]); r != "" {
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

func stringifyJSONScalar(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case float64:
		return fmt.Sprintf("%.0f", vv)
	case json.Number:
		return vv.String()
	default:
		return ""
	}
}

func firstMeaningfulString(m map[string]any, keys ...string) string {
	v := findFirstKey(m, keys...)
	if !meaningful(v) {
		return ""
	}
	return v
}

func meaningful(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "0" && !strings.EqualFold(v, "null") && !strings.EqualFold(v, "undefined")
}

func (c *Client) debugf(format string, args ...any) {
	if os.Getenv("MTS_DEBUG") == "" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[mtslink] "+format+"\n", args...)
}
