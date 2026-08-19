package agent

import "testing"

func TestHIDUDCStateConnected(t *testing.T) {
	for _, state := range []string{"attached", "powered", "default", "addressed", "configured", "suspended"} {
		connected, known := hidUDCStateConnected(state)
		if !known || !connected {
			t.Errorf("state %q = connected:%t known:%t, want true,true", state, connected, known)
		}
	}
	if connected, known := hidUDCStateConnected("not attached"); !known || connected {
		t.Errorf("not attached = connected:%t known:%t, want false,true", connected, known)
	}
	if connected, known := hidUDCStateConnected(""); known || connected {
		t.Errorf("empty state = connected:%t known:%t, want false,false", connected, known)
	}
}
