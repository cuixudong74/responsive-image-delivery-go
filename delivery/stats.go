package delivery

import "sync"

// EventKind classifies a recorded delivery event.
type EventKind string

const (
	EventServed   EventKind = "served"
	EventRejected EventKind = "rejected"
	EventFailed   EventKind = "failed"
)

// Event describes one delivered (or rejected) request, enough to
// verify cache and budget policy effectiveness.
type Event struct {
	SourceID     string
	Spec         VariantSpec
	Kind         EventKind
	Reused       bool // result came from cache or coalescing
	Coalesced    bool // waited on an in-flight generation
	WithinBudget bool
	Reason       ReasonCode
	Bytes        int
}

// Stats aggregates delivery statistics.
type Stats struct {
	mu sync.Mutex

	Requests   int64
	Generated  int64 // newly generated in this process
	CacheHits  int64
	Coalesced  int64
	Rejected   int64
	Failed     int64
	OverBudget int64

	events []Event
	head   int // next write position
	count  int
}

func newStats(capacity int) *Stats {
	if capacity <= 0 {
		capacity = 1
	}
	return &Stats{events: make([]Event, capacity)}
}

// record adds an event and updates counters.
func (s *Stats) record(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	switch ev.Kind {
	case EventServed:
		if ev.Reused {
			if ev.Coalesced {
				s.Coalesced++
			} else {
				s.CacheHits++
			}
		} else {
			s.Generated++
		}
		if !ev.WithinBudget {
			s.OverBudget++
		}
	case EventRejected:
		s.Rejected++
	case EventFailed:
		s.Failed++
	}
	s.events[s.head] = ev
	s.head = (s.head + 1) % len(s.events)
	if s.count < len(s.events) {
		s.count++
	}
}

// Recent returns up to n most recent events, newest first.
func (s *Stats) Recent(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.count {
		n = s.count
	}
	out := make([]Event, n)
	for i := 0; i < n; i++ {
		idx := (s.head - 1 - i + len(s.events)) % len(s.events)
		out[i] = s.events[idx]
	}
	return out
}

// Snapshot is a consistent copy of the counters.
type Snapshot struct {
	Requests   int64
	Generated  int64
	CacheHits  int64
	Coalesced  int64
	Rejected   int64
	Failed     int64
	OverBudget int64
}

// Snapshot returns current counters.
func (s *Stats) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Requests:   s.Requests,
		Generated:  s.Generated,
		CacheHits:  s.CacheHits,
		Coalesced:  s.Coalesced,
		Rejected:   s.Rejected,
		Failed:     s.Failed,
		OverBudget: s.OverBudget,
	}
}
