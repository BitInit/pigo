package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCLIStreamsAndResumesSession(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.json")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		var request struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if want := int(n) * 2; len(request.Messages) != want {
			t.Errorf("request messages=%d want=%d", len(request.Messages), want)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		var output, diagnostics bytes.Buffer
		err := run(context.Background(), []string{"-cwd", dir, "-model", "test-model", "-base-url", server.URL, "-session", sessionPath, "hello"}, strings.NewReader(""), &output, &diagnostics)
		if err != nil {
			t.Fatal(err)
		}
		if output.String() != "hello\n" {
			t.Fatalf("duplicated or missing stream: %q", output.String())
		}
	}
	if requests.Load() != 2 {
		t.Fatal("unexpected retries")
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatal(err)
	}
}

func TestCLIHelpDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "max-turns") {
		t.Fatal("help is missing flags")
	}
}
