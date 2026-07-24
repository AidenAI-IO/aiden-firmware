package statemanager

import (
	"slices"
	"sort"
	"sync"
)

type StateManager struct {
	m         sync.Map
	updaterMu sync.Mutex
	updaters  []StateUpdater
}

type StateEntry struct {
	Key   string
	Value string
}

func NewStateManager() *StateManager {
	return &StateManager{}
}

func (s *StateManager) RegisterUpdater(updater StateUpdater) {
	s.updaterMu.Lock()
	defer s.updaterMu.Unlock()
	if updater == nil {
		return
	}
	if slices.Contains(s.updaters, updater) {
		return
	}
	s.updaters = append(s.updaters, updater)
}

func (s *StateManager) GetState(key string) string {
	value, ok := s.m.Load(key)
	if !ok {
		return ""
	}
	return value.(string)
}

func (s *StateManager) SetState(key string, value string) {
	s.m.Store(key, value)
}

func (s *StateManager) DeleteState(key string) {
	s.m.Delete(key)
}

func (s *StateManager) update() {
	s.updaterMu.Lock()
	defer s.updaterMu.Unlock()
	for _, updater := range s.updaters {
		states := updater.UpdateState()
		for key, value := range states {
			s.m.Store(key, value)
		}
	}
}

func (s *StateManager) GetAllStates() []StateEntry {
	s.update()

	states := make([]StateEntry, 0)
	s.m.Range(func(key, value interface{}) bool {
		states = append(states, StateEntry{Key: key.(string), Value: value.(string)})
		return true
	})

	// stable sort by key
	sort.Slice(states, func(i, j int) bool {
		return states[i].Key < states[j].Key
	})

	return states
}

// StateUpdater is a interface that can be used to update the state of the state manager.
type StateUpdater interface {
	UpdateState() map[string]string
}
