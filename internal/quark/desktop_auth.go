package quark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"
)

const (
	desktopBridgeRequestTimeout = 5 * time.Second
	desktopTokenPollInterval    = time.Second
	desktopTokenWaitTimeout     = 15 * time.Second
	desktopApprovalPollInterval = 2 * time.Second
	desktopApprovalWaitTimeout  = 30 * time.Second
	desktopWebSessionTimeout    = 10 * time.Second
	quarkAccountEndpoint        = "https://pan.quark.cn/account/info"
	quarkDirectoryOrigin        = "https://pan.quark.cn"
	quarkDirectoryReferer       = "https://pan.quark.cn/"
	quarkWebUserAgent           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

var (
	errDesktopAuthorizationTimedOut = errors.New("Quark desktop authorization timed out")
	errDesktopSessionClosed         = errors.New("Quark desktop session is closed")
)

type desktopAuthTimings struct {
	TokenPollInterval    time.Duration
	TokenWaitTimeout     time.Duration
	ApprovalPollInterval time.Duration
	ApprovalWaitTimeout  time.Duration
}

var defaultDesktopAuthTimings = desktopAuthTimings{
	TokenPollInterval:    desktopTokenPollInterval,
	TokenWaitTimeout:     desktopTokenWaitTimeout,
	ApprovalPollInterval: desktopApprovalPollInterval,
	ApprovalWaitTimeout:  desktopApprovalWaitTimeout,
}

type pendingDesktopAuthorization struct {
	bridge       *desktopBridge
	requestID    string
	promptOpened bool
	timings      desktopAuthTimings
}

type desktopSession struct {
	mu     sync.Mutex
	client *http.Client
}

type wireDesktopBridgeResponse struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type wireDesktopTokenID struct {
	TokenID *string `json:"tokenId"`
}

type wireDesktopToken struct {
	Token *string `json:"token"`
}

type wireDesktopConfirmation struct {
	ID     json.RawMessage `json:"id"`
	IsOpen *bool           `json:"isOpen"`
}

type wireDesktopServiceTicket struct {
	Ticket *string `json:"st"`
}

type wireQuarkWebSession struct {
	Success *bool `json:"success"`
}

// beginAuthorization asks the running desktop client to show its confirmation
// prompt. It does not create or persist a browser profile.
func (bridge *desktopBridge) beginAuthorization(ctx context.Context) (*pendingDesktopAuthorization, error) {
	return bridge.beginAuthorizationWithTimings(ctx, defaultDesktopAuthTimings)
}

func (bridge *desktopBridge) beginAuthorizationWithTimings(ctx context.Context, timings desktopAuthTimings) (*pendingDesktopAuthorization, error) {
	if bridge == nil || bridge.client == nil || bridge.baseURL == nil {
		return nil, errors.New("Quark desktop bridge is nil")
	}
	if err := timings.validate(); err != nil {
		return nil, err
	}

	var tokenID wireDesktopTokenID
	if err := bridge.get(ctx, "/desktop_webtokenid", nil, &tokenID); err != nil {
		return nil, err
	}
	if tokenID.TokenID == nil || *tokenID.TokenID == "" {
		return nil, fmt.Errorf("%w: desktop_webtokenid is missing tokenId", errUnsupportedDesktopBridge)
	}

	tokenContext, cancel := context.WithTimeout(ctx, timings.TokenWaitTimeout)
	defer cancel()
	var token string
	for token == "" {
		if err := waitDesktopPoll(tokenContext, timings.TokenPollInterval); err != nil {
			return nil, desktopAuthorizationWaitError(ctx, err)
		}
		var response wireDesktopToken
		query := bridge.loginQuery()
		query.Set("tokenId", *tokenID.TokenID)
		if err := bridge.get(tokenContext, "/desktop_webtoken", query, &response); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, desktopAuthorizationWaitError(ctx, err)
			}
			return nil, err
		}
		if response.Token != nil {
			token = *response.Token
		}
	}

	query := bridge.loginQuery()
	query.Set("token", token)
	query.Set("source", "button")
	var confirmation wireDesktopConfirmation
	if err := bridge.get(ctx, "/desktop_weblogin_confirm", query, &confirmation); err != nil {
		return nil, err
	}
	requestID, err := decodeDesktopScalar(confirmation.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: desktop_weblogin_confirm has invalid id", errUnsupportedDesktopBridge)
	}
	return &pendingDesktopAuthorization{
		bridge:       bridge,
		requestID:    requestID,
		promptOpened: confirmation.IsOpen != nil && *confirmation.IsOpen,
		timings:      timings,
	}, nil
}

func (login *pendingDesktopAuthorization) waitForSession(ctx context.Context) (*desktopSession, error) {
	return login.waitForSessionAt(ctx, quarkAccountEndpoint, directoryEndpoint)
}

func (login *pendingDesktopAuthorization) waitForSessionAt(ctx context.Context, accountEndpoint string, protectedEndpoint string) (*desktopSession, error) {
	if login == nil || login.bridge == nil || login.requestID == "" {
		return nil, errors.New("Quark pending desktop login is nil")
	}

	approvalContext, cancel := context.WithTimeout(ctx, login.timings.ApprovalWaitTimeout)
	defer cancel()
	var ticket string
	for ticket == "" {
		if err := waitDesktopPoll(approvalContext, login.timings.ApprovalPollInterval); err != nil {
			return nil, desktopAuthorizationWaitError(ctx, err)
		}
		query := login.bridge.loginQuery()
		query.Set("id", login.requestID)
		var response wireDesktopServiceTicket
		if err := login.bridge.get(approvalContext, "/desktop_st", query, &response); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, desktopAuthorizationWaitError(ctx, err)
			}
			return nil, err
		}
		if response.Ticket != nil {
			ticket = *response.Ticket
		}
	}
	return newDesktopSessionAt(ctx, login.bridge.client, accountEndpoint, protectedEndpoint, ticket)
}

