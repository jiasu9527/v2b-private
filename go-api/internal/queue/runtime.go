package queue

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type JobFunc func(context.Context) error

type Enqueuer interface {
	Enqueue(queueName, jobName string, fn JobFunc) error
	Snapshot() Snapshot
}

type Snapshot struct {
	Running            bool
	Workers            int64
	CurrentJobs        int64
	ProcessedLastHour  int64
	FailedLast7Days    int64
	MaxRuntimeQueue    string
	MaxThroughputQueue string
	Queues             []QueueSnapshot
}

type QueueSnapshot struct {
	Name      string
	Processes int64
	Length    int64
	Wait      int64
}

type Runtime struct {
	workerCount int
	bufferSize  int

	started atomic.Bool

	ctx    context.Context
	cancel context.CancelFunc

	jobs chan queuedJob
	wg   sync.WaitGroup

	mu              sync.Mutex
	queues          map[string]*queueState
	processedEvents []jobEvent
	failedEvents    []jobEvent
}

type queuedJob struct {
	queueName string
	jobName   string
	enqueued  time.Time
	run       JobFunc
}

type queueState struct {
	name           string
	pendingTimes   []time.Time
	inFlight       int64
	totalRuntime   time.Duration
	recentFinished []jobEvent
}

type jobEvent struct {
	queueName string
	at        time.Time
	duration  time.Duration
}

func NewRuntime(workerCount, bufferSize int) *Runtime {
	if workerCount <= 0 {
		workerCount = 1
	}
	if bufferSize <= 0 {
		bufferSize = workerCount * 16
	}
	return &Runtime{
		workerCount: workerCount,
		bufferSize:  bufferSize,
		queues:      make(map[string]*queueState),
	}
}

func (r *Runtime) Start() {
	if r == nil || r.started.Load() {
		return
	}

	r.mu.Lock()
	if r.started.Load() {
		r.mu.Unlock()
		return
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.jobs = make(chan queuedJob, r.bufferSize)
	r.started.Store(true)
	r.mu.Unlock()

	for workerID := 0; workerID < r.workerCount; workerID++ {
		r.wg.Add(1)
		go r.worker()
	}
}

func (r *Runtime) Shutdown(ctx context.Context) {
	if r == nil || !r.started.Load() {
		return
	}

	r.mu.Lock()
	cancel := r.cancel
	jobs := r.jobs
	r.started.Store(false)
	r.cancel = nil
	r.jobs = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if jobs != nil {
		close(jobs)
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	if ctx == nil {
		<-done
		return
	}

	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (r *Runtime) Enqueue(queueName, jobName string, fn JobFunc) error {
	if r == nil {
		return errors.New("queue runtime unavailable")
	}
	if fn == nil {
		return errors.New("queue job is nil")
	}
	if !r.started.Load() {
		return errors.New("queue runtime not started")
	}

	queueName = normalizeQueueName(queueName)
	job := queuedJob{
		queueName: queueName,
		jobName:   jobName,
		enqueued:  time.Now(),
		run:       fn,
	}

	r.mu.Lock()
	state := r.queueState(queueName)
	state.pendingTimes = append(state.pendingTimes, job.enqueued)
	jobs := r.jobs
	r.mu.Unlock()

	select {
	case jobs <- job:
		return nil
	case <-r.ctx.Done():
		r.mu.Lock()
		r.dequeuePending(queueName)
		r.mu.Unlock()
		return errors.New("queue runtime stopped")
	}
}

func (r *Runtime) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}

	now := time.Now()
	cutProcessed := now.Add(-1 * time.Hour)
	cutFailed := now.Add(-7 * 24 * time.Hour)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.processedEvents = pruneEventsAfter(r.processedEvents, cutProcessed)
	r.failedEvents = pruneEventsAfter(r.failedEvents, cutFailed)

	queues := make([]QueueSnapshot, 0, len(r.queues))
	var currentJobs int64
	var maxRuntimeQueue string
	var maxRuntime time.Duration
	var maxThroughputQueue string
	var maxThroughput int

	for _, state := range r.queues {
		state.recentFinished = pruneEventsAfter(state.recentFinished, cutProcessed)
		pending := int64(len(state.pendingTimes))
		currentJobs += pending + state.inFlight

		wait := int64(0)
		if len(state.pendingTimes) > 0 {
			wait = int64(now.Sub(state.pendingTimes[0]).Seconds())
			if wait < 0 {
				wait = 0
			}
		}
		if pending > 0 || state.inFlight > 0 {
			queues = append(queues, QueueSnapshot{
				Name:      state.name,
				Processes: state.inFlight,
				Length:    pending,
				Wait:      wait,
			})
		}
		if state.totalRuntime > maxRuntime {
			maxRuntime = state.totalRuntime
			maxRuntimeQueue = state.name
		}
		if len(state.recentFinished) > maxThroughput {
			maxThroughput = len(state.recentFinished)
			maxThroughputQueue = state.name
		}
	}

	sort.Slice(queues, func(i, j int) bool {
		if queues[i].Processes != queues[j].Processes {
			return queues[i].Processes > queues[j].Processes
		}
		if queues[i].Length != queues[j].Length {
			return queues[i].Length > queues[j].Length
		}
		return queues[i].Name < queues[j].Name
	})

	return Snapshot{
		Running:            r.started.Load(),
		Workers:            int64(r.workerCount),
		CurrentJobs:        currentJobs,
		ProcessedLastHour:  int64(len(r.processedEvents)),
		FailedLast7Days:    int64(len(r.failedEvents)),
		MaxRuntimeQueue:    maxRuntimeQueue,
		MaxThroughputQueue: maxThroughputQueue,
		Queues:             queues,
	}
}

func (r *Runtime) worker() {
	defer r.wg.Done()

	for job := range r.jobs {
		r.markDequeued(job.queueName)

		startedAt := time.Now()
		err := job.run(r.ctx)
		r.markFinished(job.queueName, startedAt, time.Since(startedAt), err)
	}
}

func (r *Runtime) markDequeued(queueName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.queueState(queueName)
	if len(state.pendingTimes) > 0 {
		state.pendingTimes = state.pendingTimes[1:]
	}
	state.inFlight++
}

func (r *Runtime) markFinished(queueName string, startedAt time.Time, duration time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.queueState(queueName)
	if state.inFlight > 0 {
		state.inFlight--
	}
	state.totalRuntime += duration

	event := jobEvent{queueName: queueName, at: startedAt, duration: duration}
	if err != nil {
		r.failedEvents = append(r.failedEvents, event)
		return
	}

	r.processedEvents = append(r.processedEvents, event)
	state.recentFinished = append(state.recentFinished, event)
}

func (r *Runtime) queueState(queueName string) *queueState {
	state, ok := r.queues[queueName]
	if ok {
		return state
	}
	state = &queueState{name: queueName}
	r.queues[queueName] = state
	return state
}

func (r *Runtime) dequeuePending(queueName string) {
	state := r.queueState(queueName)
	if len(state.pendingTimes) > 0 {
		state.pendingTimes = state.pendingTimes[1:]
	}
}

func normalizeQueueName(queueName string) string {
	if queueName == "" {
		return "default"
	}
	return queueName
}

func pruneEventsAfter(events []jobEvent, cutoff time.Time) []jobEvent {
	if len(events) == 0 {
		return events
	}
	index := 0
	for index < len(events) && events[index].at.Before(cutoff) {
		index++
	}
	if index == 0 {
		return events
	}
	return append([]jobEvent(nil), events[index:]...)
}
