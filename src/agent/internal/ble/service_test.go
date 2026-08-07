package ble

import (
	"sync"
	"testing"
	"time"
)

type orderedWakeBackend struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	once         sync.Once
}

func (b *orderedWakeBackend) NotifyWake(sequence uint64, _ string) (bool, error) {
	if sequence == 1 {
		b.once.Do(func() { close(b.firstStarted) })
		<-b.releaseFirst
	}
	return true, nil
}

func TestServiceWakeSerializesSequenceAndStatus(t *testing.T) {
	service := NewService(4)
	backend := &orderedWakeBackend{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	service.setBackend(backend)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		if sequence, _, err := service.Wake("first"); err != nil || sequence != 1 {
			t.Errorf("first Wake() sequence=%d error=%v", sequence, err)
		}
	}()
	<-backend.firstStarted

	secondDone := make(chan struct{})
	secondStarted := make(chan struct{})
	go func() {
		defer close(secondDone)
		close(secondStarted)
		if sequence, _, err := service.Wake("second"); err != nil || sequence != 2 {
			t.Errorf("second Wake() sequence=%d error=%v", sequence, err)
		}
	}()
	<-secondStarted
	select {
	case <-secondDone:
		t.Fatal("second wake overtook the in-flight first wake")
	case <-time.After(100 * time.Millisecond):
	}

	close(backend.releaseFirst)
	<-firstDone
	<-secondDone
	status := service.Status()
	if status.LastWakeID != "2" || status.LastWakeReason != "second" {
		t.Fatalf("wake status regressed: %#v", status)
	}
}
