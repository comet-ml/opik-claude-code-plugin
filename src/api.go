package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type API struct {
	config *Config
	client *http.Client
}

func NewAPI(cfg *Config) *API {
	return &API{
		config: cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *API) Post(endpoint string, data interface{}) error {
	return a.request("POST", endpoint, data)
}

func (a *API) Patch(endpoint string, data interface{}) error {
	return a.request("PATCH", endpoint, data)
}

func (a *API) request(method, endpoint string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("%s %s: marshal: %w", method, endpoint, err)
	}

	req, err := http.NewRequest(method, a.config.URL+endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("%s %s: build request: %w", method, endpoint, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if a.config.APIKey != "" {
		req.Header.Set("Authorization", a.config.APIKey)
	}
	if a.config.Workspace != "" {
		req.Header.Set("Comet-Workspace", a.config.Workspace)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %d %s", method, endpoint, resp.StatusCode, body)
	}

	return nil
}

type Trace struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	StartTime   string            `json:"start_time"`
	EndTime     string            `json:"end_time,omitempty"`
	ProjectName string            `json:"project_name"`
	ThreadID    string            `json:"thread_id,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Input       map[string]string `json:"input,omitempty"`
	Output      map[string]string `json:"output,omitempty"`
	Model       string            `json:"model,omitempty"`
}

type Span struct {
	ID           string                 `json:"id"`
	TraceID      string                 `json:"trace_id"`
	ParentSpanID string                 `json:"parent_span_id,omitempty"`
	Name         string                 `json:"name"`
	Type         string                 `json:"type"`
	StartTime    string                 `json:"start_time"`
	EndTime      string                 `json:"end_time"`
	ProjectName  string                 `json:"project_name"`
	Input        map[string]interface{} `json:"input"`
	Output       map[string]interface{} `json:"output"`
	Usage        map[string]int         `json:"usage,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Provider     string                 `json:"provider,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Error        *SpanError             `json:"error,omitempty"`
}

type SpanError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type SpanBatch struct {
	Spans []Span `json:"spans"`
}
