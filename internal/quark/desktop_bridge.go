package quark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const (
	desktopBridgeFirstPort    = 9125
	desktopBridgeLastPort     = 9130
	desktopBridgeOrigin       = "https://pan.quark.cn"
	desktopBridgeProbeTimeout = 500 * time.Millisecond
	maxDesktopBridgeResponse  = 64 << 10
)

var (
	errDesktopClientUnavailable = errors.New("Quark desktop client is not running")
	errDesktopClientNotLoggedIn = errors.New("Quark desktop client is not logged in")
	errUnsupportedDesktopBridge = errors.New("unsupported Quark desktop bridge response")
)

type desktopBridgeInfo struct {
	AccountID     namespace.AccountID
	BridgeVersion string
	ClientVersion string
}

type desktopBridge struct {
	client  *http.Client
	baseURL *url.URL
	info    desktopBridgeInfo
}

type wireDesktopInfoResponse struct {
	Success *bool            `json:"success"`
	Data    *wireDesktopInfo `json:"data"`
}

type wireDesktopInfo struct {
	LoggedIn      *bool   `json:"isLogin"`
	BridgeVersion *string `json:"version"`
	ClientVersion *string `json:"quarkCloudVersion"`
	UserID        *string `json:"wsUid"`
}

func discoverDesktopBridge(ctx context.Context, client *http.Client) (*desktopBridge, error) {
	baseURLs := make([]string, 0, desktopBridgeLastPort-desktopBridgeFirstPort+1)
	for port := desktopBridgeFirstPort; port <= desktopBridgeLastPort; port++ {
		baseURLs = append(baseURLs, "http://127.0.0.1:"+strconv.Itoa(port))
	}
	return discoverDesktopBridgeAt(ctx, client, baseURLs)
}

func discoverDesktopBridgeAt(ctx context.Context, client *http.Client, baseURLs []string) (*desktopBridge, error) {
	if client == nil {
		return nil, errors.New("Quark desktop bridge HTTP client is nil")
	}
	if len(baseURLs) == 0 {
		return nil, errors.New("Quark desktop bridge address list is empty")
	}

	var protocolError error
	for _, rawBaseURL := range baseURLs {
		baseURL, err := parseDesktopBridgeURL(rawBaseURL)
		if err != nil {
			return nil, err
		}
		info, found, err := probeDesktopBridge(ctx, client, baseURL)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return nil, err
			}
			if found {
				protocolError = err
			}
			continue
		}
		if !found {
			continue
		}
		if !info.loggedIn {
			return nil, errDesktopClientNotLoggedIn
		}
		return &desktopBridge{
			client:  client,
			baseURL: baseURL,
			info: desktopBridgeInfo{
				AccountID:     desktopAccountID(info.userID),
				BridgeVersion: info.bridgeVersion,
				ClientVersion: info.clientVersion,
			},
		}, nil
	}
	if protocolError != nil {
		return nil, protocolError
	}
	return nil, errDesktopClientUnavailable
}

type probedDesktopInfo struct {
	loggedIn      bool
	bridgeVersion string
	clientVersion string
	userID        string
}

func probeDesktopBridge(ctx context.Context, client *http.Client, baseURL *url.URL) (probedDesktopInfo, bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, desktopBridgeProbeTimeout)
	defer cancel()

	requestURL := *baseURL
	requestURL.Path = "/desktop_info"
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return probedDesktopInfo{}, false, fmt.Errorf("create Quark desktop bridge request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", desktopBridgeOrigin)

	response, err := client.Do(request)
	if err != nil {
		return probedDesktopInfo{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return probedDesktopInfo{}, false, nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxDesktopBridgeResponse+1))
	if err != nil {
		return probedDesktopInfo{}, true, fmt.Errorf("%w: read desktop_info: %v", errUnsupportedDesktopBridge, err)
	}
	if len(body) > maxDesktopBridgeResponse {
		return probedDesktopInfo{}, true, fmt.Errorf("%w: desktop_info exceeds %d bytes", errUnsupportedDesktopBridge, maxDesktopBridgeResponse)
	}
	info, err := decodeDesktopInfo(bytes.NewReader(body))
	if err != nil {
		return probedDesktopInfo{}, true, err
	}
	return info, true, nil
}

func decodeDesktopInfo(reader io.Reader) (probedDesktopInfo, error) {
	decoder := json.NewDecoder(reader)
	var response wireDesktopInfoResponse
	if err := decoder.Decode(&response); err != nil {
		return probedDesktopInfo{}, fmt.Errorf("%w: decode desktop_info: %v", errUnsupportedDesktopBridge, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info has trailing JSON value", errUnsupportedDesktopBridge)
		}
		return probedDesktopInfo{}, fmt.Errorf("%w: decode desktop_info trailing data: %v", errUnsupportedDesktopBridge, err)
	}
	if response.Success == nil || !*response.Success {
		return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info did not report success", errUnsupportedDesktopBridge)
	}
	if response.Data == nil || response.Data.LoggedIn == nil {
		return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info is missing login state", errUnsupportedDesktopBridge)
	}
	if response.Data.BridgeVersion == nil || *response.Data.BridgeVersion == "" {
		return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info is missing bridge version", errUnsupportedDesktopBridge)
	}
	if response.Data.ClientVersion == nil || *response.Data.ClientVersion == "" {
		return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info is missing client version", errUnsupportedDesktopBridge)
	}
	if *response.Data.LoggedIn && (response.Data.UserID == nil || *response.Data.UserID == "") {
		return probedDesktopInfo{}, fmt.Errorf("%w: desktop_info is missing account identity", errUnsupportedDesktopBridge)
	}
	userID := ""
	if response.Data.UserID != nil {
		userID = *response.Data.UserID
	}
	return probedDesktopInfo{
		loggedIn:      *response.Data.LoggedIn,
		bridgeVersion: *response.Data.BridgeVersion,
		clientVersion: *response.Data.ClientVersion,
		userID:        userID,
	}, nil
}

func desktopAccountID(userID string) namespace.AccountID {
	digest := sha256.Sum256([]byte(userID))
	return namespace.AccountID("quark-" + hex.EncodeToString(digest[:]))
}

func parseDesktopBridgeURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Quark desktop bridge address: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid Quark desktop bridge address %q", rawURL)
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() || parsed.Port() == "" {
		return nil, fmt.Errorf("Quark desktop bridge address is not an explicit loopback endpoint: %q", rawURL)
	}
	return parsed, nil
}
