package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	productionWorkflowMaxBytes = 64 << 10
)

type productionWorkflowPlan struct {
	Version int                      `json:"version"`
	Steps   []productionWorkflowStep `json:"steps"`
}

type productionWorkflowStep struct {
	ProviderType types.ProductionToolProviderType `json:"provider_type"`
	ProviderID   string                           `json:"provider_id"`
	ToolName     string                           `json:"tool_name"`
	Request      json.RawMessage                  `json:"request"`
}

func canonicalProductionWorkflowPlan(raw, skillBindings types.JSON) (types.JSON, error) {
	canonical, err := types.CanonicalProductionWorkflowPlanSnapshot(raw)
	if err != nil {
		return nil, err
	}
	var plan productionWorkflowPlan
	if err := decodeProductionJSON(canonical, &plan, true); err != nil {
		return nil, err
	}
	for index := range plan.Steps {
		step := &plan.Steps[index]
		if err := validateProductionWorkflowStep(*step, skillBindings); err != nil {
			return nil, fmt.Errorf("invalid production workflow step %d: %w", index, err)
		}
	}
	return canonical, nil
}

func validateProductionWorkflowStep(step productionWorkflowStep, skillBindings types.JSON) error {
	if !step.ProviderType.IsValid() || strings.TrimSpace(step.ProviderID) != step.ProviderID || step.ProviderID == "" ||
		len(step.ProviderID) > 255 || strings.TrimSpace(step.ToolName) != step.ToolName || step.ToolName == "" ||
		len(step.ToolName) > 255 || len(step.Request) == 0 || len(step.Request) > productionWorkflowMaxBytes {
		return errProductionToolCallInvalid
	}
	requestJSON := types.JSON(step.Request)
	if types.RejectProductionCredentialFields(requestJSON) != nil {
		return errProductionToolCallInvalid
	}
	switch step.ProviderType {
	case types.ProductionToolProviderSkill:
		var request productionSkillRequest
		if step.ToolName != productionSkillToolName || decodeProductionJSON(requestJSON, &request, true) != nil ||
			!canonicalProductionUUID(request.SourceItemID) || !productionWorkflowSkillBound(skillBindings, step.ProviderID) {
			return errProductionToolCallInvalid
		}
	case types.ProductionToolProviderMCP:
		var request productionMCPRequest
		if !canonicalProductionUUID(step.ProviderID) || decodeProductionJSON(requestJSON, &request, true) != nil ||
			!canonicalProductionUUID(request.SourceItemID) || request.Arguments == nil ||
			request.ProviderDigest != "" || request.ApprovalRequired != nil {
			return errProductionToolCallInvalid
		}
	case types.ProductionToolProviderDatasource:
		var request productionDataSourceRequest
		if !canonicalProductionUUID(step.ProviderID) || decodeProductionJSON(requestJSON, &request, true) != nil ||
			!canonicalProductionUUID(request.SourceItemID) || request.ProviderDigest != "" ||
			validateProductionDataSourceRequestShape(step.ToolName, request) != nil {
			return errProductionToolCallInvalid
		}
	default:
		return errProductionToolCallInvalid
	}
	return nil
}

func productionWorkflowSkillBound(raw types.JSON, name string) bool {
	var bindings struct {
		Version int `json:"version"`
		Skills  []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"skills"`
	}
	if decodeProductionJSON(raw, &bindings, true) != nil || bindings.Version != 1 {
		return false
	}
	seen := make(map[string]struct{}, len(bindings.Skills))
	for _, binding := range bindings.Skills {
		if binding.Name == "" || !canonicalProductionSHA256(binding.Digest) {
			return false
		}
		if _, duplicate := seen[binding.Name]; duplicate {
			return false
		}
		seen[binding.Name] = struct{}{}
	}
	_, ok := seen[name]
	return ok
}
