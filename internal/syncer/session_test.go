package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

type fakeLoader struct {
	mu     sync.Mutex
	calls  []uint64
	fail   bool
	loaded chan uint64
	onLoad func(uint64)
}

func (loader *fakeLoader) LoadSnapshot(_ context.Context, account provider.Account, generation uint64) (*namespace.Snapshot, error) {
	loader.mu.Lock()
	loader.calls = append(loader.calls, generation)
	fail := loader.fail
	onLoad := loader.onLoad
	loader.mu.Unlock()
	if onLoad != nil {
		onLoad(generation)
	}
	if loader.loaded != nil {
		loader.loaded <- generation
	}
	if fail {
		return nil, errors.New("temporary read failure")
	}
	root := namespace.NodeKey{Provider: account.Provider, Account: account.ID, ID: 1}
	return namespace.NewSnapshot(generation, root, []namespace.Node{{
		Key: root, Name: "/", Kind: namespace.NodeKindDirectory,
	}})
}

func (loader *fakeLoader) setFail(fail bool) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.fail = fail
}

func testAccount(databasePath string) provider.Account {
	return provider.Account{
		Provider:     "test",
		ID:           "account",
		DatabasePath: databasePath,
	}
}

func TestRefreshPublishesCompleteGenerations(t *testing.T) {
	loader := &fakeLoader{}
	session := New(loader, testAccount("unused"))

	if err := session.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := session.Current()
	if first == nil || first.Generation() != 1 {
		t.Fatalf("first generation = %v", first)
	}

	if err := session.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := session.Current()
	if second == nil || second.Generation() != 2 || second == first {
		t.Fatalf("second generation = %v", second)
	}
	status := session.Status()
	if status.State != StateReady || status.Generation != 2 || status.LastSuccess.IsZero() || status.LastError != "" {
		t.Fatalf("status after refresh = %+v", status)
	}
}

func TestFailedRefreshRetainsPreviousSnapshot(t *testing.T) {
	loader := &fakeLoader{}
	session := New(loader, testAccount("unused"))
	if err := session.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous := session.Current()

	loader.setFail(true)
	if err := session.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded, want error")
	}
	if current := session.Current(); current != previous {
		t.Fatal("failed refresh replaced the published snapshot")
	}
	status := session.Status()
	if status.State != StateStale || status.Generation != 1 || status.LastError == "" {
		t.Fatalf("status after failed refresh = %+v", status)
	}

	loader.setFail(false)
	if err := session.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if generation := session.Current().Generation(); generation != 2 {
		t.Fatalf("generation after recovery = %d, want 2", generation)
	}
}

func TestRunDebouncesDatabaseChanges(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "filecache.db")
	if err := os.WriteFile(databasePath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	loader := &fakeLoader{loaded: make(chan uint64, 4)}
	session := New(loader, testAccount(databasePath))
	observed := make(chan uint64, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, WatchOptions{
			PollInterval: 5 * time.Millisecond,
			Debounce:     100 * time.Millisecond,
			MaxDelay:     300 * time.Millisecond,
		}, func(snapshot *namespace.Snapshot, _ Status) error {
			observed <- snapshot.Generation()
			return nil
		})
	}()

	waitGeneration(t, loader.loaded, 1)
	waitGeneration(t, observed, 1)
	if err := os.WriteFile(databasePath+"-wal", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath+"-wal", []byte("two-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitGeneration(t, loader.loaded, 2)
	waitGeneration(t, observed, 2)

	select {
	case generation := <-loader.loaded:
		t.Fatalf("burst caused an extra refresh generation %d", generation)
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context cancellation", err)
	}
}

func TestRunRetriesFailedRefreshWithoutDiscardingSnapshot(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "filecache.db")
	if err := os.WriteFile(databasePath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	loader := &fakeLoader{loaded: make(chan uint64, 4)}
	session := New(loader, testAccount(databasePath))
	observed := make(chan Status, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, WatchOptions{
			PollInterval: 5 * time.Millisecond,
			Debounce:     10 * time.Millisecond,
			MaxDelay:     30 * time.Millisecond,
		}, func(_ *namespace.Snapshot, status Status) error {
			observed <- status
			return nil
		})
	}()

	waitGeneration(t, loader.loaded, 1)
	waitState(t, observed, StateReady)
	waitFor(t, time.Second, func() bool {
		current := session.Current()
		return current != nil && current.Generation() == 1
	})
	time.Sleep(20 * time.Millisecond)
	previous := session.Current()
	loader.setFail(true)
	if err := os.WriteFile(databasePath, []byte("changed-and-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitGeneration(t, loader.loaded, 2)
	waitState(t, observed, StateStale)
	if current := session.Current(); current != previous {
		t.Fatal("failed watched refresh replaced the published snapshot")
	}

	loader.setFail(false)
	waitGeneration(t, loader.loaded, 2)
	waitState(t, observed, StateReady)
	waitFor(t, time.Second, func() bool {
		current := session.Current()
		return current != nil && current.Generation() == 2 && session.Status().State == StateReady
	})

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context cancellation", err)
	}
}

func TestRunNoticesChangeDuringInitialRefresh(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "filecache.db")
	if err := os.WriteFile(databasePath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	loader := &fakeLoader{loaded: make(chan uint64, 4)}
	loader.onLoad = func(generation uint64) {
		if generation == 1 {
			if err := os.WriteFile(databasePath, []byte("changed-during-load"), 0o600); err != nil {
				t.Errorf("change database during load: %v", err)
			}
		}
	}
	session := New(loader, testAccount(databasePath))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, WatchOptions{
			PollInterval: 5 * time.Millisecond,
			Debounce:     10 * time.Millisecond,
			MaxDelay:     30 * time.Millisecond,
		}, nil)
	}()

	waitGeneration(t, loader.loaded, 1)
	waitGeneration(t, loader.loaded, 2)
	waitFor(t, time.Second, func() bool {
		current := session.Current()
		return current != nil && current.Generation() == 2
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context cancellation", err)
	}
}

func TestInspectFilesIgnoresSHMTimeOnlyChanges(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "filecache.db")
	shmPath := databasePath + "-shm"
	if err := os.WriteFile(databasePath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shmPath, []byte("shared-memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := inspectFiles(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	changedTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(shmPath, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	after, err := inspectFiles(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("SHM timestamp changed the watched signature: before=%+v after=%+v", before, after)
	}
}

func waitGeneration(t *testing.T, loaded <-chan uint64, want uint64) {
	t.Helper()
	select {
	case got := <-loaded:
		if got != want {
			t.Fatalf("loaded generation %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for generation %d", want)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func waitState(t *testing.T, observed <-chan Status, want State) {
	t.Helper()
	select {
	case status := <-observed:
		if status.State != want {
			t.Fatalf("observed state %q, want %q", status.State, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for state %q", want)
	}
}
