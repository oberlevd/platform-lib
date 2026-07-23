package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeStopper struct {
	mu         sync.Mutex
	stopped    bool
	graceDelay time.Duration
}

func (f *fakeStopper) GracefulStop() {
	time.Sleep(f.graceDelay)
}

func (f *fakeStopper) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

func (f *fakeStopper) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func TestGRPCServerShutdownSucceedsWithinDeadline(t *testing.T) {
	fake := &fakeStopper{graceDelay: 5 * time.Millisecond}
	shutdown := GRPCServerShutdown(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := shutdown(ctx); err != nil {
		t.Errorf("expected nil error for graceful stop finishing within deadline, got %v", err)
	}
	if fake.wasStopped() {
		t.Error("Stop() should not be called when GracefulStop finished within deadline")
	}
}

func TestGRPCServerShutdownForcesStopOnTimeout(t *testing.T) {
	fake := &fakeStopper{graceDelay: 300 * time.Millisecond}
	shutdown := GRPCServerShutdown(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := shutdown(ctx)
	if err == nil {
		t.Fatal("expected error when GracefulStop exceeds the deadline")
	}

	// GracefulStop работает в отдельной горутине и может ещё не успеть
	// дойти до Stop() ровно в момент возврата shutdown() — даём небольшой
	// запас, прежде чем проверять, что принудительная остановка произошла.
	deadline := time.Now().Add(200 * time.Millisecond)
	for !fake.wasStopped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if !fake.wasStopped() {
		t.Error("expected Stop() to be called after GracefulStop exceeded the deadline")
	}
}
