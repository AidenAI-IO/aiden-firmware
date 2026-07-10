package statemanager

import (
	"sort"
	"sync"
)

type StateManager struct {
	m sync.Map
}

type StateEntry struct {
	Key   string
	Value string
}

func NewStateManager() *StateManager {
	return &StateManager{}
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

func (s *StateManager) GetAllStates() []StateEntry {
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
