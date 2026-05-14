package agent

import "math"

// AudioVAD implements voice activity detection using energy-based algorithm
type AudioVAD struct {
	energyThreshold int
	alwaysBuffer    bool
	silenceCount    int
	speechFrames    int
	speaking        bool
	frameSamples    int
	silenceLimit    int
	minSpeechFrames int
	speechBuf       []int16
	utterance       []int16
}

// NewAudioVAD creates a new VAD instance
func NewAudioVAD(sampleRate, energyThreshold, silenceMs, minSpeechMs int, alwaysBuffer bool) *AudioVAD {
	frameMs := 30
	frameSamples := sampleRate * frameMs / 1000
	silenceLimit := silenceMs / frameMs
	minSpeechFrames := minSpeechMs / frameMs

	return &AudioVAD{
		energyThreshold: energyThreshold,
		alwaysBuffer:    alwaysBuffer,
		frameSamples:    frameSamples,
		silenceLimit:    silenceLimit,
		minSpeechFrames: minSpeechFrames,
		speechBuf:       make([]int16, 0, frameSamples*100),
		utterance:       make([]int16, 0, frameSamples*100),
	}
}

// computeEnergy calculates the average energy of audio samples
func computeEnergy(samples []int16) int {
	if len(samples) == 0 {
		return 0
	}
	var sum int64
	for _, s := range samples {
		sum += int64(math.Abs(float64(s)))
	}
	return int(sum / int64(len(samples)))
}

// Process processes an audio frame and returns an utterance if detected
func (v *AudioVAD) Process(samples []int16) []int16 {
	energy := computeEnergy(samples)
	isSpeech := energy > v.energyThreshold

	// In always_buffer mode, collect all frames regardless of speech detection
	if v.alwaysBuffer {
		v.speechBuf = append(v.speechBuf, samples...)
		if isSpeech {
			v.speechFrames++
			v.speaking = true
			v.silenceCount = 0
		} else if v.speaking {
			v.silenceCount++
			if v.silenceCount >= v.silenceLimit {
				if v.speechFrames >= v.minSpeechFrames {
					v.utterance = make([]int16, len(v.speechBuf))
					copy(v.utterance, v.speechBuf)
					v.Reset()
					return v.utterance
				}
				v.Reset()
			}
		}
		return nil
	}

	// Normal VAD mode: only buffer after speech detected
	if isSpeech {
		v.speechBuf = append(v.speechBuf, samples...)
		v.silenceCount = 0
		v.speechFrames++
		v.speaking = true
	} else if v.speaking {
		v.speechBuf = append(v.speechBuf, samples...)
		v.silenceCount++

		if v.silenceCount >= v.silenceLimit {
			if v.speechFrames >= v.minSpeechFrames {
				v.utterance = make([]int16, len(v.speechBuf))
				copy(v.utterance, v.speechBuf)
				v.Reset()
				return v.utterance
			}
			v.Reset()
		}
	}

	return nil
}

// Flush returns any buffered audio as an utterance
func (v *AudioVAD) Flush() []int16 {
	if len(v.speechBuf) == 0 {
		return nil
	}

	v.utterance = make([]int16, len(v.speechBuf))
	copy(v.utterance, v.speechBuf)
	v.Reset()
	return v.utterance
}

// Reset clears the VAD state
func (v *AudioVAD) Reset() {
	v.speechBuf = v.speechBuf[:0]
	v.silenceCount = 0
	v.speechFrames = 0
	v.speaking = false
}
