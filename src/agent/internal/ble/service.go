package ble

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrBluetoothUnavailable = errors.New("Bluetooth backend is unavailable")

type RuntimeStatus struct {
	StartedAt                 string `json:"started_at"`
	BackendAvailable          bool   `json:"backend_available"`
	DeviceName                string `json:"device_name,omitempty"`
	BoardIdentity             string `json:"board_identity,omitempty"`
	AdapterPath               string `json:"adapter_path,omitempty"`
	AdapterAddress            string `json:"adapter_address,omitempty"`
	AdapterPowered            bool   `json:"adapter_powered"`
	GattRegistered            bool   `json:"gatt_registered"`
	Advertising               bool   `json:"advertising"`
	PairingOpen               bool   `json:"pairing_open"`
	PairingDeadline           string `json:"pairing_deadline,omitempty"`
	BondedDeviceCount         int    `json:"bonded_device_count"`
	TrustedDevicePath         string `json:"trusted_device_path,omitempty"`
	TrustedDeviceName         string `json:"trusted_device_name,omitempty"`
	TrustedDeviceAddr         string `json:"trusted_device_address,omitempty"`
	ConnectedDevicePath       string `json:"connected_device_path,omitempty"`
	ConnectedDeviceName       string `json:"connected_device_name,omitempty"`
	ConnectedDeviceAddr       string `json:"connected_device_address,omitempty"`
	Connected                 bool   `json:"connected"`
	Paired                    bool   `json:"paired"`
	ServicesResolved          bool   `json:"services_resolved"`
	ANCSSubscribed            bool   `json:"ancs_subscribed"`
	LastError                 string `json:"last_error,omitempty"`
	EventCount                int    `json:"event_count"`
	EventGeneration           string `json:"event_generation"`
	OldestEventID             string `json:"oldest_event_id"`
	LastEventID               string `json:"last_event_id"`
	PairingServiceUUID        string `json:"pairing_service_uuid"`
	PairingCharacteristicUUID string `json:"pairing_characteristic_uuid"`
}

type statusState struct {
	mu     sync.RWMutex
	status RuntimeStatus
}

func newStatusState() *statusState {
	return &statusState{status: RuntimeStatus{
		StartedAt:                 time.Now().UTC().Format(time.RFC3339Nano),
		PairingServiceUUID:        PairingServiceUUID,
		PairingCharacteristicUUID: PairingCharacteristicUUID,
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
	result.EventGeneration = stats.Generation
	result.OldestEventID = stats.OldestID
	result.LastEventID = stats.LastID
	return result
}

type pairingBackend interface {
	StartPairing() error
	Disconnect() error
	ForgetPairing() (int, error)
}

type Service struct {
	store     *EventStore
	status    *statusState
	consumer  *ANCSConsumer
	backendMu sync.RWMutex
	backend   pairingBackend
}

func NewService(eventCapacity int) *Service {
	store := NewEventStore(eventCapacity)
	return &Service{
		store:    store,
		status:   newStatusState(),
		consumer: NewANCSConsumer(store),
	}
}

func (s *Service) EventsSince(since uint64, limit int, generation string) EventPage {
	return s.store.PageForGeneration(since, limit, generation)
}

func (s *Service) Status() RuntimeStatus {
	return s.status.snapshot(s.store.Stats())
}

func (s *Service) StartPairing() error {
	s.backendMu.RLock()
	backend := s.backend
	s.backendMu.RUnlock()
	if backend == nil {
		return ErrBluetoothUnavailable
	}
	return backend.StartPairing()
}

func (s *Service) Disconnect() error {
	s.backendMu.RLock()
	backend := s.backend
	s.backendMu.RUnlock()
	if backend == nil {
		return ErrBluetoothUnavailable
	}
	return backend.Disconnect()
}

func (s *Service) ForgetPairing() (int, error) {
	s.backendMu.RLock()
	backend := s.backend
	s.backendMu.RUnlock()
	if backend == nil {
		return 0, ErrBluetoothUnavailable
	}
	return backend.ForgetPairing()
}

func (s *Service) setBackend(backend pairingBackend) {
	s.backendMu.Lock()
	s.backend = backend
	s.backendMu.Unlock()
}

func (s *Service) clearBackend(backend pairingBackend) {
	s.backendMu.Lock()
	if s.backend == backend {
		s.backend = nil
	}
	s.backendMu.Unlock()
}

func (s *Service) RunBlueZ(ctx context.Context, deviceName string, pairingWindow time.Duration) error {
	backend := newBlueZBackend(s, deviceName, pairingWindow)
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
