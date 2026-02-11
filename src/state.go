package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// State holds the current trace state
type State struct {
	TraceID    string `json:"trace_id"`
	StartTime  string `json:"start_time"`
	SessionID  string `json:"session_id"`
	Transcript string `json:"transcript"`
	StartLine  int    `json:"start_line"`
	LastFlush  int64  `json:"last_flush"`
}

// AgentMap maps agent IDs to their parent task UUIDs
type AgentMap map[string]string

// statePath returns the path to the state file for a session
func statePath(sessionID string) string {
	return fmt.Sprintf("/tmp/opik-%s.json", sessionID)
}

// agentsPath returns the path to the agents map file for a session
func agentsPath(sessionID string) string {
	return fmt.Sprintf("/tmp/opik-%s-agents.json", sessionID)
}

// LoadState loads the state from disk
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

// SaveState saves the state to disk
func SaveState(state *State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	
	return os.WriteFile(statePath(state.SessionID), data, 0644)
}

// DeleteState removes the state file
func DeleteState(sessionID string) {
	os.Remove(statePath(sessionID))
	os.Remove(agentsPath(sessionID))
}

// LoadAgentMap loads the agent mapping from disk
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

// SaveAgentMap saves the agent mapping to disk
func SaveAgentMap(sessionID string, agents AgentMap) error {
	data, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	
	return os.WriteFile(agentsPath(sessionID), data, 0644)
}
