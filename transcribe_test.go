package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(url, key string) *OpenAIClient {
	return newOpenAIClient(&Config{BaseURL: url, APIKey: key, Model: "whisper-1"})
}

func TestTranscribeSendsMultipart(t *testing.T) {
	var gotAuth, gotModel, gotLang, gotPrompt, gotFormat, gotFilename string
	var gotFile []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatal(err)
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		gotPrompt = r.FormValue("prompt")
		gotFormat = r.FormValue("response_format")
		file, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		gotFilename = hdr.Filename
		gotFile, _ = io.ReadAll(file)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text": "  hello there  "}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL+"/v1", "secret")
	wav := encodeWAV([]int16{1, 2, 3}, 16000)
	text, err := c.Transcribe(context.Background(), wav, "en", "jargon")
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello there" {
		t.Fatalf("text = %q", text)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "whisper-1" || gotLang != "en" || gotPrompt != "jargon" || gotFormat != "json" {
		t.Fatalf("fields: model=%q lang=%q prompt=%q format=%q", gotModel, gotLang, gotPrompt, gotFormat)
	}
	if gotFilename != "audio.wav" || len(gotFile) != len(wav) {
		t.Fatalf("file: name=%q len=%d want len=%d", gotFilename, len(gotFile), len(wav))
	}
}

func TestTranscribeNoAuthHeaderWithoutKey(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.Write([]byte(`{"text": "x"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL+"/v1", "")
	if _, err := c.Transcribe(context.Background(), []byte("wav"), "", ""); err != nil {
		t.Fatal(err)
	}
	if sawAuth {
		t.Fatal("Authorization header must not be sent without an API key")
	}
}

func TestTranscribeOmitsEmptyOptionalFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(32 << 20)
		if _, ok := r.MultipartForm.Value["language"]; ok {
			t.Error("language field should be omitted when empty")
		}
		if _, ok := r.MultipartForm.Value["prompt"]; ok {
			t.Error("prompt field should be omitted when empty")
		}
		w.Write([]byte(`{"text": "x"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL+"/v1", "")
	if _, err := c.Transcribe(context.Background(), []byte("wav"), "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestTranscribePlainTextFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("  plain response \n"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL+"/v1", "")
	text, err := c.Transcribe(context.Background(), []byte("wav"), "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if text != "plain response" {
		t.Fatalf("text = %q", text)
	}
}

func TestTranscribeErrorStatusSurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error": "model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL+"/v1", "")
	_, err := c.Transcribe(context.Background(), []byte("wav"), "en", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error should include server body: %v", err)
	}
}

func TestTranscribeContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newTestClient(srv.URL+"/v1", "")
	_, err := c.Transcribe(ctx, []byte("wav"), "en", "")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
