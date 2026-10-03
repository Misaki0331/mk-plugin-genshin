package genshin

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const rankingScanRequestLimit = 8
const rankingScanTimeout = 30 * time.Second

var (
	errRankingScanBusy = errors.New("ranking eligibility scan is busy")
)

// rankingScanGuard bounds both expensive scans and the callers waiting for an
// identical in-flight scan. Results are not retained after the scan finishes,
// so suspension and visibility changes are rechecked on the next request.
type rankingScanGuard struct {
	requests    chan struct{}
	scan        chan struct{}
	group       singleflight.Group
	mu          sync.Mutex
	lastStarted time.Time
	minInterval time.Duration
	now         func() time.Time
	timeout     time.Duration
}

func newRankingScanGuard() *rankingScanGuard {
	return &rankingScanGuard{
		requests:    make(chan struct{}, rankingScanRequestLimit),
		scan:        make(chan struct{}, 1),
		minInterval: time.Second,
		now:         time.Now,
		timeout:     rankingScanTimeout,
	}
}

func (g *rankingScanGuard) run(c context.Context, key string, scan func(context.Context) (map[string]bool, error)) (map[string]bool, error) {
	select {
	case g.requests <- struct{}{}:
	default:
		return nil, errRankingScanBusy
	}

	result := g.group.DoChan(key, func() (any, error) {
		// The shared operation must not inherit the first waiter's cancellation;
		// every waiter can still leave independently below. Bound orphaned work.
		scanContext, cancel := context.WithTimeout(context.Background(), g.timeout)
		defer cancel()
		select {
		case g.scan <- struct{}{}:
			defer func() { <-g.scan }()
		case <-scanContext.Done():
			return nil, scanContext.Err()
		}

		g.mu.Lock()
		wait := g.lastStarted.Add(g.minInterval).Sub(g.now())
		g.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-scanContext.Done():
				return nil, scanContext.Err()
			}
		}
		now := g.now()
		g.mu.Lock()
		g.lastStarted = now
		g.mu.Unlock()
		return scan(scanContext)
	})

	select {
	case <-c.Done():
		// Keep this queue slot reserved until the shared work ends; otherwise
		// canceled callers could create an unbounded orphaned queue.
		go func() {
			<-result
			<-g.requests
		}()
		return nil, c.Err()
	case result := <-result:
		<-g.requests
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(map[string]bool), nil
	}
}
