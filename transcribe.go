package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

// Transcriber turns WAV audio into text.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte, language, prompt string) (string, error)
}

// OpenAIClient talks to any OpenAI-compatible /audio/transcriptions endpoint
// (speaches, faster-whisper-server, LocalAI, vLLM, ...).
type OpenAIClient struct {
	baseURL string // e.g. http://localhost:8000/v1
	apiKey  string
	model   string // "" = auto-detect from the server's /models
	http    *http.Client

	mu       sync.Mutex
	resolved string // cached auto-detected model
}

func newOpenAIClient(cfg *Config) *OpenAIClient {
	return &OpenAIClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		// Timeouts are governed per-request via context.
		http: &http.Client{},
	}
}

// fetchModels lists the model ids the server serves.
func (c *OpenAIClient) fetchModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// resolveModel returns the model to send. Like the Python version's OpenAI
// provider, an unconfigured model is auto-detected from GET /models so
// backend model swaps are picked up without reconfiguring.
func (c *OpenAIClient) resolveModel(ctx context.Context) string {
	if c.model != "" {
		return c.model
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved != "" {
		return c.resolved
	}
	if ids, err := c.fetchModels(ctx); err == nil && len(ids) > 0 {
		c.resolved = ids[0]
	}
	return c.resolved
}

func (c *OpenAIClient) endpoint() string {
	return c.baseURL + "/audio/transcriptions"
}

func (c *OpenAIClient) Transcribe(ctx context.Context, wav []byte, language, prompt string) (string, error) {
	model := c.resolveModel(ctx)
	if model == "" {
		return "", fmt.Errorf("no model available from %s/models and none configured", c.baseURL)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="audio.wav"`)
	hdr.Set("Content-Type", "audio/wav")
	part, err := w.CreatePart(hdr)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}

	fields := map[string]string{
		"model":           model,
		"response_format": "json",
	}
	if language != "" {
		fields["language"] = language
	}
	if prompt != "" {
		fields["prompt"] = prompt
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %s: %s", resp.Status, snippet(raw, 300))
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &out); err == nil {
			return strings.TrimSpace(out.Text), nil
		}
	}
	// Some servers ignore response_format and return plain text.
	return string(trimmed), nil
}

// probeServer checks that something is listening at the base URL. A 404 on
// /models still proves reachability.
func probeServer(baseURL, apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// checkServer probes the server so misconfiguration surfaces at startup
// instead of on the first recording. Non-fatal: prints a status line.
func (c *OpenAIClient) checkServer() {
	if err := probeServer(c.baseURL, c.apiKey); err != nil {
		warn("Server not reachable at %s (%v)", c.baseURL, err)
		info("   Transcriptions will fail until the server is up.")
		info("   Run sttg --config to change the server settings.")
		return
	}
	info("✓ Server: %s", c.baseURL)
	if c.model != "" {
		info("✓ Model: %s", c.model)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if m := c.resolveModel(ctx); m != "" {
		info("✓ Model: %s (auto)", m)
	} else {
		warn("Server lists no models and no STT_MODEL is set; transcriptions may fail.")
	}
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