func (bridge *desktopBridge) loginQuery() url.Values {
	query := make(url.Values)
	query.Set("platform", "clouddrive")
	query.Set("port", bridge.baseURL.Port())
	return query
}

func (bridge *desktopBridge) get(ctx context.Context, path string, query url.Values, destination any) error {
	requestContext, cancel := context.WithTimeout(ctx, desktopBridgeRequestTimeout)
	defer cancel()

	requestURL := *bridge.baseURL
	requestURL.Path = path
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create Quark desktop bridge %s request: %w", path, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", desktopBridgeOrigin)
	request.Header.Set("Referer", quarkDirectoryReferer)

	response, err := bridge.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if requestContext.Err() != nil {
			return requestContext.Err()
		}
		return fmt.Errorf("%w: desktop bridge request failed", errDesktopClientUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Quark desktop bridge %s returned HTTP status %d", path, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxDesktopBridgeResponse+1))
	if err != nil {
		return fmt.Errorf("%w: read %s response", errUnsupportedDesktopBridge, path)
	}
	if len(body) > maxDesktopBridgeResponse {
		return fmt.Errorf("%w: %s response exceeds %d bytes", errUnsupportedDesktopBridge, path, maxDesktopBridgeResponse)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var envelope wireDesktopBridgeResponse
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("%w: decode %s response", errUnsupportedDesktopBridge, path)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s response has trailing data", errUnsupportedDesktopBridge, path)
	}
	if envelope.Success == nil || !*envelope.Success {
		return fmt.Errorf("%w: %s did not report success", errUnsupportedDesktopBridge, path)
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return fmt.Errorf("%w: %s is missing data", errUnsupportedDesktopBridge, path)
	}
	if err := json.Unmarshal(envelope.Data, destination); err != nil {
		return fmt.Errorf("%w: decode %s data", errUnsupportedDesktopBridge, path)
	}
	return nil
}

func newDesktopSessionAt(ctx context.Context, baseClient *http.Client, accountEndpoint string, protectedEndpoint string, ticket string) (*desktopSession, error) {
	if baseClient == nil {
		return nil, errors.New("Quark web session HTTP client is nil")
	}
	if ticket == "" {
		return nil, errors.New("Quark desktop service ticket is empty")
	}
	accountURL, err := parseHTTPURL(accountEndpoint, "Quark account endpoint")
	if err != nil {
		return nil, err
	}
	protectedURL, err := parseHTTPURL(protectedEndpoint, "Quark protected endpoint")
	if err != nil {
		return nil, err
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create Quark in-memory cookie jar: %w", err)
	}
	clientCopy := *baseClient
	clientCopy.Jar = jar
	authClient := clientCopy
	authClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	requestURL := *accountURL
	query := requestURL.Query()
	query.Set("st", ticket)
	query.Set("lw", "avatar")
	requestURL.RawQuery = query.Encode()
	requestContext, cancel := context.WithTimeout(ctx, desktopWebSessionTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Quark web session request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Referer", quarkDirectoryReferer)
	request.Header.Set("User-Agent", quarkWebUserAgent)

	response, err := authClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requestContext.Err() != nil {
			return nil, requestContext.Err()
		}
		return nil, errors.New("establish Quark web session: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("establish Quark web session: HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDesktopBridgeResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read Quark web session response: %w", err)
	}
	if len(body) > maxDesktopBridgeResponse {
		return nil, fmt.Errorf("Quark web session response exceeds %d bytes", maxDesktopBridgeResponse)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var sessionResponse wireQuarkWebSession
	if err := decoder.Decode(&sessionResponse); err != nil {
		return nil, fmt.Errorf("decode Quark web session response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode Quark web session response: trailing data")
	}
	if sessionResponse.Success == nil || !*sessionResponse.Success {
		return nil, errors.New("Quark web session response did not report success")
	}
	if len(jar.Cookies(protectedURL)) == 0 {
		return nil, errors.New("Quark web session did not set credentials for the directory service")
	}
	return &desktopSession{client: &clientCopy}, nil
}

func (session *desktopSession) httpClient() (*http.Client, error) {
	if session == nil {
		return nil, errDesktopSessionClosed
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.client == nil {
		return nil, errDesktopSessionClosed
	}
	return session.client, nil
}

func (session *desktopSession) close() {
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.client == nil {
		return
	}
	session.client.Jar = nil
	session.client.Transport = closedDesktopSessionTransport{}
	session.client = nil
}

type closedDesktopSessionTransport struct{}

func (closedDesktopSessionTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errDesktopSessionClosed
}

func (timings desktopAuthTimings) validate() error {
	if timings.TokenPollInterval <= 0 || timings.TokenWaitTimeout <= 0 ||
		timings.ApprovalPollInterval <= 0 || timings.ApprovalWaitTimeout <= 0 {
		return errors.New("invalid Quark desktop authentication timings")
	}
	return nil
}

func waitDesktopPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func desktopAuthorizationWaitError(parent context.Context, waitError error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(waitError, context.DeadlineExceeded) {
		return errDesktopAuthorizationTimedOut
	}
	return waitError
}

func decodeDesktopScalar(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("missing value")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return "", errors.New("empty value")
		}
		return text, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	number, ok := value.(json.Number)
	if !ok || number.String() == "" {
		return "", errors.New("value is not a string or number")
	}
	return number.String(), nil
}

func parseHTTPURL(rawURL string, name string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%s is not an absolute HTTP URL", name)
	}
	return parsed, nil
}
