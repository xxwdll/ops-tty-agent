package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyCommandStripsRoutingFields(t *testing.T) {
	const targetToken = "downstream-token"

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Token"); got != targetToken {
			t.Fatalf("X-Token = %q, want %q", got, targetToken)
		}

		var got CommandRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode forwarded request: %v", err)
		}
		if got.Target != "" || got.TargetToken != "" {
			t.Fatalf("routing fields leaked downstream: target=%q target_token=%q", got.Target, got.TargetToken)
		}
		if got.Cmd != "hostname" || got.TimeoutSeconds != 15 {
			t.Fatalf("command fields changed during forwarding: %+v", got)
		}

		jsonEncode(w, CommandResponse{Stdout: "host-a\n", ExitCode: 0})
	}))
	defer downstream.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/cmd", strings.NewReader(`{}`))
	proxyCommand(recorder, request, CommandRequest{
		Cmd:            "hostname",
		TimeoutSeconds: 15,
		Target:         downstream.URL,
		TargetToken:    targetToken,
	}, downstream.URL, targetToken)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response CommandResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode proxy response: %v", err)
	}
	if response.Stdout != "host-a\n" || response.ExitCode != 0 {
		t.Fatalf("unexpected proxy response: %+v", response)
	}
}

func TestGetProxiesPreserveDownstreamResponse(t *testing.T) {
	const targetToken = "wrong-token"

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Token"); got != targetToken {
			t.Fatalf("X-Token = %q, want %q", got, targetToken)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer downstream.Close()

	tests := []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request, string, string)
	}{
		{name: "disk", path: "/disk", call: proxyDisk},
		{name: "stat", path: "/stat?path=%2Ftmp%2Fx", call: proxyStat},
		{name: "tail", path: "/tail?path=%2Ftmp%2Fx&lines=10", call: proxyTail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			tt.call(recorder, request, downstream.URL, targetToken)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", got)
			}
			if got := strings.TrimSpace(recorder.Body.String()); got != `{"error":"unauthorized"}` {
				t.Fatalf("body = %q", got)
			}
		})
	}
}

func TestProxyUploadForwardsTokenPathAndDirectory(t *testing.T) {
	const targetToken = "upload-token"
	const body = "file contents"

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/upload/report & notes.txt" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("dir"); got != "/tmp/a&b" {
			t.Errorf("dir = %q, want /tmp/a&b", got)
		}
		if got := r.Header.Get("X-Token"); got != targetToken {
			t.Errorf("X-Token = %q, want %q", got, targetToken)
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(gotBody) != body {
			t.Errorf("body = %q, want %q", gotBody, body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"created"}`))
	}))
	defer downstream.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/upload/report%20%26%20notes.txt?dir=%2Ftmp%2Fa%26b", strings.NewReader(body))
	proxyUpload(recorder, request, "report & notes.txt", "/tmp/a&b", downstream.URL+"/", targetToken)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
}

func TestProxyDownloadForwardsEncodedPath(t *testing.T) {
	const targetToken = "download-token"

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download/report & notes.txt" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/tmp/a&b#c" {
			t.Errorf("path query = %q, want /tmp/a&b#c", got)
		}
		if got := r.Header.Get("X-Token"); got != targetToken {
			t.Errorf("X-Token = %q, want %q", got, targetToken)
		}
		_, _ = w.Write([]byte("downloaded"))
	}))
	defer downstream.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/download/report%20%26%20notes.txt?path=%2Ftmp%2Fa%26b%23c", nil)
	proxyDownload(recorder, request, "report & notes.txt", downstream.URL+"/", targetToken)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != "downloaded" {
		t.Fatalf("body = %q, want downloaded", recorder.Body.String())
	}
}

func TestResolveTarget(t *testing.T) {
	originalTarget, originalToken := target, serverToken
	t.Cleanup(func() {
		target, serverToken = originalTarget, originalToken
	})

	target = "http://default:8080"
	serverToken = "server-token"

	tests := []struct {
		name         string
		requestURL   string
		requestToken string
		wantURL      string
		wantToken    string
	}{
		{name: "request overrides default", requestURL: "host:9090", requestToken: "request-token", wantURL: "http://host:9090", wantToken: "request-token"},
		{name: "request reuses server token", requestURL: "https://host:9443", wantURL: "https://host:9443", wantToken: "server-token"},
		{name: "default target", wantURL: "http://default:8080", wantToken: "server-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotToken := resolveTarget(tt.requestURL, tt.requestToken)
			if gotURL != tt.wantURL || gotToken != tt.wantToken {
				t.Fatalf("resolveTarget() = (%q, %q), want (%q, %q)", gotURL, gotToken, tt.wantURL, tt.wantToken)
			}
		})
	}
}
