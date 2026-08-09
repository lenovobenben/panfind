package quark

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopAuthenticationCreatesMemorySession(t *testing.T) {
	var protectedRequests atomic.Int32
	webServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/account/info":
			if request.URL.Query().Get("st") != "service-ticket" || request.URL.Query().Get("lw") != "avatar" {
				t.Errorf("account query keys are incorrect")
			}
			if request.Header.Get("Referer") != quarkDirectoryReferer || request.Header.Get("User-Agent") != quarkWebUserAgent {
				t.Errorf("account request headers are incorrect")
			}
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "credential", Path: "/", HttpOnly: true})
			_, _ = io.WriteString(writer, `{"success":true,"data":{}}`)
		case "/protected":
			protectedRequests.Add(1)
			cookie, err := request.Cookie("session")
			if err != nil || cookie.Value != "credential" {
				t.Errorf("protected request cookie = %v, error %v", cookie, err)
			}
			_, _ = io.WriteString(writer, `{}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer webServer.Close()

	var tokenRequests atomic.Int32
	var ticketRequests atomic.Int32
	var bridgeServer *httptest.Server
	bridgeServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != desktopBridgeOrigin || request.Header.Get("Referer") != quarkDirectoryReferer {
			t.Errorf("bridge request headers are incorrect")
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/desktop_webtokenid":
			_, _ = io.WriteString(writer, `{"success":true,"data":{"tokenId":"token-id"}}`)
		case "/desktop_webtoken":
			assertLoginQuery(t, request.URL.Query(), bridgeServerPort(request.Host))
			if request.URL.Query().Get("tokenId") != "token-id" {
				t.Errorf("tokenId query is missing")
			}
			if tokenRequests.Add(1) == 1 {
				_, _ = io.WriteString(writer, `{"success":true,"data":{}}`)
				return
			}
			_, _ = io.WriteString(writer, `{"success":true,"data":{"token":"short-token"}}`)
		case "/desktop_weblogin_confirm":
			assertLoginQuery(t, request.URL.Query(), bridgeServerPort(request.Host))
			if request.URL.Query().Get("token") != "short-token" || request.URL.Query().Get("source") != "button" {
				t.Errorf("confirmation query is incorrect")
			}
			_, _ = io.WriteString(writer, `{"success":true,"data":{"id":12345,"isOpen":true}}`)
		case "/desktop_st":
			assertLoginQuery(t, request.URL.Query(), bridgeServerPort(request.Host))
			if request.URL.Query().Get("id") != "12345" {
				t.Errorf("confirmation id query is missing")
			}
			if ticketRequests.Add(1) == 1 {
				_, _ = io.WriteString(writer, `{"success":true,"data":{}}`)
				return
			}
			_, _ = io.WriteString(writer, `{"success":true,"data":{"st":"service-ticket"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer bridgeServer.Close()

	baseURL, err := parseDesktopBridgeURL(bridgeServer.URL)
	if err != nil {
		t.Fatalf("parseDesktopBridgeURL() error: %v", err)
	}
	baseClient := bridgeServer.Client()
	bridge := &desktopBridge{client: baseClient, baseURL: baseURL}
	timings := desktopAuthTimings{
		TokenPollInterval:    time.Millisecond,
		TokenWaitTimeout:     200 * time.Millisecond,
		ApprovalPollInterval: time.Millisecond,
		ApprovalWaitTimeout:  200 * time.Millisecond,
	}
	login, err := bridge.beginAuthorizationWithTimings(context.Background(), timings)
	if err != nil {
		t.Fatalf("beginAuthorizationWithTimings() error: %v", err)
	}
	if !login.promptOpened {
		t.Fatal("desktop confirmation prompt was not reported open")
	}
	session, err := login.waitForSessionAt(context.Background(), webServer.URL+"/account/info", webServer.URL+"/protected")
	if err != nil {
		t.Fatalf("waitForSessionAt() error: %v", err)
	}
	if baseClient.Jar != nil {
		t.Fatal("desktop bridge HTTP client received the web session cookie jar")
	}
	sessionClient, err := session.httpClient()
	if err != nil {
		t.Fatalf("httpClient() error: %v", err)
	}
	response, err := sessionClient.Get(webServer.URL + "/protected")
	if err != nil {
		t.Fatalf("protected request error: %v", err)
	}
	response.Body.Close()
	if protectedRequests.Load() != 1 {
		t.Fatalf("protected requests = %d", protectedRequests.Load())
	}

	session.close()
	if _, err := session.httpClient(); !errors.Is(err, errDesktopSessionClosed) {
		t.Fatalf("httpClient() after close error = %v", err)
	}
	if _, err := sessionClient.Get(webServer.URL + "/protected"); !errors.Is(err, errDesktopSessionClosed) {
		t.Fatalf("retained HTTP client after close error = %v", err)
	}
}

func TestDesktopAuthenticationTimesOutWithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/desktop_webtokenid" {
			_, _ = io.WriteString(writer, `{"success":true,"data":{"tokenId":"token-id"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"success":true,"data":{}}`)
	}))
	defer server.Close()
	baseURL, err := parseDesktopBridgeURL(server.URL)
	if err != nil {
		t.Fatalf("parseDesktopBridgeURL() error: %v", err)
	}
	bridge := &desktopBridge{client: server.Client(), baseURL: baseURL}
	timings := desktopAuthTimings{
		TokenPollInterval:    time.Millisecond,
		TokenWaitTimeout:     10 * time.Millisecond,
		ApprovalPollInterval: time.Millisecond,
		ApprovalWaitTimeout:  time.Second,
	}
	_, err = bridge.beginAuthorizationWithTimings(context.Background(), timings)
	if !errors.Is(err, errDesktopAuthorizationTimedOut) {
		t.Fatalf("beginAuthorizationWithTimings() error = %v", err)
	}
}

