// Package syncer keeps one account's published snapshot fresh.
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

type Loader interface {
	LoadSnapshot(context.Context, provider.Account, uint64) (*namespace.Snapshot, error)
}

type State string

const (
	StateEmpty      State = "empty"
	StateRefreshing State = "refreshing"
	StateReady      State = "ready"
	StateStale      State = "stale"
)

type Status struct {
	State       State
	Generation  uint64
	LastAttempt time.Time
	LastSuccess time.Time
	LastError   string
}

// Session owns the currently published snapshot for one provider account.
// A refresh builds a complete replacement before making it visible.
type Session struct {
	loader  Loader
	account provider.Account
	now     func() time.Time

	current atomic.Pointer[namespace.Snapshot]
	refresh sync.Mutex
	status  sync.Mutex
	state   Status
}

func New(loader Loader, account provider.Account) *Session {
	return &Session{
		loader:  loader,
		account: account,
		now:     time.Now,
		state:   Status{State: StateEmpty},
	}
}

// Current returns the last complete snapshot, or nil before the first
// successful refresh.
func (s *Session) Current() *namespace.Snapshot {
	return s.current.Load()
}

func (s *Session) Status() Status {
	s.status.Lock()
	defer s.status.Unlock()
	return s.state
}

// Refresh loads and atomically publishes the next complete generation.
// A failed refresh records a stale state but leaves Current unchanged.
func (s *Session) Refresh(ctx context.Context) error {
	s.refresh.Lock()
	defer s.refresh.Unlock()

	generation := uint64(1)
	if current := s.current.Load(); current != nil {
		generation = current.Generation() + 1
	}
	attemptedAt := s.now()
	s.updateStatus(func(status *Status) {
		status.State = StateRefreshing
		status.LastAttempt = attemptedAt
	})

	next, err := s.loader.LoadSnapshot(ctx, s.account, generation)
	if err == nil && next == nil {
		err = fmt.Errorf("snapshot loader returned nil")
	}
	if err != nil {
		s.updateStatus(func(status *Status) {
			status.State = StateStale
			status.LastError = err.Error()
		})
		return err
	}

	s.current.Store(next)
	succeededAt := s.now()
	s.updateStatus(func(status *Status) {
		status.State = StateReady
		status.Generation = next.Generation()
		status.LastSuccess = succeededAt
		status.LastError = ""
	})
	return nil
}

func (s *Session) updateStatus(update func(*Status)) {
	s.status.Lock()
	defer s.status.Unlock()
	update(&s.state)
}

type WatchOptions struct {
	PollInterval time.Duration
	Debounce     time.Duration
	MaxDelay     time.Duration
}

type Observer func(*namespace.Snapshot, Status) error

func (options WatchOptions) withDefaults() WatchOptions {
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.Debounce <= 0 {
		options.Debounce = 250 * time.Millisecond
	}
	if options.MaxDelay <= 0 {
		options.MaxDelay = time.Second
	}
	if options.MaxDelay < options.Debounce {
		options.MaxDelay = options.Debounce
	}
	return options
}

// Run loads the initial snapshot and polls the database, WAL, SHM, and parent
// directory for change hints. Bursts are merged before a full refresh. Refresh
// failures keep the old snapshot visible and are retried after MaxDelay.
func (s *Session) Run(ctx context.Context, options WatchOptions, observer Observer) error {
	options = options.withDefaults()
	beforeRefresh, err := inspectFiles(s.account.DatabasePath)
	if err != nil {
		return err
	}
	refreshErr := s.Refresh(ctx)
	if refreshErr != nil && s.Current() == nil {
		return refreshErr
	}
	previous, err := inspectFiles(s.account.DatabasePath)
	if err != nil {
		return err
	}
	if observer != nil {
		if err := observer(s.Current(), s.Status()); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()

	var firstChange time.Time
	var lastChange time.Time
	if refreshErr != nil || previous != beforeRefresh {
		firstChange = time.Now()
		lastChange = firstChange
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			current, err := inspectFiles(s.account.DatabasePath)
			if err != nil {
				return err
			}
			if current != previous {
				previous = current
				if firstChange.IsZero() {
					firstChange = now
				}
				lastChange = now
			}
			if firstChange.IsZero() {
				continue
			}

			quietEnough := now.Sub(lastChange) >= options.Debounce
			waitedLongEnough := now.Sub(firstChange) >= options.MaxDelay
			if !quietEnough && !waitedLongEnough {
				continue
			}

			if err := s.Refresh(ctx); err != nil {
				if observer != nil {
					if observerErr := observer(s.Current(), s.Status()); observerErr != nil {
						return observerErr
					}
				}
				// Keep the refresh pending so a transient database failure is retried.
				firstChange = now
				lastChange = now
				continue
			}
			if observer != nil {
				if err := observer(s.Current(), s.Status()); err != nil {
					return err
				}
			}
			firstChange = time.Time{}
			lastChange = time.Time{}
		}
	}
}

type fileSetStamp struct {
	Parent   fileStamp
	Database fileStamp
	WAL      fileStamp
	SHM      fileStamp
}

type fileStamp struct {
	Exists  bool
	Size    int64
	Mode    os.FileMode
	ModTime int64
}

func inspectFiles(databasePath string) (fileSetStamp, error) {
	parent, err := inspectFile(filepath.Dir(databasePath))
	if err != nil {
		return fileSetStamp{}, fmt.Errorf("inspect database directory: %w", err)
	}
	database, err := inspectFile(databasePath)
	if err != nil {
		return fileSetStamp{}, fmt.Errorf("inspect database: %w", err)
	}
	wal, err := inspectFile(databasePath + "-wal")
	if err != nil {
		return fileSetStamp{}, fmt.Errorf("inspect database WAL: %w", err)
	}
	shm, err := inspectFile(databasePath + "-shm")
	if err != nil {
		return fileSetStamp{}, fmt.Errorf("inspect database SHM: %w", err)
	}
	// Opening a WAL database for a read-only transaction can update the SHM
	// timestamp. WAL or database changes carry the durable data signal, while
	// SHM creation, removal, or resizing is still useful as a lifecycle hint.
	shm.ModTime = 0
	return fileSetStamp{Parent: parent, Database: database, WAL: wal, SHM: shm}, nil
}

func inspectFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileStamp{}, nil
		}
		return fileStamp{}, err
	}
	return fileStamp{
		Exists:  true,
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime().UnixNano(),
	}, nil
}
