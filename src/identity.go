package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ClaudeIdentity is Claude Code's OAuth identity, read from ~/.claude.json.
// Used as the source of truth for who is running this session.
type ClaudeIdentity struct {
	UserUUID    string `json:"user_uuid,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	OrgUUID     string `json:"org_uuid,omitempty"`
	OrgName     string `json:"org_name,omitempty"`
}

// loadClaudeIdentity reads ~/.claude.json and pulls the oauthAccount block.
// Returns a zero-value identity on any read/parse failure — identity is best-effort.
func loadClaudeIdentity() ClaudeIdentity {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeIdentity{}
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return ClaudeIdentity{}
	}
	var raw struct {
		OAuthAccount struct {
			AccountUUID      string `json:"accountUuid"`
			EmailAddress     string `json:"emailAddress"`
			DisplayName      string `json:"displayName"`
			OrganizationUUID string `json:"organizationUuid"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ClaudeIdentity{}
	}
	return ClaudeIdentity{
		UserUUID:    raw.OAuthAccount.AccountUUID,
		Email:       raw.OAuthAccount.EmailAddress,
		DisplayName: raw.OAuthAccount.DisplayName,
		OrgUUID:     raw.OAuthAccount.OrganizationUUID,
		OrgName:     raw.OAuthAccount.OrganizationName,
	}
}

// applyToTrace stamps identity onto a Trace's metadata and adds a `user:<email>`
// tag for quick filtering in the Opik UI. No-op when identity is empty.
func (i ClaudeIdentity) applyToTrace(t *Trace) {
	if i.Email == "" && i.UserUUID == "" {
		return
	}
	if t.Metadata == nil {
		t.Metadata = map[string]interface{}{}
	}
	cc := map[string]interface{}{}
	if i.Email != "" {
		cc["user_email"] = i.Email
	}
	if i.UserUUID != "" {
		cc["user_uuid"] = i.UserUUID
	}
	if i.DisplayName != "" {
		cc["user_display_name"] = i.DisplayName
	}
	if i.OrgUUID != "" {
		cc["org_uuid"] = i.OrgUUID
	}
	if i.OrgName != "" {
		cc["org_name"] = i.OrgName
	}
	t.Metadata["cc"] = cc

	if i.Email != "" {
		t.Tags = append(t.Tags, "user:"+i.Email)
	}
}
