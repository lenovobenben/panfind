package quark

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverDesktopBridge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/desktop_info" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("Origin") != desktopBridgeOrigin {
			t.Errorf("Origin = %q", request.Header.Get("Origin"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"success":true,"code":"OK","data":{"isLogin":true,"version":"2.5.40","quarkCloudVersion":"7.0.6.771","wsUid":"secret","wsUtdid":"secret"}}`)
	}))
	defer server.Close()

	bridge, err := discoverDesktopBridgeAt(context.Background(), server.Client(), []string{server.URL})
	if err != nil {
		t.Fatalf("discoverDesktopBridgeAt() error: %v", err)
	}
	if bridge.baseURL.String() != server.URL {
		t.Fatalf("base URL = %q, want %q", bridge.baseURL, server.URL)
	}
	if bridge.info.BridgeVersion != "2.5.40" || bridge.info.ClientVersion != "7.0.6.771" {
		t.Fatalf("bridge info = %+v", bridge.info)
	}
	if bridge.info.AccountID != desktopAccountID("secret") || strings.Contains(string(bridge.info.AccountID), "secret") {
		t.Fatalf("bridge account ID was not anonymized")
	}
}

func TestDiscoverDesktopBridgeSkipsUnavailableAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"success":true,"data":{"isLogin":true,"version":"bridge","quarkCloudVersion":"client","wsUid":"account"}}`)
	}))
	defer server.Close()

	bridge, err := discoverDesktopBridgeAt(context.Background(), server.Client(), []string{"http://127.0.0.1:1", server.URL})
	if err != nil {
		t.Fatalf("discoverDesktopBridgeAt() error: %v", err)
	}
	if bridge.baseURL.String() != server.URL {
		t.Fatalf("base URL = %q, want %q", bridge.baseURL, server.URL)
	}
}

func TestDiscoverDesktopBridgeReportsClientState(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{
			name: "not logged in",
			body: `{"success":true,"data":{"isLogin":false,"version":"bridge","quarkCloudVersion":"client"}}`,
			want: errDesktopClientNotLoggedIn,
		},
		{
			name: "unsupported response",
			body: `{"success":true,"data":{"isLogin":true}}`,
			want: errUnsupportedDesktopBridge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()

			_, err := discoverDesktopBridgeAt(context.Background(), server.Client(), []string{server.URL})
			if !errors.Is(err, test.want) {
				t.Fatalf("discoverDesktopBridgeAt() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDiscoverDesktopBridgeReportsUnavailableClient(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, err := discoverDesktopBridgeAt(context.Background(), client, []string{"http://127.0.0.1:9125"})
	if !errors.Is(err, errDesktopClientUnavailable) {
		t.Fatalf("discoverDesktopBridgeAt() error = %v, want %v", err, errDesktopClientUnavailable)
	}
}

func TestDecodeDesktopInfoRejectsProtocolChanges(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid JSON", body: `{`, want: "decode desktop_info"},
		{name: "unsuccessful", body: `{"success":false}`, want: "did not report success"},
		{name: "missing login", body: `{"success":true,"data":{"version":"bridge","quarkCloudVersion":"client"}}`, want: "login state"},
		{name: "missing bridge version", body: `{"success":true,"data":{"isLogin":true,"quarkCloudVersion":"client"}}`, want: "bridge version"},
		{name: "missing client version", body: `{"success":true,"data":{"isLogin":true,"version":"bridge"}}`, want: "client version"},
		{name: "missing account identity", body: `{"success":true,"data":{"isLogin":true,"version":"bridge","quarkCloudVersion":"client"}}`, want: "account identity"},
		{name: "trailing value", body: `{"success":true,"data":{"isLogin":true,"version":"bridge","quarkCloudVersion":"client","wsUid":"account"}} {}`, want: "trailing JSON value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeDesktopInfo(strings.NewReader(test.body))
			if !errors.Is(err, errUnsupportedDesktopBridge) || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeDesktopInfo() error = %v, want %v containing %q", err, errUnsupportedDesktopBridge, test.want)
			}
		})
	}
}

func TestProbeDesktopBridgeLimitsResponseSize(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxDesktopBridgeResponse+1))),
			Header:     make(http.Header),
		}, nil
	})}
	baseURL, err := parseDesktopBridgeURL("http://127.0.0.1:9125")
	if err != nil {
		t.Fatalf("parseDesktopBridgeURL() error: %v", err)
	}
	_, found, err := probeDesktopBridge(context.Background(), client, baseURL)
	if !found || !errors.Is(err, errUnsupportedDesktopBridge) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("probeDesktopBridge() = found %v, error %v", found, err)
	}
}

func TestParseDesktopBridgeURLRejectsNonLoopbackAddress(t *testing.T) {
	for _, rawURL := range []string{
		"https://127.0.0.1:9125",
		"http://example.com:9125",
		"http://127.0.0.1:9125/path",
		"http://127.0.0.1",
	} {
		if _, err := parseDesktopBridgeURL(rawURL); err == nil {
			t.Errorf("parseDesktopBridgeURL(%q) succeeded", rawURL)
		}
	}
}

func TestDesktopAccountIDIsStableAndScoped(t *testing.T) {
	first := desktopAccountID("account-one")
	if first == "" || first != desktopAccountID("account-one") {
		t.Fatalf("desktopAccountID() is not stable: %q", first)
	}
	if first == desktopAccountID("account-two") {
		t.Fatal("different desktop accounts received the same internal ID")
	}
	if strings.Contains(string(first), "account-one") {
		t.Fatal("internal account ID contains the source account identity")
	}
}
