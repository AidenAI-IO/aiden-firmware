package agent

import "testing"

func TestMobileGymSessionStoreSetGetClear(t *testing.T) {
	store := &mobileGymSessionStore{}
	store.Set(mobileGymSession{EpisodeID: "ep1", BridgeURL: "http://127.0.0.1:1", BridgeToken: "tok"})
	session, ok := store.Get()
	if !ok || session.EpisodeID != "ep1" || session.BridgeToken != "tok" {
		t.Fatalf("Get() = %#v %v", session, ok)
	}
	store.Clear("ep1")
	if _, ok := store.Get(); ok {
		t.Fatal("session still active after Clear")
	}
}
