// benchmark-hid-capture records the production HID provider without a device.
package main

import (
	"aiden-agent/internal/agent/mnk"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type sample struct {
	TimeMS float64 `json:"time_ms"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Down   bool    `json:"down"`
	Report []byte  `json:"report"`
}

type capture struct {
	started time.Time
	samples []sample
}

func (c *capture) Write(b []byte) error {
	if len(b) != 6 {
		return fmt.Errorf("expected touchscreen report, got %d bytes", len(b))
	}
	c.samples = append(c.samples, sample{
		TimeMS: float64(time.Since(c.started)) / float64(time.Millisecond),
		X:      float64(binary.LittleEndian.Uint16(b[2:4])) * 1000 / 32767,
		Y:      float64(binary.LittleEndian.Uint16(b[4:6])) * 1000 / 32767,
		Down:   b[0]&1 != 0,
		Report: append([]byte(nil), b...),
	})
	return nil
}
func (c *capture) Close() {}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var request mnk.MNKRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return err
	}
	if request.Operation != "swipe" || request.Swipe == nil {
		return fmt.Errorf("swipe request required")
	}
	d := &capture{started: time.Now()}
	p := mnk.NewHIDProvider(d, nil, nil, nil, true, "qwerty", nil)
	s := request.Swipe
	err := p.SwipeWithOptions(context.Background(), s.Path, s.Button, mnk.SwipeOptions{
		DurationMs: s.DurationMs, HoldBeforeMs: s.HoldBeforeMs, HoldAfterMs: s.HoldAfterMs, Steps: s.Steps,
	})
	if err != nil {
		return err
	}
	if len(os.Args) != 2 {
		return fmt.Errorf("output path required")
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(d.samples)
}
