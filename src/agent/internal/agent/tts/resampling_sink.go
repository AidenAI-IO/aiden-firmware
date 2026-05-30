package tts

import (
	"context"
	"encoding/binary"
	"math"
)

// NewResamplingSink wraps target so providers can keep producing sourceFormat
// while playback receives target.Format(). Only PCM16 mono sample-rate
// conversion is supported; unsupported formats pass through unchanged.
func NewResamplingSink(sourceFormat AudioFormat, target AudioSink) AudioSink {
	targetFormat := target.Format()
	if sourceFormat.SampleRate == targetFormat.SampleRate ||
		sourceFormat.SampleRate <= 0 || targetFormat.SampleRate <= 0 ||
		sourceFormat.Channels != 1 || targetFormat.Channels != 1 ||
		sourceFormat.BitWidth != 16 || targetFormat.BitWidth != 16 {
		return target
	}
	return &resamplingSink{
		sourceFormat: sourceFormat,
		target:       target,
		resampler:    newPCM16MonoLinearResampler(sourceFormat.SampleRate, targetFormat.SampleRate),
	}
}

type resamplingSink struct {
	sourceFormat AudioFormat
	target       AudioSink
	resampler    *pcm16MonoLinearResampler
}

func (s *resamplingSink) Format() AudioFormat { return s.sourceFormat }

func (s *resamplingSink) WritePCM(data []byte) error {
	return s.target.WritePCM(s.resampler.Write(data))
}

func (s *resamplingSink) Drain(ctx context.Context) error { return s.target.Drain(ctx) }

func (s *resamplingSink) Stop() error { return s.target.Stop() }

type pcm16MonoLinearResampler struct {
	srcRate int
	dstRate int

	pendingByte []byte
	lastSample  int16
	haveLast    bool
	srcSeen     int64
	dstEmitted  int64
}

func newPCM16MonoLinearResampler(srcRate, dstRate int) *pcm16MonoLinearResampler {
	return &pcm16MonoLinearResampler{srcRate: srcRate, dstRate: dstRate}
}

func (r *pcm16MonoLinearResampler) Write(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	buf := data
	if len(r.pendingByte) > 0 {
		combined := make([]byte, 0, len(r.pendingByte)+len(data))
		combined = append(combined, r.pendingByte...)
		combined = append(combined, data...)
		buf = combined
		r.pendingByte = nil
	}
	if len(buf)%2 != 0 {
		r.pendingByte = append(r.pendingByte[:0], buf[len(buf)-1])
		buf = buf[:len(buf)-1]
	}
	if len(buf) == 0 {
		return nil
	}

	newSamples := make([]int16, len(buf)/2)
	for i := range newSamples {
		newSamples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}

	baseIndex := r.srcSeen
	samples := newSamples
	if r.haveLast {
		baseIndex--
		withLast := make([]int16, 0, len(newSamples)+1)
		withLast = append(withLast, r.lastSample)
		withLast = append(withLast, newSamples...)
		samples = withLast
	}
	r.srcSeen += int64(len(newSamples))
	r.lastSample = newSamples[len(newSamples)-1]
	r.haveLast = true

	lastGlobalIndex := baseIndex + int64(len(samples)-1)
	out := make([]byte, 0, int(float64(len(newSamples))*float64(r.dstRate)/float64(r.srcRate))*2)
	for {
		pos := float64(r.dstEmitted) * float64(r.srcRate) / float64(r.dstRate)
		idx := int64(math.Floor(pos))
		if idx+1 > lastGlobalIndex {
			break
		}
		local := idx - baseIndex
		if local < 0 || local+1 >= int64(len(samples)) {
			break
		}
		frac := pos - float64(idx)
		a := float64(samples[local])
		b := float64(samples[local+1])
		value := int16(math.Round(a + (b-a)*frac))
		out = binary.LittleEndian.AppendUint16(out, uint16(value))
		r.dstEmitted++
	}
	return out
}
