package agent

import "sync"

type mobileGymSession struct {
	EpisodeID   string
	BridgeURL   string
	BridgeToken string
}

type mobileGymSessionStore struct {
	mu      sync.RWMutex
	current mobileGymSession
	active  bool
}

func (s *mobileGymSessionStore) Set(session mobileGymSession) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = session
	s.active = true
}

func (s *mobileGymSessionStore) Get() (mobileGymSession, bool) {
	if s == nil {
		return mobileGymSession{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.active {
		return mobileGymSession{}, false
	}
	return s.current, true
}

func (s *mobileGymSessionStore) Clear(episodeID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if episodeID != "" && s.current.EpisodeID != episodeID {
		return
	}
	s.current = mobileGymSession{}
	s.active = false
}
