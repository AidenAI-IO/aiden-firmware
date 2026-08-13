package agent

import (
	"reflect"
	"strings"
	"sync"
)

type StorageWriteGate interface {
	AllowWrite(StorageCapability) bool
	HandleWriteError(error) bool
}

type ModelRuntimeBindings struct {
	mu               sync.RWMutex
	sessionID        func() string
	storageWriteGate StorageWriteGate
}

func NewModelRuntimeBindings() *ModelRuntimeBindings {
	return &ModelRuntimeBindings{}
}

func (b *ModelRuntimeBindings) SetSessionIDProvider(provider func() string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.sessionID = provider
	b.mu.Unlock()
}

func (b *ModelRuntimeBindings) CurrentSessionID() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	provider := b.sessionID
	b.mu.RUnlock()
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider())
}

func (b *ModelRuntimeBindings) SetStorageWriteGate(gate StorageWriteGate) {
	if b == nil {
		return
	}
	if gate != nil {
		value := reflect.ValueOf(gate)
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
			if value.IsNil() {
				gate = nil
			}
		}
	}
	b.mu.Lock()
	b.storageWriteGate = gate
	b.mu.Unlock()
}

func (b *ModelRuntimeBindings) StorageWriteGate() StorageWriteGate {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.storageWriteGate
}
