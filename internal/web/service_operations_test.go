package web

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestBeginServiceRestartDeduplicatesActiveOperation(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server.serviceAction = func(verb, unit string) error {
		if verb != "restart" || unit != "bypasscore" {
			t.Fatalf("unexpected action %s %s", verb, unit)
		}
		calls.Add(1)
		close(started)
		<-release
		return nil
	}
	server.serviceActive = func(string) bool { return true }

	first, created, err := server.beginServiceRestart("bypasscore", "BypassCore", 0)
	if err != nil || !created {
		t.Fatalf("first operation: created=%v err=%v", created, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("restart did not start")
	}
	second, created, err := server.beginServiceRestart("bypasscore", "BypassCore", 0)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("duplicate operation was not coalesced: first=%q second=%q created=%v err=%v",
			first.ID, second.ID, created, err)
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := server.serviceOperationSnapshot(first.ID)
		if ok && snapshot.Done {
			if !snapshot.Success || calls.Load() != 1 {
				t.Fatalf("unexpected completed operation: %#v calls=%d", snapshot, calls.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("restart operation did not finish")
}
