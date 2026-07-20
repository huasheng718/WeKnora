package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	productionWorkflowVersion  = 1
	productionWorkflowMaxSteps = 32
	productionWorkflowMaxBytes = 64 << 10
)

type productionWorkflowPlanSnapshot struct {
	Version int                              `json:"version"`
	Steps   []productionWorkflowStepSnapshot `json:"steps"`
}

type productionWorkflowStepSnapshot struct {
	ProviderType ProductionToolProviderType `json:"provider_type"`
	ProviderID   string                     `json:"provider_id"`
	ToolName     string                     `json:"tool_name"`
	Request      json.RawMessage            `json:"request"`
}

// CanonicalProductionWorkflowPlanSnapshot validates the server-owned workflow
// envelope and returns the exact representation allowed in durable run rows.
func CanonicalProductionWorkflowPlanSnapshot(raw JSON) (JSON, error) {
	if len(raw) == 0 {
		raw = JSON(`{"steps":[],"version":1}`)
	}
	if len(raw) > productionWorkflowMaxBytes {
		return nil, errors.New("production workflow plan exceeds size limit")
	}
	if err := RejectProductionCredentialFields(raw); err != nil {
		return nil, err
	}

	var plan productionWorkflowPlanSnapshot
	if err := decodeStrictProductionWorkflowJSON(raw, &plan); err != nil ||
		plan.Version != productionWorkflowVersion || plan.Steps == nil || len(plan.Steps) > productionWorkflowMaxSteps {
		return nil, errors.New("invalid production workflow plan")
	}
	seen := make(map[string]struct{}, len(plan.Steps))
	for index := range plan.Steps {
		step := &plan.Steps[index]
		if !step.ProviderType.IsValid() || strings.TrimSpace(step.ProviderID) != step.ProviderID || step.ProviderID == "" ||
			len(step.ProviderID) > 255 || strings.TrimSpace(step.ToolName) != step.ToolName || step.ToolName == "" ||
			len(step.ToolName) > 255 || len(step.Request) == 0 || len(step.Request) > productionWorkflowMaxBytes {
			return nil, fmt.Errorf("invalid production workflow step %d", index)
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(step.Request, &request); err != nil || request == nil {
			return nil, fmt.Errorf("invalid production workflow step %d request", index)
		}
		canonicalRequest, err := CanonicalProductionJSON(JSON(step.Request))
		if err != nil {
			return nil, fmt.Errorf("canonicalize production workflow step %d request: %w", index, err)
		}
		step.Request = json.RawMessage(canonicalRequest)
		key := string(step.ProviderType) + "\x00" + step.ProviderID + "\x00" + step.ToolName + "\x00" + string(canonicalRequest)
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("production workflow contains duplicate steps")
		}
		seen[key] = struct{}{}
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	return CanonicalProductionJSON(JSON(encoded))
}

func decodeStrictProductionWorkflowJSON(raw JSON, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
