package aiops

import (
	"context"
	"io"
	"sync"
	"time"
)

// Reasoning models may be silent before the first response body byte. This is
// distinct from a stream that has started and then stalled. The legacy 30s
// failover setting remains the subsequent idle window, not the initial model
// inference budget; the independent completion deadline still bounds both.
const providerFirstResponseGrace = 90 * time.Second

// One timer watches network inactivity, not the duration of a healthy stream.
// A mutex plus the last-activity check prevents an already-fired timer callback
// from canceling a request whose body read has just renewed its idle window.
type providerIdleWatch struct {
	mu          sync.Mutex
	timer       *time.Timer
	cancel      context.CancelFunc
	lastRead    time.Time
	timeout     time.Duration
	idleTimeout time.Duration
	closed      bool
	timedOut    bool
}

func newProviderIdleContext(parent context.Context, timeout time.Duration) (context.Context, *providerIdleWatch) {
	return newProviderResponseContext(parent, timeout, timeout)
}

func newProviderResponseContext(parent context.Context, firstTimeout, idleTimeout time.Duration) (context.Context, *providerIdleWatch) {
	ctx, cancel := context.WithCancel(parent)
	w := &providerIdleWatch{cancel: cancel, lastRead: time.Now(), timeout: firstTimeout, idleTimeout: idleTimeout}
	w.timer = time.AfterFunc(firstTimeout, w.check)
	return ctx, w
}

func (w *providerIdleWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if remaining := w.timeout - time.Since(w.lastRead); remaining > 0 {
		w.timer.Reset(remaining)
		return
	}
	w.timedOut = true
	w.closed = true
	w.cancel()
}

func (w *providerIdleWatch) touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.lastRead = time.Now()
		w.timeout = w.idleTimeout
		w.timer.Reset(w.timeout)
	}
}

func (w *providerIdleWatch) expired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.timedOut
}

func (w *providerIdleWatch) close() {
	w.mu.Lock()
	w.closed = true
	w.timer.Stop()
	w.mu.Unlock()
	w.cancel()
}

type providerActivityReader struct {
	io.Reader
	activity *providerIdleWatch
}

func (r *providerActivityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity.touch()
	}
	return n, err
}
