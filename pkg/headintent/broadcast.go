// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"errors"
	"sync"
	"time"
)

// Broadcaster fans a Receiver's read-only diagnostics snapshot out to a bounded
// set of subscribers (the mapper->Electron diagnostics stream). It is deliberately
// a READ-ONLY consumer of the receiver: it only calls the injected snapshot func
// and never touches UDP receive, controller evaluation, mixing, or CRSF transmit,
// so a slow, stuck, or absent subscriber can have zero effect on mapper operation.
//
// Semantics (owner spec, CB8 slice 3A):
//   - snapshot delivered immediately on Subscribe;
//   - state transitions pushed immediately (bypass the value rate limit);
//   - ordinary value-only updates rate-limited to ~10 Hz (MinValueGap);
//   - each subscriber has a bounded one-item, latest-value buffer — superseded
//     snapshots are dropped rather than blocking the producer;
//   - at most MaxSubscribers concurrent subscribers (default 4); the next one is
//     refused with ErrTooManySubscribers.
type Broadcaster struct {
	snapshot    SnapshotFunc
	now         func() time.Time
	poll        time.Duration
	minValueGap time.Duration
	maxSubs     int

	mu          sync.Mutex
	subs        []*subscriber
	haveEmitted bool
	lastVitals  vitals
	lastEmitAt  time.Time

	started bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// SnapshotFunc returns the current read-only diagnostics snapshot.
type SnapshotFunc func() Diagnostics

type subscriber struct {
	ch chan Diagnostics // buffered 1: latest-value
}

// ErrTooManySubscribers is returned by Subscribe once MaxSubscribers are active.
var ErrTooManySubscribers = errors.New("too many head-intent diagnostics subscribers")

// BroadcasterOptions configure a Broadcaster; the zero value is usable.
type BroadcasterOptions struct {
	Now            func() time.Time // default time.Now (monotonic)
	PollInterval   time.Duration    // how often Start samples the source; default 20ms
	MinValueGap    time.Duration    // min gap between value-only updates; default 100ms (~10Hz)
	MaxSubscribers int              // default 4
}

// NewBroadcaster builds a stopped broadcaster over the given snapshot source.
func NewBroadcaster(snapshot SnapshotFunc, opts BroadcasterOptions) *Broadcaster {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 20 * time.Millisecond
	}
	if opts.MinValueGap <= 0 {
		opts.MinValueGap = 100 * time.Millisecond
	}
	if opts.MaxSubscribers <= 0 {
		opts.MaxSubscribers = 4
	}
	return &Broadcaster{
		snapshot:    snapshot,
		now:         opts.Now,
		poll:        opts.PollInterval,
		minValueGap: opts.MinValueGap,
		maxSubs:     opts.MaxSubscribers,
	}
}

// Start launches the sampling loop. Idempotent.
func (b *Broadcaster) Start() {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return
	}
	b.started = true
	b.stopCh = make(chan struct{})
	stop := b.stopCh
	b.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(b.poll)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				b.pump()
			}
		}
	}()
}

// Stop halts the sampling loop and waits for it to exit. Idempotent. Subscribers
// are left intact (their RPC goroutines exit on their own context cancellation).
func (b *Broadcaster) Stop() {
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return
	}
	b.started = false
	close(b.stopCh)
	b.mu.Unlock()
	b.wg.Wait()
}

// Subscribe registers a subscriber and immediately primes it with the current
// snapshot. It returns the receive-only channel, an unsubscribe func, and an error
// if MaxSubscribers are already active. The caller MUST call unsubscribe when done.
func (b *Broadcaster) Subscribe() (<-chan Diagnostics, func(), error) {
	b.mu.Lock()
	if len(b.subs) >= b.maxSubs {
		b.mu.Unlock()
		return nil, func() {}, ErrTooManySubscribers
	}
	s := &subscriber{ch: make(chan Diagnostics, 1)}
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	// Immediate snapshot on subscription (latest-value semantics).
	deliverLatest(s.ch, b.snapshot())

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() { b.remove(s) })
	}
	return s.ch, unsubscribe, nil
}

func (b *Broadcaster) remove(s *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, x := range b.subs {
		if x == s {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			return
		}
	}
}

// SubscriberCount reports the number of active subscribers. It lets callers/tests
// confirm that a disconnect or cancellation released its subscription.
func (b *Broadcaster) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// pump samples the source once and fans the snapshot out iff it is a state
// transition (always) or a rate-limited value change. Reading the source is the
// only thing it does to the receiver; it cannot mutate any mapper state.
func (b *Broadcaster) pump() {
	cur := b.snapshot()
	v := toVitals(cur)
	now := b.now()

	b.mu.Lock()
	emit := false
	switch {
	case !b.haveEmitted || v.state != b.lastVitals.state:
		emit = true // first sample or state transition: bypass the rate limit
	case v != b.lastVitals && now.Sub(b.lastEmitAt) >= b.minValueGap:
		emit = true // ordinary value-only update: rate-limited
	}
	if !emit {
		b.mu.Unlock()
		return
	}
	b.haveEmitted = true
	b.lastVitals = v
	b.lastEmitAt = now
	subs := make([]*subscriber, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()

	for _, s := range subs {
		deliverLatest(s.ch, cur)
	}
}

// deliverLatest performs a non-blocking, latest-value delivery into a cap-1
// channel: if the slot is full it drops the superseded value and inserts the new
// one. It never blocks the producer, so a slow consumer cannot stall the mapper.
func deliverLatest(ch chan Diagnostics, d Diagnostics) {
	select {
	case ch <- d:
	default:
		select {
		case <-ch: // drop superseded snapshot
		default:
		}
		select {
		case ch <- d:
		default:
		}
	}
}

// vitals is a fully comparable projection of the wire-relevant snapshot fields, so
// state-transition and value-change detection use a plain `==` (no maps/pointers).
type vitals struct {
	state                         string
	total, valid, invalid         uint64
	hasLastValid                  bool
	seq                           int64
	gaps, repeats, regs           uint64
	ageMs                         int64
	hasAge                        bool
	rate                          int
	yaw, pitch, roll              float64
	tracking, centered, hasCenter bool
	timeout                       int
	hasTimeout                    bool
	clockDelta                    int64
	hasClock                      bool
	staleMs                       int64
	lastErr                       string
}

func toVitals(d Diagnostics) vitals {
	v := vitals{
		state:   d.State,
		total:   d.Counts.Total,
		valid:   d.Counts.Valid,
		invalid: d.Counts.Invalid,
		gaps:    d.SeqGaps,
		repeats: d.SeqRepeats,
		regs:    d.SeqRegressions,
		rate:    d.RatePerSec,
		staleMs: d.StaleMs,
		lastErr: d.LastError,
	}
	if d.PacketAgeMs != nil {
		v.hasAge = true
		v.ageMs = *d.PacketAgeMs
	}
	if d.SenderClockDeltaMs != nil {
		v.hasClock = true
		v.clockDelta = *d.SenderClockDeltaMs
	}
	if d.LastValid != nil {
		v.hasLastValid = true
		v.seq = d.LastValid.Seq
		v.yaw = d.LastValid.YawDeg
		v.pitch = d.LastValid.PitchDeg
		v.roll = d.LastValid.RollDeg
		v.tracking = d.LastValid.TrackingEnabled
		if d.LastValid.Centered != nil {
			v.hasCenter = true
			v.centered = *d.LastValid.Centered
		}
		if d.LastValid.TimeoutMs != nil {
			v.hasTimeout = true
			v.timeout = *d.LastValid.TimeoutMs
		}
	}
	return v
}
