package quark

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type fakeAuthorizationProvider struct {
	authorizations []*fakePendingAuthorization
	calls          int
}

func (provider *fakeAuthorizationProvider) begin(context.Context) (pendingRefreshAuthorization, error) {
	if provider.calls >= len(provider.authorizations) {
		return nil, errors.New("unexpected authorization request")
	}
	result := provider.authorizations[provider.calls]
	provider.calls++
	return result, nil
}

type fakePendingAuthorization struct {
	id      namespace.AccountID
	prompt  bool
	session refreshSession
	err     error
}

func (authorization *fakePendingAuthorization) accountID() namespace.AccountID {
	return authorization.id
}

func (authorization *fakePendingAuthorization) promptIsOpen() bool {
	return authorization.prompt
}

func (authorization *fakePendingAuthorization) wait(context.Context) (refreshSession, error) {
	return authorization.session, authorization.err
}

type fakeRefreshSession struct {
	client directoryClient
	err    error
	closed bool
}

func (session *fakeRefreshSession) directoryClient() (directoryClient, error) {
	return session.client, session.err
}

func (session *fakeRefreshSession) close() {
	session.closed = true
}

func TestRefreshRunnerAuthenticatesScansAndClosesSession(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 2}: {nodes: []remoteNode{{
			ID: "file", ParentID: rootRemoteID, Name: "file.txt", Kind: namespace.NodeKindFile,
		}}},
	}}
	session := &fakeRefreshSession{client: client}
	authorization := &fakePendingAuthorization{id: "account-1", prompt: true, session: session}
	runner, err := newRefreshRunner(store, &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{authorization}}, 2)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}

	var notice AuthorizationNotice
	snapshot, err := runner.run(ctx, func(value AuthorizationNotice) {
		notice = value
	})
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if notice.AccountID != "account-1" || !notice.PromptOpened {
		t.Fatalf("authorization notice = %+v", notice)
	}
	if !session.closed {
		t.Fatal("authenticated session was not closed")
	}
	if _, exists := snapshot.Lookup("/file.txt"); !exists {
		t.Fatal("published snapshot does not contain scanned file")
	}
	if _, exists, err := store.stagingGeneration(ctx, "account-1"); err != nil || exists {
		t.Fatalf("stagingGeneration() after publish = exists %t, error %v", exists, err)
	}
}

func TestRefreshRunnerResumesCheckpointAfterAuthenticationExpires(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	firstClient := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {nodes: []remoteNode{{
			ID: "file", ParentID: rootRemoteID, Name: "file.txt", Kind: namespace.NodeKindFile,
		}}},
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {err: errQuarkAuthenticationExpired},
	}}
	firstSession := &fakeRefreshSession{client: firstClient}
	firstRunner, err := newRefreshRunner(store, &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", session: firstSession,
	}}}, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	if _, err := firstRunner.run(ctx, nil); !errors.Is(err, errQuarkAuthenticationExpired) {
		t.Fatalf("first run() error = %v", err)
	}
	if !firstSession.closed {
		t.Fatal("failed refresh did not close its session")
	}
	runID, exists, err := store.stagingGeneration(ctx, "account-1")
	if err != nil || !exists {
		t.Fatalf("stagingGeneration() after failure = (%d, %t, %v)", runID, exists, err)
	}
	page := mustNextCrawlPage(t, store, runID)
	if page.Number != 2 {
		t.Fatalf("checkpoint after authentication expiry = %+v", page)
	}
	failedStatus, err := store.refreshStatus(ctx, "account-1")
	if err != nil {
		t.Fatalf("refreshStatus() after failure error: %v", err)
	}
	if failedStatus.State != RefreshStateFailed || failedStatus.StagingGeneration != runID ||
		failedStatus.StartedAt == nil || failedStatus.LastAttemptAt == nil || failedStatus.LastProgressAt == nil ||
		!strings.Contains(failedStatus.LastError, errQuarkAuthenticationExpired.Error()) ||
		failedStatus.DirectoriesDiscovered != 1 || failedStatus.DirectoriesCompleted != 0 ||
		failedStatus.DirectoriesPending != 1 || failedStatus.StagedNodes != 1 {
		t.Fatalf("refreshStatus() after failure = %+v", failedStatus)
	}

	secondClient := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {},
	}}
	secondSession := &fakeRefreshSession{client: secondClient}
	secondRunner, err := newRefreshRunner(store, &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", session: secondSession,
	}}}, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() for resume error: %v", err)
	}
	snapshot, err := secondRunner.run(ctx, nil)
	if err != nil {
		t.Fatalf("resumed run() error: %v", err)
	}
	if snapshot.Generation() != uint64(runID) {
		t.Fatalf("resumed generation = %d, want %d", snapshot.Generation(), runID)
	}
	if _, exists := snapshot.Lookup("/file.txt"); !exists {
		t.Fatal("resumed snapshot lost the first committed page")
	}
	if !secondSession.closed {
		t.Fatal("resumed refresh did not close its session")
	}
	readyStatus, err := store.refreshStatus(ctx, "account-1")
	if err != nil {
		t.Fatalf("refreshStatus() after publish error: %v", err)
	}
	if readyStatus.State != RefreshStateReady || readyStatus.PublishedGeneration != runID ||
		readyStatus.SnapshotUpdatedAt == nil || readyStatus.StagingGeneration != 0 || readyStatus.LastError != "" {
		t.Fatalf("refreshStatus() after publish = %+v", readyStatus)
	}
}

func TestRefreshRunnerDoesNotCreateGenerationBeforeAuthorization(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	authorizationError := errors.New("authorization denied")
	runner, err := newRefreshRunner(store, &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", err: authorizationError,
	}}}, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	if _, err := runner.run(ctx, nil); !errors.Is(err, authorizationError) {
		t.Fatalf("run() error = %v", err)
	}
	if _, exists, err := store.stagingGeneration(ctx, "account-1"); err != nil || exists {
		t.Fatalf("stagingGeneration() after authorization failure = exists %t, error %v", exists, err)
	}
}
