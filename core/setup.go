package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type AddPlatformRequest struct {
	Type      string         `json:"type"`
	Options   map[string]any `json:"options"`
	WorkDir   string         `json:"work_dir"`
	AgentType string         `json:"agent_type"`
}

func (m *ManagementServer) handleProjectAddPlatform(w http.ResponseWriter, r *http.Request, projectName string) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req AddPlatformRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Type == "" {
		mgmtError(w, http.StatusBadRequest, "type is required")
		return
	}
	if !strings.EqualFold(req.Type, "matrix") {
		mgmtError(w, http.StatusBadRequest, "only matrix platform is supported")
		return
	}
	if m.addPlatformToProject == nil {
		mgmtError(w, http.StatusServiceUnavailable, "config persistence not available")
		return
	}
	if err := m.addPlatformToProject(projectName, "matrix", req.Options, req.WorkDir, req.AgentType); err != nil {
		mgmtError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusCreated, map[string]any{
		"message":          fmt.Sprintf("matrix platform added to project %q", projectName),
		"restart_required": true,
	})
}
