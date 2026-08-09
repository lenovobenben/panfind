package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type crawlPage struct {
	AccountID   namespace.AccountID
	DirectoryID int64
	RemoteID    string
	Number      int
}

type crawledNode struct {
	RemoteID string
	Node     namespace.Node
}

type crawlProgress struct {
	Nodes                int64
	Directories          int64
	CompletedDirectories int64
	NextPages            int64
}

func (s *store) crawlProgress(ctx context.Context, runID int64) (crawlProgress, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return crawlProgress{}, fmt.Errorf("begin Quark crawl progress read: %w", err)
	}
	defer tx.Rollback()
	if _, err := stagingAccount(ctx, tx, runID); err != nil {
		return crawlProgress{}, err
	}

	var progress crawlProgress
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(state = 'complete'), 0),
		       COALESCE(SUM(next_page), 0)
		FROM crawl_queue
		WHERE run_id = ?
	`, runID).Scan(&progress.Directories, &progress.CompletedDirectories, &progress.NextPages); err != nil {
		return crawlProgress{}, fmt.Errorf("read Quark crawl queue progress: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM nodes
		WHERE run_id = ?
	`, runID).Scan(&progress.Nodes); err != nil {
		return crawlProgress{}, fmt.Errorf("read Quark crawl node progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return crawlProgress{}, fmt.Errorf("commit Quark crawl progress read: %w", err)
	}
	return progress, nil
}

// nextCrawlPage returns the oldest directory that has not been fully scanned.
// A page remains pending until commitCrawlPage atomically advances it.
func (s *store) nextCrawlPage(ctx context.Context, runID int64) (crawlPage, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return crawlPage{}, false, fmt.Errorf("begin Quark crawl queue read: %w", err)
	}
	defer tx.Rollback()

	accountID, err := stagingAccount(ctx, tx, runID)
	if err != nil {
		return crawlPage{}, false, err
	}
	page := crawlPage{AccountID: accountID}
	if err := tx.QueryRowContext(ctx, `
		SELECT local_id, remote_id, next_page
		FROM crawl_queue
		WHERE run_id = ? AND state = 'pending'
		ORDER BY queue_id
		LIMIT 1
	`, runID).Scan(&page.DirectoryID, &page.RemoteID, &page.Number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return crawlPage{}, false, nil
		}
		return crawlPage{}, false, fmt.Errorf("read Quark crawl queue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return crawlPage{}, false, fmt.Errorf("commit Quark crawl queue read: %w", err)
	}
	return page, true, nil
}

