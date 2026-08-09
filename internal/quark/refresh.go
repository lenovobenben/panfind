package quark

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const maxAuthenticationExpirationsWithoutProgress = 3

type AuthorizationNotice struct {
	AccountID       namespace.AccountID
	PromptOpened    bool
	Reauthorization bool
}

type authorizationProvider interface {
	begin(context.Context) (pendingRefreshAuthorization, error)
}

type pendingRefreshAuthorization interface {
	accountID() namespace.AccountID
	promptIsOpen() bool
	wait(context.Context) (refreshSession, error)
}

type refreshSession interface {
	directoryClient() (directoryClient, error)
	close()
}

type desktopAuthorizationProvider struct {
	client *http.Client
}

type refreshRunner struct {
	store         *store
	authorization authorizationProvider
	pageSize      int
	retryPolicy   directoryRetryPolicy
}

func newDesktopAuthorizationProvider(client *http.Client) (*desktopAuthorizationProvider, error) {
	if client == nil {
		return nil, errors.New("Quark desktop authorization HTTP client is nil")
	}
	return &desktopAuthorizationProvider{client: client}, nil
}

func (provider *desktopAuthorizationProvider) begin(ctx context.Context) (pendingRefreshAuthorization, error) {
	bridge, err := discoverDesktopBridge(ctx, provider.client)
	if err != nil {
		return nil, err
	}
	authorization, err := bridge.beginAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	return authorization, nil
}

func (authorization *pendingDesktopAuthorization) accountID() namespace.AccountID {
	if authorization == nil || authorization.bridge == nil {
		return ""
	}
	return authorization.bridge.info.AccountID
}

func (authorization *pendingDesktopAuthorization) promptIsOpen() bool {
	return authorization != nil && authorization.promptOpened
}

func (authorization *pendingDesktopAuthorization) wait(ctx context.Context) (refreshSession, error) {
	return authorization.waitForSession(ctx)
}

func (session *desktopSession) directoryClient() (directoryClient, error) {
	client, err := session.httpClient()
	if err != nil {
		return nil, err
	}
	return newHTTPDirectoryClient(client)
}

func newRefreshRunner(store *store, authorization authorizationProvider, pageSize int) (*refreshRunner, error) {
	if store == nil {
		return nil, errors.New("Quark refresh store is nil")
	}
	if authorization == nil {
		return nil, errors.New("Quark authorization provider is nil")
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return &refreshRunner{
		store: store, authorization: authorization, pageSize: pageSize, retryPolicy: defaultDirectoryRetryPolicy(),
	}, nil
}

// run obtains short-lived desktop sessions until the current account's
// staging generation is published or cannot make bounded progress.
func (runner *refreshRunner) run(ctx context.Context, observe func(AuthorizationNotice)) (*namespace.Snapshot, error) {
	var accountID namespace.AccountID
	var runID int64
	noProgressExpirations := 0
	for {
		authorization, err := runner.authorization.begin(ctx)
		if err != nil {
			return nil, runner.fail(ctx, runID, fmt.Errorf("begin Quark desktop authorization: %w", err))
		}
		if authorization == nil {
			return nil, runner.fail(ctx, runID, errors.New("Quark authorization provider returned no authorization"))
		}
		candidateAccountID := authorization.accountID()
		if candidateAccountID == "" {
			return nil, runner.fail(ctx, runID, errors.New("Quark desktop authorization has an empty account ID"))
		}
		if accountID != "" && candidateAccountID != accountID {
			return nil, runner.fail(ctx, runID, errors.New("Quark desktop account changed during refresh"))
		}
		if observe != nil {
			observe(AuthorizationNotice{
				AccountID: candidateAccountID, PromptOpened: authorization.promptIsOpen(), Reauthorization: runID != 0,
			})
		}

		session, err := authorization.wait(ctx)
		if err != nil {
			return nil, runner.fail(ctx, runID, fmt.Errorf("complete Quark desktop authorization: %w", err))
		}
		if session == nil {
			return nil, runner.fail(ctx, runID, errors.New("Quark desktop authorization returned no session"))
		}
		if runID == 0 {
			accountID = candidateAccountID
			runID, err = runner.resumeOrBeginGeneration(ctx, accountID)
			if err != nil {
				session.close()
				return nil, err
			}
		}

		before, err := runner.store.crawlProgress(ctx, runID)
		if err != nil {
			session.close()
			return nil, runner.fail(ctx, runID, err)
		}
		snapshot, refreshErr := runner.runSession(ctx, runID, session)
		if refreshErr == nil {
			return snapshot, nil
		}
		if !errors.Is(refreshErr, errQuarkAuthenticationExpired) {
			return nil, runner.fail(ctx, runID, refreshErr)
		}
		if err := runner.recordFailure(ctx, runID, refreshErr); err != nil {
			return nil, errors.Join(refreshErr, err)
		}
		after, err := runner.store.crawlProgress(ctx, runID)
		if err != nil {
			return nil, runner.fail(ctx, runID, errors.Join(refreshErr, err))
		}
		if after == before {
			noProgressExpirations++
		} else {
			noProgressExpirations = 0
		}
		if noProgressExpirations >= maxAuthenticationExpirationsWithoutProgress {
			boundedErr := fmt.Errorf(
				"Quark authentication expired in %d consecutive sessions without committed scan progress: %w",
				noProgressExpirations, refreshErr,
			)
			return nil, runner.fail(ctx, runID, boundedErr)
		}
	}
}

func (runner *refreshRunner) resumeOrBeginGeneration(ctx context.Context, accountID namespace.AccountID) (int64, error) {
	runID, exists, err := runner.store.stagingGeneration(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if exists {
		return runID, nil
	}
	return runner.store.beginGeneration(ctx, accountID)
}

func (runner *refreshRunner) runSession(ctx context.Context, runID int64, session refreshSession) (*namespace.Snapshot, error) {
	defer session.close()
	if err := runner.store.markGenerationAttempt(ctx, runID); err != nil {
		return nil, err
	}
	client, err := session.directoryClient()
	if err != nil {
		return nil, fmt.Errorf("create authenticated Quark directory client: %w", err)
	}
	retryingClient, err := newRetryingDirectoryClient(client, runner.retryPolicy)
	if err != nil {
		return nil, err
	}
	scanner, err := newScanner(runner.store, retryingClient, runner.pageSize)
	if err != nil {
		return nil, err
	}
	return scanner.run(ctx, runID)
}

func (runner *refreshRunner) recordFailure(ctx context.Context, runID int64, refreshErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return runner.store.recordGenerationFailure(cleanupCtx, runID, refreshErr)
}

func (runner *refreshRunner) fail(ctx context.Context, runID int64, refreshErr error) error {
	if runID == 0 {
		return refreshErr
	}
	if err := runner.recordFailure(ctx, runID, refreshErr); err != nil {
		return errors.Join(refreshErr, err)
	}
	return refreshErr
}

var (
	_ authorizationProvider       = (*desktopAuthorizationProvider)(nil)
	_ pendingRefreshAuthorization = (*pendingDesktopAuthorization)(nil)
	_ refreshSession              = (*desktopSession)(nil)
)
