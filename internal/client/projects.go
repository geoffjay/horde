package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Project is the JSON shape returned by the projects endpoints.
type Project struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Workspace string      `json:"workspace"`
	Goal      string      `json:"goal"`
	State     string      `json:"state"`
	Team      ProjectTeam `json:"team"`
}

// ProjectTeam is the team assigned to a project.
type ProjectTeam struct {
	Agents []TeamAgent `json:"agents"`
}

// TeamAgent is one agent member of a project team.
type TeamAgent struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	AssignedAt string `json:"assigned_at"`
}

// CreateProjectRequest is the body for POST /api/v1/projects.
type CreateProjectRequest struct {
	Name       string   `json:"name"`
	Workspace  string   `json:"workspace"`
	Goal       string   `json:"goal"`
	AgentNames []string `json:"agents"`
}

// ListProjects fetches all projects, optionally filtered by state.
func (c *Client) ListProjects(ctx context.Context, state string) ([]Project, error) {
	path := "/api/v1/projects/"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	var ps []Project
	if err := c.getJSON(ctx, path, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// GetProject fetches a single project by id.
func (c *Client) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	if err := c.getJSON(ctx, "/api/v1/projects/"+id, &p); err != nil {
		return p, err
	}
	return p, nil
}

// CreateProject creates a new project with the given inputs.
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (Project, error) {
	var p Project
	body, _ := json.Marshal(req)
	if err := c.postJSON(ctx, "/api/v1/projects/", body, &p); err != nil {
		return p, err
	}
	return p, nil
}

// PauseProject transitions a project to the paused state.
func (c *Client) PauseProject(ctx context.Context, id string) (Project, error) {
	return c.postAction(ctx, "/api/v1/projects/"+id+"/pause")
}

// ResumeProject transitions a project back to the active state.
func (c *Client) ResumeProject(ctx context.Context, id string) (Project, error) {
	return c.postAction(ctx, "/api/v1/projects/"+id+"/resume")
}

// FinishProject transitions a project to the finished state.
func (c *Client) FinishProject(ctx context.Context, id string) (Project, error) {
	return c.postAction(ctx, "/api/v1/projects/"+id+"/finish")
}

// AssignAgent assigns an agent (by name) to a project's team.
func (c *Client) AssignAgent(ctx context.Context, projectID, agentName string) (Project, error) {
	body, _ := json.Marshal(map[string]string{"name": agentName})
	var p Project
	if err := c.postJSON(ctx, "/api/v1/projects/"+projectID+"/agents", body, &p); err != nil {
		return p, err
	}
	return p, nil
}

// RemoveAgent removes an agent from a project's team.
func (c *Client) RemoveAgent(ctx context.Context, projectID, agentID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/api/v1/projects/"+projectID+"/agents/"+agentID, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("remove agent: %s", resp.Status)
	}
	return nil
}

// postAction is a helper for the no-body project lifecycle endpoints
// (pause/resume/finish) that return the updated project.
func (c *Client) postAction(ctx context.Context, path string) (Project, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, http.NoBody)
	if err != nil {
		return Project{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Project{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Project{}, fmt.Errorf("%s: %s", path, resp.Status)
	}
	var p Project
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return p, err
	}
	return p, nil
}
