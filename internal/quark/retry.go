package quark

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultDirectoryAttempts     = 4
	defaultDirectoryInitialDelay = 250 * time.Millisecond
	defaultDirectoryMaximumDelay = 30 * time.Second
)

var errQuarkTransient = errors.New("transient Quark directory request failure")

type transientDirectoryError struct {
	err        error
	retryAfter time.Duration
}

func (err *transientDirectoryError) Error() string {
	return err.err.Error()
}

func (err *transientDirectoryError) Unwrap() error {
	return err.err
}

func (err *transientDirectoryError) Is(target error) bool {
	return target == errQuarkTransient || errors.Is(err.err, target)
}

func newTransientDirectoryError(err error, retryAfter time.Duration) error {
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &transientDirectoryError{err: err, retryAfter: retryAfter}
}

type directoryRetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaximumDelay time.Duration
	Sleep        func(context.Context, time.Duration) error
}

func defaultDirectoryRetryPolicy() directoryRetryPolicy {
	return directoryRetryPolicy{
		MaxAttempts: defaultDirectoryAttempts, InitialDelay: defaultDirectoryInitialDelay,
		MaximumDelay: defaultDirectoryMaximumDelay, Sleep: sleepContext,
	}
}

type retryingDirectoryClient struct {
	client directoryClient
	policy directoryRetryPolicy
}

func newRetryingDirectoryClient(client directoryClient, policy directoryRetryPolicy) (*retryingDirectoryClient, error) {
	if client == nil {
		return nil, errors.New("Quark retrying directory client is nil")
	}
	if policy.MaxAttempts <= 0 {
		return nil, errors.New("Quark directory retry attempts must be positive")
	}
	if policy.InitialDelay <= 0 || policy.MaximumDelay < policy.InitialDelay {
		return nil, errors.New("Quark directory retry delays are invalid")
	}
	if policy.Sleep == nil {
		return nil, errors.New("Quark directory retry sleeper is nil")
	}
	return &retryingDirectoryClient{client: client, policy: policy}, nil
}

func (client *retryingDirectoryClient) ListDirectory(ctx context.Context, request listDirectoryRequest) ([]remoteNode, error) {
	delay := client.policy.InitialDelay
	for attempt := 1; ; attempt++ {
		nodes, err := client.client.ListDirectory(ctx, request)
		if err == nil {
			return nodes, nil
		}
		if !errors.Is(err, errQuarkTransient) {
			return nil, err
		}
		if attempt >= client.policy.MaxAttempts {
			return nil, fmt.Errorf("Quark directory request failed after %d attempts: %w", attempt, err)
		}

		wait := delay
		var transient *transientDirectoryError
		if errors.As(err, &transient) && transient.retryAfter > wait {
			wait = transient.retryAfter
		}
		if wait > client.policy.MaximumDelay {
			return nil, fmt.Errorf(
				"Quark directory retry delay %s exceeds limit %s: %w",
				wait, client.policy.MaximumDelay, err,
			)
		}
		if err := client.policy.Sleep(ctx, wait); err != nil {
			return nil, err
		}
		if delay <= client.policy.MaximumDelay/2 {
			delay *= 2
		} else {
			delay = client.policy.MaximumDelay
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ directoryClient = (*retryingDirectoryClient)(nil)
