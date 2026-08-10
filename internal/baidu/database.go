package baidu

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
	_ "modernc.org/sqlite"
)

const syntheticRootID int64 = -1 << 63

var ErrUnsupportedSchema = errors.New("unsupported Baidu filecache.db schema")

var requiredFileMetaColumns = []string{
	"fid",
	"parent_path",
	"server_filename",
	"file_size",
	"md5",
	"isdir",
	"category",
	"server_mtime",
	"local_mtime",
}

type fileMetaRow struct {
	fid            int64
	parentPath     string
	serverFilename string
	fileSize       sql.NullInt64
	md5            sql.NullString
	isDir          sql.NullInt64
	category       sql.NullInt64
	serverMTime    sql.NullInt64
	fullPath       string
}

func loadSnapshot(ctx context.Context, account provider.Account, generation uint64) (*namespace.Snapshot, error) {
	db, err := openReadOnly(account.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open account %q database: %w", account.ID, err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin read-only snapshot: %w", err)
	}
	defer tx.Rollback()

	if err := inspectSchema(ctx, tx); err != nil {
		return nil, err
	}

	rows, err := readFileMeta(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit read-only snapshot: %w", err)
	}

	return buildSnapshot(account, generation, rows)
}

func openReadOnly(databasePath string) (*sql.DB, error) {
	absPath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	uriPath := filepath.ToSlash(absPath)
	if volume := filepath.VolumeName(absPath); volume != "" {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	uri.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func inspectSchema(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(file_meta)")
	if err != nil {
		return fmt.Errorf("%w: inspect file_meta: %v", ErrUnsupportedSchema, err)
	}
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("%w: read file_meta columns: %v", ErrUnsupportedSchema, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read file_meta columns: %v", ErrUnsupportedSchema, err)
	}

	missing := make([]string, 0)
	for _, required := range requiredFileMetaColumns {
		if _, exists := columns[required]; !exists {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: file_meta is missing columns: %s", ErrUnsupportedSchema, strings.Join(missing, ", "))
	}
	return nil
}

func readFileMeta(ctx context.Context, tx *sql.Tx) ([]fileMetaRow, error) {
	// The macOS client defines a unique index with its private
	// baidunetdisksort collation. PanFind cannot register that implementation,
	// so force a table scan instead of letting SQLite prepare the index.
	rows, err := tx.QueryContext(ctx, `
		SELECT fid, parent_path, server_filename, file_size, md5,
		       isdir, category, server_mtime
		FROM file_meta NOT INDEXED`)
	if err != nil {
		return nil, fmt.Errorf("read file_meta: %w", err)
	}
	defer rows.Close()

	result := make([]fileMetaRow, 0)
	for rows.Next() {
		var row fileMetaRow
		if err := rows.Scan(
			&row.fid,
			&row.parentPath,
			&row.serverFilename,
			&row.fileSize,
			&row.md5,
			&row.isDir,
			&row.category,
			&row.serverMTime,
		); err != nil {
			return nil, fmt.Errorf("scan file_meta row: %w", err)
		}

		parentPath, err := normalizeCloudPath(row.parentPath)
		if err != nil {
			return nil, fmt.Errorf("invalid parent path for fid %d: %w", row.fid, err)
		}
		if row.serverFilename == "" || row.serverFilename == "." || row.serverFilename == ".." || strings.Contains(row.serverFilename, "/") {
			return nil, fmt.Errorf("invalid server filename for fid %d", row.fid)
		}
		row.parentPath = parentPath
		row.fullPath = path.Join(parentPath, row.serverFilename)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file_meta: %w", err)
	}
	return result, nil
}

func buildSnapshot(account provider.Account, generation uint64, rows []fileMetaRow) (*namespace.Snapshot, error) {
	rootKey := namespace.NodeKey{Provider: ProviderID, Account: account.ID, ID: syntheticRootID}
	nodes := make([]namespace.Node, 0, len(rows)+1)
	nodes = append(nodes, namespace.Node{Key: rootKey, Name: "/", Kind: namespace.NodeKindDirectory})

	pathKeys := map[string]namespace.NodeKey{"/": rootKey}
	directoryPaths := map[string]bool{"/": true}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].fullPath == rows[j].fullPath {
			return rows[i].fid < rows[j].fid
		}
		return rows[i].fullPath < rows[j].fullPath
	})

	for _, row := range rows {
		key := namespace.NodeKey{Provider: ProviderID, Account: account.ID, ID: row.fid}
		if row.fid == syntheticRootID {
			return nil, fmt.Errorf("fid %d conflicts with the synthetic root", row.fid)
		}
		if !row.isDir.Valid {
			return nil, fmt.Errorf("missing isdir value for fid %d", row.fid)
		}
		if previous, exists := pathKeys[row.fullPath]; exists {
			return nil, fmt.Errorf("duplicate cloud path %q for node IDs %d and %d", row.fullPath, previous.ID, row.fid)
		}
		pathKeys[row.fullPath] = key
		directoryPaths[row.fullPath] = row.isDir.Int64 != 0
	}

	for _, row := range rows {
		parentKey, exists := pathKeys[row.parentPath]
		if !exists {
			return nil, fmt.Errorf("parent path %q for fid %d is missing", row.parentPath, row.fid)
		}
		if !directoryPaths[row.parentPath] {
			return nil, fmt.Errorf("parent path %q for fid %d is not a directory", row.parentPath, row.fid)
		}

		kind := namespace.NodeKindFile
		if row.isDir.Valid && row.isDir.Int64 != 0 {
			kind = namespace.NodeKindDirectory
		}
		if row.fileSize.Valid && row.fileSize.Int64 < 0 {
			return nil, fmt.Errorf("negative file size for fid %d", row.fid)
		}

		node := namespace.Node{
			Key:    namespace.NodeKey{Provider: ProviderID, Account: account.ID, ID: row.fid},
			Parent: parentKey,
			Name:   row.serverFilename,
			Kind:   kind,
		}
		if row.fileSize.Valid {
			node.Size = uint64(row.fileSize.Int64)
		}
		if row.md5.Valid && row.md5.String != "" {
			node.Hash = pointer(row.md5.String)
		}
		if row.category.Valid {
			if row.category.Int64 < math.MinInt32 || row.category.Int64 > math.MaxInt32 {
				return nil, fmt.Errorf("category is outside int32 range for fid %d", row.fid)
			}
			category := int32(row.category.Int64)
			node.Category = &category
		}
		if row.serverMTime.Valid && row.serverMTime.Int64 > 0 {
			modifiedAt := time.Unix(row.serverMTime.Int64, 0).UTC()
			node.ModifiedAt = &modifiedAt
		}
		nodes = append(nodes, node)
	}

	return namespace.NewSnapshot(generation, rootKey, nodes)
}

func normalizeCloudPath(value string) (string, error) {
	if value == "" {
		return "/", nil
	}
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("path contains NUL")
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value), nil
}

func pointer[T any](value T) *T {
	return &value
}