// commitCrawlPage writes one remote response page, queues its child
// directories, and advances the page checkpoint in one transaction.
func (s *store) commitCrawlPage(ctx context.Context, runID int64, page crawlPage, nodes []crawledNode, complete bool) error {
	if page.RemoteID == "" || page.Number <= 0 {
		return fmt.Errorf("invalid Quark crawl page: %+v", page)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Quark crawl page transaction: %w", err)
	}
	defer tx.Rollback()

	accountID, err := stagingAccount(ctx, tx, runID)
	if err != nil {
		return err
	}
	if err := verifyPendingPage(ctx, tx, runID, page); err != nil {
		return err
	}
	parent := namespace.NodeKey{Provider: ProviderID, Account: accountID, ID: page.DirectoryID}
	for _, crawled := range nodes {
		if crawled.RemoteID == "" {
			return errors.New("Quark crawled node remote ID is empty")
		}
		if err := validateStagedNode(accountID, crawled.Node); err != nil {
			return err
		}
		if crawled.Node.Parent != parent {
			return fmt.Errorf("Quark node %d is not a child of crawl directory %d", crawled.Node.Key.ID, page.DirectoryID)
		}
		if err := verifyRemoteID(ctx, tx, accountID, crawled); err != nil {
			return err
		}
		if err := insertCrawledNode(ctx, tx, runID, crawled.Node); err != nil {
			return err
		}
		if crawled.Node.Kind == namespace.NodeKindDirectory {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO crawl_queue(run_id, local_id, remote_id, next_page, state)
				VALUES (?, ?, ?, 1, 'pending')
			`, runID, crawled.Node.Key.ID, crawled.RemoteID); err != nil {
				return fmt.Errorf("queue Quark directory %d: %w", crawled.Node.Key.ID, err)
			}
		}
	}

	if complete {
		if _, err := tx.ExecContext(ctx, `
			UPDATE crawl_queue
			SET state = 'complete'
			WHERE run_id = ? AND local_id = ?
		`, runID, page.DirectoryID); err != nil {
			return fmt.Errorf("complete Quark crawl directory %d: %w", page.DirectoryID, err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE crawl_queue
			SET next_page = next_page + 1
			WHERE run_id = ? AND local_id = ?
		`, runID, page.DirectoryID); err != nil {
			return fmt.Errorf("advance Quark crawl directory %d: %w", page.DirectoryID, err)
		}
	}
	progressAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE sync_runs
		SET last_progress_at = ?, last_error = NULL
		WHERE run_id = ? AND state = 'staging'
	`, formatTime(&progressAt), runID); err != nil {
		return fmt.Errorf("update Quark crawl progress: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Quark crawl page: %w", err)
	}
	return nil
}

func verifyPendingPage(ctx context.Context, tx *sql.Tx, runID int64, page crawlPage) error {
	var remoteID string
	var nextPage int
	var state string
	if err := tx.QueryRowContext(ctx, `
		SELECT remote_id, next_page, state
		FROM crawl_queue
		WHERE run_id = ? AND local_id = ?
	`, runID, page.DirectoryID).Scan(&remoteID, &nextPage, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("Quark crawl directory %d is not queued", page.DirectoryID)
		}
		return fmt.Errorf("read Quark crawl directory %d: %w", page.DirectoryID, err)
	}
	if remoteID != page.RemoteID || nextPage != page.Number || state != "pending" {
		return fmt.Errorf("stale Quark crawl page: got %+v, current remote ID %q page %d state %q", page, remoteID, nextPage, state)
	}
	return nil
}

func verifyRemoteID(ctx context.Context, tx *sql.Tx, accountID namespace.AccountID, crawled crawledNode) error {
	var localID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT local_id
		FROM remote_ids
		WHERE account_id = ? AND remote_id = ?
	`, accountID, crawled.RemoteID).Scan(&localID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("Quark remote ID %q has not been resolved", crawled.RemoteID)
		}
		return fmt.Errorf("read Quark remote ID mapping: %w", err)
	}
	if localID != crawled.Node.Key.ID {
		return fmt.Errorf("Quark remote ID %q maps to %d, not %d", crawled.RemoteID, localID, crawled.Node.Key.ID)
	}
	return nil
}

func insertCrawledNode(ctx context.Context, tx *sql.Tx, runID int64, node namespace.Node) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes(
			run_id, local_id, parent_id, name, kind, size,
			modified_at, created_at, first_seen_at, category
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		runID,
		node.Key.ID,
		node.Parent.ID,
		node.Name,
		node.Kind,
		strconv.FormatUint(node.Size, 10),
		formatTime(node.ModifiedAt),
		formatTime(node.CreatedAt),
		formatTime(node.FirstSeenAt),
		node.Category,
	); err != nil {
		return fmt.Errorf("stage Quark node %d: %w", node.Key.ID, err)
	}
	return nil
}

func requireCompletedCrawl(ctx context.Context, tx *sql.Tx, runID int64) error {
	var total int
	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(state = 'pending'), 0)
		FROM crawl_queue
		WHERE run_id = ?
	`, runID).Scan(&total, &pending); err != nil {
		return fmt.Errorf("inspect Quark crawl queue: %w", err)
	}
	if total == 0 {
		return fmt.Errorf("Quark generation %d has no crawl queue", runID)
	}
	if pending != 0 {
		return fmt.Errorf("Quark generation %d has %d incomplete directories", runID, pending)
	}
	return nil
}
