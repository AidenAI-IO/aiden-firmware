package agent

import "aiden-agent/internal/agent/statemanager"

func newPhoneBridgeForTest() *PhoneBridge {
	return NewPhoneBridge(newTestLogger(), statemanager.NewStateManager())
}
