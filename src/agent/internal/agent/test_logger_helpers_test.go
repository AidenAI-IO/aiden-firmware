package agent

import (
	"io"
	"log"

	"aiden-agent/internal/agent/statemanager"
)

func newTestLogger() *Logger {
	return &Logger{logger: log.New(io.Discard, "", 0)}
}

func newServerForTest(runtime *Runtime) *Server {
	if runtime.logger == nil {
		runtime.logger = newTestLogger()
	}
	if runtime.stateManager == nil {
		runtime.stateManager = statemanager.NewStateManager()
	}
	return NewServer(runtime, ":0")
}
