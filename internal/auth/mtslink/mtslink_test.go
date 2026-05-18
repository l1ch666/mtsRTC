package mtslink

import "testing"

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
