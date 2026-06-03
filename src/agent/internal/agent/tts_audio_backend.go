package agent

import "aiden-agent/internal/agent/tts"

// audioBackend bridges the internal *AudioServiceClient to the
// tts.AudioServiceBackend interface used by the new pluggable TTS module.
type audioBackend struct{ c *AudioServiceClient }

func newAudioBackend(c *AudioServiceClient) *audioBackend { return &audioBackend{c: c} }

var _ tts.AudioServiceBackend = (*audioBackend)(nil)

func (a *audioBackend) StartPlayback(format tts.AudioFormat) (uint64, error) {
	res, err := a.c.StartPlayback(AudioFormat{
		SampleRate: uint32(format.SampleRate),
		Channels:   uint32(format.Channels),
		BitWidth:   uint32(format.BitWidth),
	})
	if err != nil {
		return 0, err
	}
	return res.SessionID, nil
}

func (a *audioBackend) WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error {
	return a.c.WritePlayChunk(sessionID, data, isFinal)
}

func (a *audioBackend) StopPlayback(sessionID uint64) error {
	return a.c.StopPlayback(sessionID)
}

func (a *audioBackend) PlaybackSessionCount() (int, error) {
	h, err := a.c.Health()
	if err != nil {
		return 0, err
	}
	return int(h.PlaybackSessions), nil
}
