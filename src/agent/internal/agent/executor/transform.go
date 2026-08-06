package executor

import "aiden-agent/internal/agent/messages"

type OutboundMessageTransform interface {
	Transform(messages []messages.Message) []messages.Message
}
