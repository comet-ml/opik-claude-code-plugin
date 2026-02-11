package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State holds the current trace state
type State struct {
	TraceID    string `json:"trace_id"`
	StartTime  string `json:"start_time"`
	SessionID  string `json:"session_id"`
	Transcript string `json:"transcript"`
	StartLine  int    `json:"start_line"`
	LastFlush  int64  `json:"last_flush"`
	SlugSent   bool   `json:"slug_sent,omitempty"`
}

// AgentMap maps agent IDs to their parent task UUIDs
type AgentMap map[string]string

func statePath(sessionID string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("opik-%s.json", sessionID))
}

func agentsPath(sessionID string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("opik-%s-agents.json", sessionID))
}

func LoadState(sessionID string) (*State, error) {
	data, err := os.ReadFile(statePath(sessionID))
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func SaveState(state *State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := statePath(state.SessionID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func DeleteState(sessionID string) {
	os.Remove(statePath(sessionID))
	os.Remove(agentsPath(sessionID))
}

func LoadAgentMap(sessionID string) AgentMap {
	data, err := os.ReadFile(agentsPath(sessionID))
	if err != nil {
		return make(AgentMap)
	}
	var agents AgentMap
	if err := json.Unmarshal(data, &agents); err != nil {
		return make(AgentMap)
	}
	return agents
}

func SaveAgentMap(sessionID string, agents AgentMap) error {
	data, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	path := agentsPath(sessionID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
