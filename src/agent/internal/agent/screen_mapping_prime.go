package agent

import (
	"context"
	"fmt"
	"time"
)

const (
	startupScreenMappingPrimeTimeout       = 20 * time.Second
	startupScreenMappingPrimeRetryInterval = 300 * time.Millisecond
)

// PrimeScreenMapping captures one screenshot so shared screenState has current
// frame dimensions and active-area metadata before the first input action.
func (s *ToolSet) PrimeScreenMapping(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("tool set is not configured")
	}
	tool, ok := s.Get("screenshot")
	if !ok || tool == nil {
		return fmt.Errorf("screenshot tool is not available")
	}
	if _, err := tool.Call(ctx, "{}"); err != nil {
		return fmt.Errorf("capture screenshot: %w", err)
	}
	if s.screen == nil {
		return nil
	}
	width, height, active, _, ok := s.screen.ActiveAreaWithAge()
	if !ok || width <= 0 || height <= 0 || !active.Valid {
		return fmt.Errorf("screenshot did not establish screen mapping")
	}
	return nil
}

func (r *Runtime) PrimeScreenMappingOnStartup(ctx context.Context) error {
	if r == nil || r.tools == nil {
		return nil
	}
	if !r.config.HID.PointerTouchscreen() {
		return nil
	}

	startedAt := time.Now()
	primeCtx, cancel := context.WithTimeout(ctx, startupScreenMappingPrimeTimeout)
	defer cancel()

	var lastErr error
	attempts := 0
	for {
		attempts++
		if err := r.tools.PrimeScreenMapping(primeCtx); err != nil {
			lastErr = err
		} else {
			if r.logger != nil && r.tools.screen != nil {
				width, height, active, _, ok := r.tools.screen.ActiveAreaWithAge()
				if ok {
					r.logger.Info(
						"screen mapping prime succeeded: attempts=%d elapsed_ms=%d source=%dx%d active=%+v",
						attempts,
						time.Since(startedAt).Milliseconds(),
						width,
						height,
						active,
					)
				} else {
					r.logger.Info("screen mapping prime succeeded: attempts=%d elapsed_ms=%d", attempts, time.Since(startedAt).Milliseconds())
				}
			}
			return nil
		}

		timer := time.NewTimer(startupScreenMappingPrimeRetryInterval)
		select {
		case <-primeCtx.Done():
			timer.Stop()
			if r.logger != nil {
				r.logger.Warn("screen mapping prime failed: attempts=%d elapsed_ms=%d err=%v", attempts, time.Since(startedAt).Milliseconds(), lastErr)
			}
			return fmt.Errorf("screen mapping prime failed after %d attempts: %w", attempts, lastErr)
		case <-timer.C:
		}
	}
}