func TestDesktopAuthenticationTimesOutWithoutApproval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/desktop_st" {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, `{"success":true,"data":{}}`)
	}))
	defer server.Close()
	baseURL, err := parseDesktopBridgeURL(server.URL)
	if err != nil {
		t.Fatalf("parseDesktopBridgeURL() error: %v", err)
	}
	login := &pendingDesktopAuthorization{
		bridge:    &desktopBridge{client: server.Client(), baseURL: baseURL},
		requestID: "request-id",
		timings: desktopAuthTimings{
			TokenPollInterval:    time.Millisecond,
			TokenWaitTimeout:     time.Second,
			ApprovalPollInterval: time.Millisecond,
			ApprovalWaitTimeout:  10 * time.Millisecond,
		},
	}
	_, err = login.waitForSessionAt(context.Background(), "https://pan.quark.cn/account/info", directoryEndpoint)
	if !errors.Is(err, errDesktopAuthorizationTimedOut) {
		t.Fatalf("waitForSessionAt() error = %v", err)
	}
}

func TestDesktopSessionDoesNotLeakTicketInRequestError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New(request.URL.String())
	})}
	_, err := newDesktopSessionAt(context.Background(), client, "https://pan.quark.cn/account/info", directoryEndpoint, "sensitive-service-ticket")
	if err == nil || strings.Contains(err.Error(), "sensitive-service-ticket") {
		t.Fatalf("newDesktopSessionAt() error = %v", err)
	}
}

func TestDesktopBridgeDoesNotLeakTokenInRequestError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New(request.URL.String())
	})}
	baseURL, err := parseDesktopBridgeURL("http://127.0.0.1:9125")
	if err != nil {
		t.Fatalf("parseDesktopBridgeURL() error: %v", err)
	}
	bridge := &desktopBridge{client: client, baseURL: baseURL}
	query := make(url.Values)
	query.Set("token", "sensitive-short-token")
	var response wireDesktopConfirmation
	err = bridge.get(context.Background(), "/desktop_weblogin_confirm", query, &response)
	if !errors.Is(err, errDesktopClientUnavailable) || strings.Contains(err.Error(), "sensitive-short-token") {
		t.Fatalf("desktop bridge request error = %v", err)
	}
}

func assertLoginQuery(t *testing.T, query url.Values, port string) {
	t.Helper()
	if query.Get("platform") != "clouddrive" || query.Get("port") != port {
		t.Errorf("login query platform=%q port=%q", query.Get("platform"), query.Get("port"))
	}
}

func bridgeServerPort(host string) string {
	_, port, _ := strings.Cut(host, ":")
	return port
}
