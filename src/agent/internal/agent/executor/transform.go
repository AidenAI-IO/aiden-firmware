package executor

import "aiden-agent/internal/agent/contextmanager"

type OutboundMessageTransform interface {
	Transform(messages []contextmanager.Message) []contextmanager.Message
}
