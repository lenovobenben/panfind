package quark

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type retryResponse struct {
	nodes []remoteNode
	err   error
}

type sequenceDirectoryClient struct {
	responses []retryResponse
	calls     int
}

func (client *sequenceDirectoryClient) ListDirectory(context.Context, listDirectoryRequest) ([]remoteNode, error) {
	if client.calls >= len(client.responses) {
		return nil, errors.New("unexpected directory request")
	}
	response := client.responses[client.calls]
	client.calls++
	return response.nodes, response.err
}

func TestRetryingDirectoryClientUsesExponentialBackoff(t *testing.T) {
	transient := newTransientDirectoryError(errors.New("temporary failure"), 0)
	base := &sequenceDirectoryClient{responses: []retryResponse{
		{err: transient}, {err: transient}, {nodes: []remoteNode{{ID: "file"}}},
	}}
	var sleeps []time.Duration
	policy := defaultDirectoryRetryPolicy()
	policy.Sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}
	client, err := newRetryingDirectoryClient(base, policy)
	if err != nil {
		t.Fatalf("newRetryingDirectoryClient() error: %v", err)
	}
	nodes, err := client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1})
	if err != nil {
		t.Fatalf("ListDirectory() error: %v", err)
	}
	if base.calls != 3 || len(nodes) != 1 || len(sleeps) != 2 ||
		sleeps[0] != 250*time.Millisecond || sleeps[1] != 500*time.Millisecond {
		t.Fatalf("calls = %d, nodes = %+v, sleeps = %v", base.calls, nodes, sleeps)
	}
}

func TestRetryingDirectoryClientHonorsRetryAfter(t *testing.T) {
	base := &sequenceDirectoryClient{responses: []retryResponse{
		{err: newTransientDirectoryError(errors.New("rate limited"), 2*time.Second)},
		{},
	}}
	var sleeps []time.Duration
	policy := defaultDirectoryRetryPolicy()
	policy.Sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}
	client, err := newRetryingDirectoryClient(base, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1}); err != nil {
		t.Fatalf("ListDirectory() error: %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("retry sleeps = %v", sleeps)
	}
}

func TestRetryingDirectoryClientBoundsAttemptsAndDelay(t *testing.T) {
	transient := newTransientDirectoryError(errors.New("temporary failure"), 0)
	base := &sequenceDirectoryClient{responses: []retryResponse{
		{err: transient}, {err: transient}, {err: transient}, {err: transient},
	}}
	policy := defaultDirectoryRetryPolicy()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	client, err := newRetryingDirectoryClient(base, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1})
	if !errors.Is(err, errQuarkTransient) || !strings.Contains(err.Error(), "after 4 attempts") || base.calls != 4 {
		t.Fatalf("ListDirectory() calls = %d, error = %v", base.calls, err)
	}

	base = &sequenceDirectoryClient{responses: []retryResponse{{
		err: newTransientDirectoryError(errors.New("long rate limit"), 31*time.Second),
	}}}
	client, err = newRetryingDirectoryClient(base, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1})
	if !errors.Is(err, errQuarkTransient) || !strings.Contains(err.Error(), "exceeds limit") || base.calls != 1 {
		t.Fatalf("ListDirectory() long delay calls = %d, error = %v", base.calls, err)
	}
}

func TestRetryingDirectoryClientDoesNotRetryPermanentOrAuthenticationErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "permanent", err: errors.New("invalid response")},
		{name: "authentication", err: errQuarkAuthenticationExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &sequenceDirectoryClient{responses: []retryResponse{{err: test.err}}}
			policy := defaultDirectoryRetryPolicy()
			policy.Sleep = func(context.Context, time.Duration) error {
				t.Fatal("unexpected retry sleep")
				return nil
			}
			client, err := newRetryingDirectoryClient(base, policy)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1})
			if !errors.Is(err, test.err) || base.calls != 1 {
				t.Fatalf("ListDirectory() calls = %d, error = %v", base.calls, err)
			}
		})
	}
}

func TestRetryingDirectoryClientStopsWhenSleepIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &sequenceDirectoryClient{responses: []retryResponse{{
		err: newTransientDirectoryError(errors.New("temporary failure"), 0),
	}}}
	policy := defaultDirectoryRetryPolicy()
	policy.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return sleepContext(ctx, time.Hour)
	}
	client, err := newRetryingDirectoryClient(base, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListDirectory(ctx, listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1})
	if !errors.Is(err, context.Canceled) || base.calls != 1 {
		t.Fatalf("ListDirectory() calls = %d, error = %v", base.calls, err)
	}
}
