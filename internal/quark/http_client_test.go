package quark

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestHTTPDirectoryClient(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "file-sort.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/1/clouddrive/file/sort" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		want := map[string]string{
			"pr":                   "ucpro",
			"fr":                   "pc",
			"uc_param_str":         "",
			"pdir_fid":             rootRemoteID,
			"_page":                "2",
			"_size":                "50",
			"_fetch_total":         "1",
			"_fetch_sub_dirs":      "0",
			"_sort":                "file_type:asc,updated_at:desc",
			"fetch_all_file":       "1",
			"fetch_risk_file_name": "1",
		}
		for key, value := range want {
			values, exists := query[key]
			if !exists || len(values) != 1 || values[0] != value {
				t.Errorf("query %q = %v, want [%q]", key, values, value)
			}
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("Origin") != quarkDirectoryOrigin || request.Header.Get("Referer") != quarkDirectoryReferer {
			t.Errorf("directory request origin headers are incorrect")
		}
		if request.Header.Get("User-Agent") != quarkWebUserAgent {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write(fixture); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	client, err := newHTTPDirectoryClientAt(server.Client(), server.URL+"/1/clouddrive/file/sort")
	if err != nil {
		t.Fatalf("newHTTPDirectoryClientAt() error: %v", err)
	}
	nodes, err := client.ListDirectory(context.Background(), listDirectoryRequest{
		DirectoryID: rootRemoteID,
		Page:        2,
		Size:        50,
	})
	if err != nil {
		t.Fatalf("ListDirectory() error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("ListDirectory() returned %d nodes", len(nodes))
	}
	if nodes[0].ID != "synthetic-directory-id" || nodes[0].ParentID != rootRemoteID ||
		nodes[0].Name != "shows" || nodes[0].Kind != namespace.NodeKindDirectory || nodes[0].Size != 0 {
		t.Fatalf("unexpected directory node: %+v", nodes[0])
	}
	if nodes[1].ID != "synthetic-file-id" || nodes[1].Kind != namespace.NodeKindFile ||
		nodes[1].Size != 3920473 || nodes[1].Category == nil || *nodes[1].Category != 4 {
		t.Fatalf("unexpected file node: %+v", nodes[1])
	}
	if nodes[1].CreatedAt == nil || !nodes[1].CreatedAt.Equal(time.UnixMilli(1700000000789).UTC()) ||
		nodes[1].ModifiedAt == nil || !nodes[1].ModifiedAt.Equal(time.UnixMilli(1700000000999).UTC()) {
		t.Fatalf("unexpected file timestamps: created=%v modified=%v", nodes[1].CreatedAt, nodes[1].ModifiedAt)
	}
}

func TestHTTPDirectoryClientFeedsScanner(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "file-sort.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	emptyPage := []byte(`{"status":200,"code":0,"data":{"list":[]}}`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("pdir_fid") == rootRemoteID {
			if _, err := writer.Write(fixture); err != nil {
				t.Errorf("write fixture: %v", err)
			}
			return
		}
		if _, err := writer.Write(emptyPage); err != nil {
			t.Errorf("write empty page: %v", err)
		}
	}))
	defer server.Close()

	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()
	runID, err := store.beginGeneration(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("beginGeneration() error: %v", err)
	}
	client, err := newHTTPDirectoryClientAt(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("newHTTPDirectoryClientAt() error: %v", err)
	}
	scanner, err := newScanner(store, client, 50)
	if err != nil {
		t.Fatalf("newScanner() error: %v", err)
	}
	snapshot, err := scanner.run(context.Background(), runID)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if _, exists := snapshot.Lookup("/shows"); !exists {
		t.Fatal("snapshot does not contain synthetic directory")
	}
	if _, exists := snapshot.Lookup("/example.pdf"); !exists {
		t.Fatal("snapshot does not contain synthetic file")
	}
}

func TestDecodeDirectoryResponseRejectsProtocolChanges(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing status", body: `{"code":0,"data":{"list":[]}}`, want: "invalid status"},
		{name: "business error", body: `{"status":200,"code":32001,"data":{"list":[]}}`, want: "invalid business code"},
		{name: "missing data", body: `{"status":200,"code":0}`, want: "missing data"},
		{name: "null list", body: `{"status":200,"code":0,"data":{"list":null}}`, want: "missing data.list"},
		{
			name: "directory markers disagree",
			body: validWireNode(`"file_type":1,"dir":true`),
			want: "dir and file_type disagree",
		},
		{
			name: "negative size",
			body: `{"status":200,"code":0,"data":{"list":[{"fid":"file","pdir_fid":"0","file_name":"file","file_type":1,"dir":false,"size":-1,"created_at":1700000000000,"updated_at":1700000000001}]}}`,
			want: "cannot unmarshal number -1",
		},
		{
			name: "missing timestamp",
			body: `{"status":200,"code":0,"data":{"list":[{"fid":"file","pdir_fid":"0","file_name":"file","file_type":1,"dir":false,"size":1,"updated_at":1700000000000}]}}`,
			want: "created_at",
		},
		{name: "trailing value", body: `{"status":200,"code":0,"data":{"list":[]}} {}`, want: "trailing JSON value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeDirectoryResponse(strings.NewReader(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeDirectoryResponse() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestHTTPDirectoryClientRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "not authenticated", http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := newHTTPDirectoryClientAt(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("newHTTPDirectoryClientAt() error: %v", err)
	}
	_, err = client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 50})
	if !errors.Is(err, errQuarkAuthenticationExpired) || strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("ListDirectory() error = %v", err)
	}
}

func TestHTTPDirectoryClientLimitsResponseSize(t *testing.T) {
	client, err := newHTTPDirectoryClientAt(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxResponseSize+1))),
			Header:     make(http.Header),
		}, nil
	})}, directoryEndpoint)
	if err != nil {
		t.Fatalf("newHTTPDirectoryClientAt() error: %v", err)
	}
	_, err = client.ListDirectory(context.Background(), listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 50})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ListDirectory() error = %v, want size limit", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func validWireNode(fields string) string {
	return `{"status":200,"code":0,"data":{"list":[{"fid":"file","pdir_fid":"0","file_name":"file",` + fields + `,"size":1,"created_at":1700000000000,"updated_at":1700000000001}]}}`
}
