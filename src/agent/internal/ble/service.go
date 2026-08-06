package ble

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var ErrBluetoothUnavailable = errors.New("Bluetooth backend is unavailable")

type RuntimeStatus struct {
	StartedAt              string `json:"started_at"`
	BackendAvailable       bool   `json:"backend_available"`
	AdapterPath            string `json:"adapter_path,omitempty"`
	AdapterAddress         string `json:"adapter_address,omitempty"`
	AdapterPowered         bool   `json:"adapter_powered"`
	GattRegistered         bool   `json:"gatt_registered"`
	Advertising            bool   `json:"advertising"`
	WakeSubscriber         bool   `json:"wake_subscriber"`
	ConnectedDevicePath    string `json:"connected_device_path,omitempty"`
	ConnectedDeviceName    string `json:"connected_device_name,omitempty"`
	ConnectedDeviceAddr    string `json:"connected_device_address,omitempty"`
	Connected              bool   `json:"connected"`
	Paired                 bool   `json:"paired"`
	ServicesResolved       bool   `json:"services_resolved"`
	ANCSSubscribed         bool   `json:"ancs_subscribed"`
	LastWakeID             string `json:"last_wake_id"`
	LastWakeReason         string `json:"last_wake_reason,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	EventCount             int    `json:"event_count"`
	OldestEventID          string `json:"oldest_event_id"`
	LastEventID            string `json:"last_event_id"`
	WakeServiceUUID        string `json:"wake_service_uuid"`
	WakeCharacteristicUUID string `json:"wake_characteristic_uuid"`
}

type statusState struct {
	mu     sync.RWMutex
	status RuntimeStatus
}

func newStatusState() *statusState {
	return &statusState{status: RuntimeStatus{
		StartedAt:              time.Now().UTC().Format(time.RFC3339Nano),
		WakeServiceUUID:        WakeServiceUUID,
		WakeCharacteristicUUID: WakeCharacteristicUUID,
	}}
}

func (s *statusState) update(fn func(*RuntimeStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
}

func (s *statusState) snapshot(stats EventStats) RuntimeStatus {
	s.mu.RLock()
	result := s.status
	s.mu.RUnlock()
	result.EventCount = stats.Count
	result.OldestEventID = stats.OldestID
	result.LastEventID = stats.LastID
	return result
}

type wakeBackend interface {
	NotifyWake(sequence uint64, reason string) (bool, error)
}

type Service struct {
	store        *EventStore
	status       *statusState
	consumer     *ANCSConsumer
	backendMu    sync.RWMutex
	backend      wakeBackend
	wakeSequence atomic.Uint64
}

func NewService(eventCapacity int) *Service {
	store := NewEventStore(eventCapacity)
	return &Service{
		store:    store,
		status:   newStatusState(),
		consumer: NewANCSConsumer(store),
	}
}

func (s *Service) EventsSince(since uint64, limit int) EventPage {
	return s.store.Page(since, limit)
}

func (s *Service) Status() RuntimeStatus {
	return s.status.snapshot(s.store.Stats())
}

func (s *Service) Wake(reason string) (sequence uint64, delivered bool, err error) {
	s.backendMu.RLock()
	backend := s.backend
	s.backendMu.RUnlock()
	if backend == nil {
		return 0, false, ErrBluetoothUnavailable
	}

	sequence = s.wakeSequence.Add(1)
	delivered, err = backend.NotifyWake(sequence, reason)
	if err != nil {
		return 0, false, err
	}
	s.status.update(func(status *RuntimeStatus) {
		status.LastWakeID = strconv.FormatUint(sequence, 10)
		status.LastWakeReason = reason
	})
	return sequence, delivered, nil
}

func (s *Service) setBackend(backend wakeBackend) {
	s.backendMu.Lock()
	s.backend = backend
	s.backendMu.Unlock()
}

func (s *Service) clearBackend(backend wakeBackend) {
	s.backendMu.Lock()
	if s.backend == backend {
		s.backend = nil
	}
	s.backendMu.Unlock()
}

func (s *Service) RunBlueZ(ctx context.Context, deviceName string) error {
	backend := newBlueZBackend(s, deviceName)
	if err := backend.start(); err != nil {
		backend.close()
		s.status.update(func(status *RuntimeStatus) {
			status.BackendAvailable = false
			status.GattRegistered = false
			status.Advertising = false
			status.LastError = err.Error()
		})
		return err
	}
	s.setBackend(backend)
	defer s.clearBackend(backend)
	defer backend.close()
	s.status.update(func(status *RuntimeStatus) { status.LastError = "" })
	return backend.run(ctx)
}
