package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/oberlevd/platform-lib/logger"
)

func testLogger() *logger.Logger {
	return logger.New(logger.Config{
		Service: "lifecycle-test",
		Output:  io.Discard,
	})
}

func TestManagerRunsShutdownFuncsInReverseOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string

	m := New(testLogger())

	record := func(name string) ShutdownFunc {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	m.Register("first", record("first"))
	m.Register("second", record("second"))
	m.Register("third", record("third"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	m.Run(ctx, time.Second)

	mu.Lock()
	defer mu.Unlock()

	want := []string{"third", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want[i])
		}
	}
}

func TestManagerContinuesAfterComponentError(t *testing.T) {
	var mu sync.Mutex
	var called []string

	m := New(testLogger())

	m.Register("failing", func(ctx context.Context) error {
		mu.Lock()
		called = append(called, "failing")
		mu.Unlock()
		return errors.New("boom")
	})
	m.Register("ok", func(ctx context.Context) error {
		mu.Lock()
		called = append(called, "ok")
		mu.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // сигнал уже "получен" до вызова Run

	m.Run(ctx, time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 2 {
		t.Fatalf("called = %v, want both components to run despite one failing", called)
	}
}

func TestCloserShutdownCallsClose(t *testing.T) {
	closer := &fakeCloser{}
	shutdown := CloserShutdown(closer)

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !closer.closed {
		t.Error("expected Close() to be called")
	}
}

type fakeCloser struct {
	closed bool
}

func (f *fakeCloser) Close() error {
	f.closed = true
	return nil
}
