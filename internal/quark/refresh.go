package quark

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type AuthorizationNotice struct {
	AccountID    namespace.AccountID
	PromptOpened bool
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
	return &refreshRunner{store: store, authorization: authorization, pageSize: pageSize}, nil
}

// run obtains a one-scan desktop session, resumes the current account's
// staging generation when present, and closes the session on every exit path.
func (runner *refreshRunner) run(ctx context.Context, observe func(AuthorizationNotice)) (*namespace.Snapshot, error) {
	authorization, err := runner.authorization.begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin Quark desktop authorization: %w", err)
	}
	if authorization == nil {
		return nil, errors.New("Quark authorization provider returned no authorization")
	}
	accountID := authorization.accountID()
	if accountID == "" {
		return nil, errors.New("Quark desktop authorization has an empty account ID")
	}
	if observe != nil {
		observe(AuthorizationNotice{AccountID: accountID, PromptOpened: authorization.promptIsOpen()})
	}

	session, err := authorization.wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("complete Quark desktop authorization: %w", err)
	}
	if session == nil {
		return nil, errors.New("Quark desktop authorization returned no session")
	}
	defer session.close()
	client, err := session.directoryClient()
	if err != nil {
		return nil, fmt.Errorf("create authenticated Quark directory client: %w", err)
	}

	runID, exists, err := runner.store.stagingGeneration(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !exists {
		runID, err = runner.store.beginGeneration(ctx, accountID)
		if err != nil {
			return nil, err
		}
	}
	scanner, err := newScanner(runner.store, client, runner.pageSize)
	if err != nil {
		return nil, err
	}
	return scanner.run(ctx, runID)
}

var (
	_ authorizationProvider       = (*desktopAuthorizationProvider)(nil)
	_ pendingRefreshAuthorization = (*pendingDesktopAuthorization)(nil)
	_ refreshSession              = (*desktopSession)(nil)
)
