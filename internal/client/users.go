package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// User is one entry in the GET /api/v1/users response (id + admin + you
// marker). Tokens are never surfaced by the API.
type User struct {
	ID    string `json:"id"`
	Admin bool   `json:"admin"`
	You   bool   `json:"you"`
}

// UsersResponse is the GET /api/v1/users response shape.
type UsersResponse struct {
	AuthEnabled bool   `json:"auth_enabled"`
	Users       []User `json:"users"`
}

// Users fetches the per-user identity list and the node's auth-enabled state.
// Used by the TUI Users view. When auth is disabled the response is an empty
// users list with auth_enabled=false.
func (c *Client) Users(ctx context.Context) (UsersResponse, error) {
	resp, err := c.get(ctx, "/api/v1/users")
	if err != nil {
		return UsersResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UsersResponse{}, fmt.Errorf("/api/v1/users: %s", resp.Status)
	}
	var out UsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UsersResponse{}, err
	}
	return out, nil
}
