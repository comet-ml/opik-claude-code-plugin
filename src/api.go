package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// API wraps HTTP calls to the Opik API
type API struct {
	config *Config
	client *http.Client
}

// NewAPI creates a new API client
func NewAPI(config *Config) *API {
	return &API{
		config: config,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Post sends a POST request to the API
func (a *API) Post(endpoint string, data interface{}) error {
	return a.request("POST", endpoint, data)
}

// Patch sends a PATCH request to the API
func (a *API) Patch(endpoint string, data interface{}) error {
	return a.request("PATCH", endpoint, data)
}

func (a *API) request(method, endpoint string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	url := a.config.URL + endpoint
	req, err := http.NewRequest(method, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
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
		if a.config.Debug {
			debugLog("API %s %s failed: %v", method, endpoint, err)
		}
		return err
	}
	defer resp.Body.Close()

	if a.config.Debug && resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		debugLog("API %s %s failed (%d): %s", method, endpoint, resp.StatusCode, string(body))
	}

	return nil
}

// Trace represents an Opik trace
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
}

// Span represents an Opik span
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
}

// SpanBatch wraps spans for batch API calls
type SpanBatch struct {
	Spans []Span `json:"spans"`
}
