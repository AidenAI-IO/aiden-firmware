package agent

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// GPIOWatcher watches a GPIO pin for edge events
type GPIOWatcher struct {
	pin       int
	valuePath string
	fd        int
	callback  func()
	stopChan  chan struct{}
}

// NewGPIOWatcher creates a new GPIO watcher
func NewGPIOWatcher(pin int, callback func()) (*GPIOWatcher, error) {
	w := &GPIOWatcher{
		pin:       pin,
		valuePath: fmt.Sprintf("/sys/class/gpio/gpio%d/value", pin),
		callback:  callback,
		stopChan:  make(chan struct{}),
	}

	// Export GPIO if not already exported
	if err := w.exportGPIO(); err != nil {
		return nil, fmt.Errorf("export GPIO: %w", err)
	}

	// Set direction to input
	if err := w.setDirection("in"); err != nil {
		return nil, fmt.Errorf("set direction: %w", err)
	}

	// Set edge to falling
	if err := w.setEdge("falling"); err != nil {
		return nil, fmt.Errorf("set edge: %w", err)
	}

	return w, nil
}

// exportGPIO exports the GPIO pin
func (w *GPIOWatcher) exportGPIO() error {
	// Check if already exported
	if _, err := os.Stat(fmt.Sprintf("/sys/class/gpio/gpio%d", w.pin)); err == nil {
		return nil // Already exported
	}

	f, err := os.OpenFile("/sys/class/gpio/export", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open export: %w", err)
	}
	defer f.Close()

	pinStr := strconv.Itoa(w.pin)
	if _, err := f.WriteString(pinStr); err != nil {
		return fmt.Errorf("write pin: %w", err)
	}

	// Wait for GPIO to be ready
	time.Sleep(100 * time.Millisecond)
	return nil
}

// setDirection sets the GPIO direction
func (w *GPIOWatcher) setDirection(direction string) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d/direction", w.pin)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open direction: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(direction); err != nil {
		return fmt.Errorf("write direction: %w", err)
	}

	return nil
}

// setEdge sets the GPIO edge trigger
func (w *GPIOWatcher) setEdge(edge string) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d/edge", w.pin)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open edge: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(edge); err != nil {
		return fmt.Errorf("write edge: %w", err)
	}

	return nil
}

// Start starts watching the GPIO pin
func (w *GPIOWatcher) Start() error {
	// Open GPIO value file
	fd, err := unix.Open(w.valuePath, unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open value: %w", err)
	}
	w.fd = fd

	// Start watching in a goroutine
	go w.watch()

	return nil
}

// watch watches for GPIO events
func (w *GPIOWatcher) watch() {
	buf := make([]byte, 64)

	for {
		select {
		case <-w.stopChan:
			return
		default:
		}

		// Seek to beginning
		unix.Seek(w.fd, 0, 0)

		// Read current value
		unix.Read(w.fd, buf)

		// Poll for events
		fds := []unix.PollFd{
			{
				Fd:     int32(w.fd),
				Events: unix.POLLPRI | unix.POLLERR,
			},
		}

		// Wait for event (with timeout to check stopChan)
		n, err := unix.Poll(fds, 1000) // 1 second timeout
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}

		if n > 0 && (fds[0].Revents&unix.POLLPRI) != 0 {
			// GPIO event detected
			if w.callback != nil {
				w.callback()
			}

			// Debounce: wait 150ms before next event
			time.Sleep(150 * time.Millisecond)
		}
	}
}

// Stop stops watching the GPIO pin
func (w *GPIOWatcher) Stop() {
	close(w.stopChan)
	if w.fd > 0 {
		unix.Close(w.fd)
		w.fd = 0
	}
}
