package quark

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type fakeAuthorizationProvider struct {
	authorizations []*fakePendingAuthorization
	calls          int
	onBegin        func(int)
}

func (provider *fakeAuthorizationProvider) begin(context.Context) (pendingRefreshAuthorization, error) {
	if provider.onBegin != nil {
		provider.onBegin(provider.calls)
	}
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

type cancelingDirectoryClient struct {
	cancel context.CancelFunc
}

func (client *cancelingDirectoryClient) ListDirectory(ctx context.Context, _ listDirectoryRequest) ([]remoteNode, error) {
	client.cancel()
	return nil, ctx.Err()
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

func TestRefreshRunnerRetriesTransientPageWithoutReauthorization(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	client := &sequenceDirectoryClient{responses: []retryResponse{
		{err: newTransientDirectoryError(errors.New("temporary timeout"), 0)},
		{},
	}}
	session := &fakeRefreshSession{client: client}
	provider := &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", session: session,
	}}}
	runner, err := newRefreshRunner(store, provider, 2)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	var sleeps []time.Duration
	runner.retryPolicy.Sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}
	snapshot, err := runner.run(ctx, nil)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if snapshot.Generation() == 0 || provider.calls != 1 || client.calls != 2 ||
		len(sleeps) != 1 || sleeps[0] != defaultDirectoryInitialDelay {
		t.Fatalf("generation = %d, authorizations = %d, requests = %d, sleeps = %v",
			snapshot.Generation(), provider.calls, client.calls, sleeps)
	}
	if !session.closed {
		t.Fatal("refresh did not close its session")
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
	secondClient := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {},
	}}
	secondSession := &fakeRefreshSession{client: secondClient}
	provider := &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{
		{id: "account-1", session: firstSession},
		{id: "account-1", prompt: true, session: secondSession},
	}}
	var runID int64
	provider.onBegin = func(call int) {
		if call != 1 {
			return
		}
		if !firstSession.closed {
			t.Fatal("expired session was not closed before reauthorization")
		}
		var exists bool
		var err error
		runID, exists, err = store.stagingGeneration(ctx, "account-1")
		if err != nil || !exists {
			t.Fatalf("stagingGeneration() before reauthorization = (%d, %t, %v)", runID, exists, err)
		}
		page := mustNextCrawlPage(t, store, runID)
		if page.Number != 2 {
			t.Fatalf("checkpoint before reauthorization = %+v", page)
		}
		failedStatus, err := store.refreshStatus(ctx, "account-1")
		if err != nil {
			t.Fatalf("refreshStatus() before reauthorization error: %v", err)
		}
		if failedStatus.State != RefreshStateFailed || failedStatus.StagingGeneration != runID ||
			failedStatus.StartedAt == nil || failedStatus.LastAttemptAt == nil || failedStatus.LastProgressAt == nil ||
			!strings.Contains(failedStatus.LastError, errQuarkAuthenticationExpired.Error()) ||
			failedStatus.DirectoriesDiscovered != 1 || failedStatus.DirectoriesCompleted != 0 ||
			failedStatus.DirectoriesPending != 1 || failedStatus.StagedNodes != 1 {
			t.Fatalf("refreshStatus() before reauthorization = %+v", failedStatus)
		}
	}
	runner, err := newRefreshRunner(store, provider, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	var notices []AuthorizationNotice
	snapshot, err := runner.run(ctx, func(notice AuthorizationNotice) {
		notices = append(notices, notice)
	})
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if provider.calls != 2 || len(notices) != 2 || notices[0].Reauthorization || !notices[1].Reauthorization ||
		!notices[1].PromptOpened {
		t.Fatalf("authorization calls = %d, notices = %+v", provider.calls, notices)
	}
	if snapshot.Generation() != uint64(runID) {
		t.Fatalf("published generation = %d, want %d", snapshot.Generation(), runID)
	}
	if _, exists := snapshot.Lookup("/file.txt"); !exists {
		t.Fatal("published snapshot lost the first committed page")
	}
	if !secondSession.closed {
		t.Fatal("replacement session was not closed")
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

func TestRefreshRunnerBoundsAuthenticationExpiryWithoutProgress(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	provider := &fakeAuthorizationProvider{}
	sessions := make([]*fakeRefreshSession, maxAuthenticationExpirationsWithoutProgress)
	for index := range sessions {
		client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
			{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {err: errQuarkAuthenticationExpired},
		}}
		sessions[index] = &fakeRefreshSession{client: client}
		provider.authorizations = append(provider.authorizations, &fakePendingAuthorization{
			id: "account-1", session: sessions[index],
		})
	}
	runner, err := newRefreshRunner(store, provider, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	var notices []AuthorizationNotice
	_, err = runner.run(ctx, func(notice AuthorizationNotice) {
		notices = append(notices, notice)
	})
	if !errors.Is(err, errQuarkAuthenticationExpired) ||
		!strings.Contains(err.Error(), "3 consecutive sessions without committed scan progress") {
		t.Fatalf("run() error = %v", err)
	}
	if provider.calls != maxAuthenticationExpirationsWithoutProgress || len(notices) != provider.calls {
		t.Fatalf("authorization calls = %d, notices = %+v", provider.calls, notices)
	}
	for index, session := range sessions {
		if !session.closed {
			t.Errorf("session %d was not closed", index)
		}
	}
	if notices[0].Reauthorization || !notices[1].Reauthorization || !notices[2].Reauthorization {
		t.Fatalf("reauthorization notices = %+v", notices)
	}
	status, err := store.refreshStatus(ctx, "account-1")
	if err != nil {
		t.Fatalf("refreshStatus() error: %v", err)
	}
	if status.State != RefreshStateFailed ||
		!strings.Contains(status.LastError, "3 consecutive sessions without committed scan progress") {
		t.Fatalf("refreshStatus() = %+v", status)
	}
}

func TestRefreshRunnerResetsExpiryBoundAfterCommittedProgress(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	responses := []map[listDirectoryRequest]directoryResponse{
		{{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {err: errQuarkAuthenticationExpired}},
		{
			{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {nodes: []remoteNode{{
				ID: "file", ParentID: rootRemoteID, Name: "file.txt", Kind: namespace.NodeKindFile,
			}}},
			{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {err: errQuarkAuthenticationExpired},
		},
		{{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {err: errQuarkAuthenticationExpired}},
		{{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {err: errQuarkAuthenticationExpired}},
		{{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {}},
	}
	provider := &fakeAuthorizationProvider{}
	for _, script := range responses {
		provider.authorizations = append(provider.authorizations, &fakePendingAuthorization{
			id: "account-1", session: &fakeRefreshSession{client: &scriptedDirectoryClient{responses: script}},
		})
	}
	runner, err := newRefreshRunner(store, provider, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	snapshot, err := runner.run(ctx, nil)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if provider.calls != len(responses) {
		t.Fatalf("authorization calls = %d, want %d", provider.calls, len(responses))
	}
	if _, exists := snapshot.Lookup("/file.txt"); !exists {
		t.Fatal("published snapshot lost progress committed by an earlier session")
	}
}

func TestRefreshRunnerRejectsAccountChangeDuringReauthorization(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	firstSession := &fakeRefreshSession{client: &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {err: errQuarkAuthenticationExpired},
	}}}
	foreignSession := &fakeRefreshSession{client: &scriptedDirectoryClient{}}
	provider := &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{
		{id: "account-1", session: firstSession},
		{id: "account-2", session: foreignSession},
	}}
	runner, err := newRefreshRunner(store, provider, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	_, err = runner.run(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "account changed during refresh") {
		t.Fatalf("run() error = %v", err)
	}
	if !firstSession.closed {
		t.Fatal("expired session was not closed")
	}
	if foreignSession.closed {
		t.Fatal("session for the foreign account was unexpectedly opened")
	}
	if _, exists, err := store.stagingGeneration(ctx, "account-1"); err != nil || !exists {
		t.Fatalf("account-1 staging generation = exists %t, error %v", exists, err)
	}
	if _, exists, err := store.stagingGeneration(ctx, "account-2"); err != nil || exists {
		t.Fatalf("account-2 staging generation = exists %t, error %v", exists, err)
	}
	status, err := store.refreshStatus(ctx, "account-1")
	if err != nil {
		t.Fatalf("refreshStatus() error: %v", err)
	}
	if status.State != RefreshStateFailed || !strings.Contains(status.LastError, "account changed during refresh") {
		t.Fatalf("refreshStatus() = %+v", status)
	}
}

func TestRefreshRunnerClosesSessionAndRecordsCanceledScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	session := &fakeRefreshSession{client: &cancelingDirectoryClient{cancel: cancel}}
	runner, err := newRefreshRunner(store, &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", session: session,
	}}}, 1)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}
	if _, err := runner.run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v", err)
	}
	if !session.closed {
		t.Fatal("canceled refresh did not close its session")
	}
	status, err := store.refreshStatus(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("refreshStatus() error: %v", err)
	}
	if status.State != RefreshStateFailed || !strings.Contains(status.LastError, context.Canceled.Error()) {
		t.Fatalf("refreshStatus() = %+v", status)
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
