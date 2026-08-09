package quark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const (
	directoryEndpoint = "https://drive-pc.quark.cn/1/clouddrive/file/sort"
	maxResponseSize   = 16 << 20
)

var errQuarkAuthenticationExpired = errors.New("Quark authentication expired")

type httpDirectoryClient struct {
	client   *http.Client
	endpoint *url.URL
}

func newHTTPDirectoryClient(client *http.Client) (*httpDirectoryClient, error) {
	return newHTTPDirectoryClientAt(client, directoryEndpoint)
}

func newHTTPDirectoryClientAt(client *http.Client, endpoint string) (*httpDirectoryClient, error) {
	if client == nil {
		return nil, errors.New("Quark HTTP client is nil")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Quark directory endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("Quark directory endpoint is not absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("Quark directory endpoint has unsupported scheme %q", parsed.Scheme)
	}
	return &httpDirectoryClient{client: client, endpoint: parsed}, nil
}

func (client *httpDirectoryClient) ListDirectory(ctx context.Context, request listDirectoryRequest) ([]remoteNode, error) {
	if request.DirectoryID == "" {
		return nil, errors.New("Quark directory ID is empty")
	}
	if request.Page <= 0 || request.Size <= 0 {
		return nil, fmt.Errorf("invalid Quark directory page request: %+v", request)
	}

	requestURL := *client.endpoint
	query := requestURL.Query()
	query.Set("pr", "ucpro")
	query.Set("fr", "pc")
	query.Set("uc_param_str", "")
	query.Set("pdir_fid", request.DirectoryID)
	query.Set("_page", strconv.Itoa(request.Page))
	query.Set("_size", strconv.Itoa(request.Size))
	query.Set("_fetch_total", "1")
	query.Set("_fetch_sub_dirs", "0")
	query.Set("_sort", "file_type:asc,updated_at:desc")
	query.Set("fetch_all_file", "1")
	query.Set("fetch_risk_file_name", "1")
	requestURL.RawQuery = query.Encode()

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Quark directory request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Origin", quarkDirectoryOrigin)
	httpRequest.Header.Set("Referer", quarkDirectoryReferer)
	httpRequest.Header.Set("User-Agent", quarkWebUserAgent)

	response, err := client.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send Quark directory request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return nil, errQuarkAuthenticationExpired
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Quark directory request returned HTTP status %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Quark directory response: %w", err)
	}
	if len(body) > maxResponseSize {
		return nil, fmt.Errorf("Quark directory response exceeds %d bytes", maxResponseSize)
	}
	return decodeDirectoryResponse(bytes.NewReader(body))
}

type wireDirectoryResponse struct {
	Status *int               `json:"status"`
	Code   *int               `json:"code"`
	Data   *wireDirectoryData `json:"data"`
}

type wireDirectoryData struct {
	List json.RawMessage `json:"list"`
}

type wireNode struct {
	ID        *string `json:"fid"`
	ParentID  *string `json:"pdir_fid"`
	Name      *string `json:"file_name"`
	FileType  *int    `json:"file_type"`
	Directory *bool   `json:"dir"`
	Size      *uint64 `json:"size"`
	CreatedAt *int64  `json:"created_at"`
	UpdatedAt *int64  `json:"updated_at"`
	Category  *int32  `json:"category"`
}

func decodeDirectoryResponse(reader io.Reader) ([]remoteNode, error) {
	decoder := json.NewDecoder(reader)
	var response wireDirectoryResponse
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Quark directory response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode Quark directory response: trailing JSON value")
		}
		return nil, fmt.Errorf("decode Quark directory response trailing data: %w", err)
	}
	if response.Status == nil || *response.Status != http.StatusOK {
		return nil, fmt.Errorf("Quark directory response has invalid status: %v", optionalInt(response.Status))
	}
	if response.Code == nil || *response.Code != 0 {
		return nil, fmt.Errorf("Quark directory response has invalid business code: %v", optionalInt(response.Code))
	}
	if response.Data == nil {
		return nil, errors.New("Quark directory response is missing data")
	}
	if len(response.Data.List) == 0 || bytes.Equal(bytes.TrimSpace(response.Data.List), []byte("null")) {
		return nil, errors.New("Quark directory response is missing data.list")
	}

	var wireNodes []wireNode
	if err := json.Unmarshal(response.Data.List, &wireNodes); err != nil {
		return nil, fmt.Errorf("decode Quark directory response data.list: %w", err)
	}
	result := make([]remoteNode, len(wireNodes))
	for index, wire := range wireNodes {
		node, err := decodeWireNode(wire)
		if err != nil {
			return nil, fmt.Errorf("decode Quark directory node %d: %w", index, err)
		}
		result[index] = node
	}
	return result, nil
}

func decodeWireNode(wire wireNode) (remoteNode, error) {
	if wire.ID == nil || *wire.ID == "" {
		return remoteNode{}, errors.New("missing fid")
	}
	if wire.ParentID == nil || *wire.ParentID == "" {
		return remoteNode{}, errors.New("missing pdir_fid")
	}
	if wire.Name == nil || *wire.Name == "" {
		return remoteNode{}, errors.New("missing file_name")
	}
	if wire.FileType == nil || (*wire.FileType != 0 && *wire.FileType != 1) {
		return remoteNode{}, fmt.Errorf("invalid file_type: %v", optionalInt(wire.FileType))
	}
	if wire.Directory == nil {
		return remoteNode{}, errors.New("missing dir")
	}
	if (*wire.Directory && *wire.FileType != 0) || (!*wire.Directory && *wire.FileType != 1) {
		return remoteNode{}, errors.New("dir and file_type disagree")
	}
	if wire.Size == nil {
		return remoteNode{}, errors.New("missing size")
	}
	if wire.CreatedAt == nil || *wire.CreatedAt <= 0 {
		return remoteNode{}, errors.New("missing or invalid created_at")
	}
	if wire.UpdatedAt == nil || *wire.UpdatedAt <= 0 {
		return remoteNode{}, errors.New("missing or invalid updated_at")
	}

	kind := namespace.NodeKindFile
	if *wire.Directory {
		kind = namespace.NodeKindDirectory
	}
	createdAt := time.UnixMilli(*wire.CreatedAt).UTC()
	updatedAt := time.UnixMilli(*wire.UpdatedAt).UTC()
	return remoteNode{
		ID:         *wire.ID,
		ParentID:   *wire.ParentID,
		Name:       *wire.Name,
		Kind:       kind,
		Size:       *wire.Size,
		ModifiedAt: &updatedAt,
		CreatedAt:  &createdAt,
		Category:   wire.Category,
	}, nil
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

var _ directoryClient = (*httpDirectoryClient)(nil)
