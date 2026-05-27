package mtslink

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseRoomFullStreamURL(t *testing.T) {
	room, err := ParseRoom("https://my.mts-link.ru/j/167846474/19645959806/stream-new/18867526566")
	if err != nil {
		t.Fatal(err)
	}
	if room.UserID != "167846474" || room.EventID != "19645959806" || room.SessionID != "18867526566" {
		t.Fatalf("room = %#v", room)
	}
}

func TestParseRoomPermanentURLLeavesSessionForResolve(t *testing.T) {
	room, err := ParseRoom("https://my.mts-link.ru/j/167846474/19645959806")
	if err != nil {
		t.Fatal(err)
	}
	if room.UserID != "167846474" || room.EventID != "19645959806" {
		t.Fatalf("room = %#v", room)
	}
	if room.SessionID != "" {
		t.Fatalf("short permanent URL must resolve session lazily, got %#v", room)
	}
}

func TestExtractRoomInfoFromPrejoinHTML(t *testing.T) {
	room := ExtractRoomInfo(`<html><script>window.__STATE__={"eventSessionId":"18867526566"};</script></html>`)
	if room.SessionID != "18867526566" {
		t.Fatalf("session id = %q", room.SessionID)
	}
}

func TestExtractRoomInfoFromEscapedURL(t *testing.T) {
	room := ExtractRoomInfo(`https:\/\/my.mts-link.ru\/j\/167846474\/19645959806\/stream-new\/18867526566`)
	if room.UserID != "167846474" || room.EventID != "19645959806" || room.SessionID != "18867526566" {
		t.Fatalf("room = %#v", room)
	}
}

func TestFindFirstKeyPrefersCallerOrder(t *testing.T) {
	got := findFirstKey(map[string]any{
		"memberId": "member",
		"userId":   "user",
		"nested": map[string]any{
			"userId": "nested-user",
		},
	}, "userId", "memberId")
	if got != "user" {
		t.Fatalf("findFirstKey() = %q, want user", got)
	}

	got = findFirstKey(map[string]any{
		"z": map[string]any{"memberId": "member"},
		"a": map[string]any{"userId": "user"},
	}, "userId", "memberId")
	if got != "user" {
		t.Fatalf("findFirstKey(nested) = %q, want user", got)
	}
}

func TestBootstrapDoesNotCreateDuplicateConferenceWhenPublishTokenMissing(t *testing.T) {
	var conferencePosts int
	client := NewClient("")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		body := `{}`
		switch {
		case strings.Contains(path, "/guestlogin"):
			body = `{"userId":"u1"}`
		case strings.HasSuffix(path, "/api/login"):
			body = `{"id":"u1"}`
		case strings.Contains(path, "/connections"):
			body = `{"id":"conn1","user":{"id":"u1"}}`
		case strings.Contains(path, "/conferences") || strings.Contains(path, "/conference"):
			conferencePosts++
			body = `{"id":"conf1"}`
		case strings.Contains(path, "/join-token"):
			body = `{"joinToken":"join1"}`
		}
		return jsonResponse(req, body), nil
	})}

	roomURL := "https://my.mts-link.ru/j/167846474/19645959806/stream-new/18867526566"
	bootstrap, err := client.Bootstrap(t.Context(), roomURL, "", "", "")
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if bootstrap.ConferenceID != "conf1" || bootstrap.JoinToken != "join1" {
		t.Fatalf("bootstrap = %#v", bootstrap)
	}
	if conferencePosts != 1 {
		t.Fatalf("conference posts = %d, want 1", conferencePosts)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
