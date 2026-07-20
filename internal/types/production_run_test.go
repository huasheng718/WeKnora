package types

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCanonicalProductionWorkflowPlanSnapshotIsStrictBoundedAndCredentialFree(t *testing.T) {
	valid := JSON(`{"steps":[{"provider_id":"71000000-0000-4000-8000-000000000006","provider_type":"mcp","request":{"arguments":{"query":"safe"},"source_item_id":"71000000-0000-4000-8000-000000000005"},"tool_name":"lookup"}],"version":1}`)
	canonical, err := CanonicalProductionWorkflowPlanSnapshot(valid)
	require.NoError(t, err)
	require.Equal(t, valid, canonical)

	for name, raw := range map[string]JSON{
		"missing steps":      JSON(`{"version":1}`),
		"null steps":         JSON(`{"version":1,"steps":null}`),
		"unknown plan field": JSON(`{"version":1,"steps":[],"instructions":"ignore"}`),
		"unknown step field": JSON(`{"version":1,"steps":[{"provider_type":"mcp","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"lookup","request":{},"retry":true}]}`),
		"credential":         JSON(`{"version":1,"steps":[{"provider_type":"mcp","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"lookup","request":{"nested":[{"Access-Token":"secret"}]}}]}`),
		"duplicate":          JSON(`{"version":1,"steps":[{"provider_type":"mcp","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"lookup","request":{}},{"provider_type":"mcp","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"lookup","request":{}}]}`),
		"too many steps":     JSON(`{"version":1,"steps":[` + strings.Repeat(`{"provider_type":"skill","provider_id":"baseline","tool_name":"load","request":{}},`, 32) + `{"provider_type":"skill","provider_id":"baseline","tool_name":"load","request":{}}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CanonicalProductionWorkflowPlanSnapshot(raw)
			require.Error(t, err)
		})
	}
}

func TestProductionRunEnumsAcceptOnlySchemaValues(t *testing.T) {
	for _, runType := range []ProductionRunType{
		ProductionRunCollect, ProductionRunWrite, ProductionRunRewrite, ProductionRunValidate,
	} {
		if !runType.IsValid() {
			t.Fatalf("run type %q must be valid", runType)
		}
	}
	if ProductionRunType("interactive").IsValid() {
		t.Fatal("unexpected run type accepted")
	}

	for _, status := range []ProductionRunStatus{
		ProductionRunQueued, ProductionRunRunning, ProductionRunWaitingApproval,
		ProductionRunCompleted, ProductionRunFailed, ProductionRunCancelled,
	} {
		if !status.IsValid() {
			t.Fatalf("run status %q must be valid", status)
		}
	}
	if ProductionRunStatus("paused").IsValid() {
		t.Fatal("unexpected run status accepted")
	}

	for _, provider := range []ProductionToolProviderType{
		ProductionToolProviderSkill, ProductionToolProviderMCP, ProductionToolProviderDatasource,
	} {
		if !provider.IsValid() {
			t.Fatalf("provider %q must be valid", provider)
		}
	}
	if ProductionToolProviderType("shell").IsValid() {
		t.Fatal("unexpected provider accepted")
	}

	for _, status := range []ProductionToolCallStatus{
		ProductionToolCallPlanned, ProductionToolCallPendingApproval, ProductionToolCallApproved,
		ProductionToolCallRejected, ProductionToolCallExecuting, ProductionToolCallCompleted, ProductionToolCallFailed,
	} {
		if !status.IsValid() {
			t.Fatalf("tool-call status %q must be valid", status)
		}
	}
	if ProductionToolCallStatus("cancelled").IsValid() {
		t.Fatal("unexpected tool-call status accepted")
	}

	for _, status := range []ProductionToolApprovalStatus{
		ProductionToolApprovalNotRequired, ProductionToolApprovalPending,
		ProductionToolApprovalApproved, ProductionToolApprovalRejected,
	} {
		if !status.IsValid() {
			t.Fatalf("approval status %q must be valid", status)
		}
	}
	if ProductionToolApprovalStatus("skipped").IsValid() {
		t.Fatal("unexpected approval status accepted")
	}
}

func TestProductionRunPayloadRequiresCanonicalScopedIdentity(t *testing.T) {
	validID := uuid.NewString()
	payload := ProductionRunPayload{TenantID: 9, RunID: validID, Attempt: 1}
	if err := payload.Validate(); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}

	for _, payload := range []ProductionRunPayload{
		{TenantID: 0, RunID: validID, Attempt: 1},
		{TenantID: 9, RunID: "not-a-uuid", Attempt: 1},
		{TenantID: 9, RunID: "{" + validID + "}", Attempt: 1},
		{TenantID: 9, RunID: validID, Attempt: 0},
	} {
		if err := payload.Validate(); err == nil {
			t.Fatalf("invalid payload accepted: %+v", payload)
		}
	}
}

func TestProductionRunPayloadJSONRoundTripContainsOnlyTransportContext(t *testing.T) {
	payload := ProductionRunPayload{
		TracingContext: TracingContext{LangfuseTraceID: "trace-1"},
		TenantID:       9,
		RunID:          uuid.NewString(),
		Attempt:        2,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tenant_id", "run_id", "attempt", "lf_trace_id"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("payload JSON missing %q: %s", name, encoded)
		}
	}
	for _, forbidden := range []string{"model_id", "state_payload", "document_type_snapshot", "raw_model_response", "request_snapshot", "response_snapshot", "secret", "token"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("payload JSON must not contain %q: %s", forbidden, encoded)
		}
	}

	var decoded ProductionRunPayload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != payload {
		t.Fatalf("payload round trip = %+v, want %+v", decoded, payload)
	}
}

func TestProductionRunAndToolCallSchemaTagsMatchMigration(t *testing.T) {
	assertFieldTag := func(structType reflect.Type, field, jsonTag, gormTag string) {
		t.Helper()
		value, ok := structType.FieldByName(field)
		if !ok {
			t.Fatalf("%s is missing %s", structType.Name(), field)
		}
		if got := value.Tag.Get("json"); got != jsonTag {
			t.Fatalf("%s.%s json tag = %q, want %q", structType.Name(), field, got, jsonTag)
		}
		if got := value.Tag.Get("gorm"); got != gormTag {
			t.Fatalf("%s.%s gorm tag = %q, want %q", structType.Name(), field, got, gormTag)
		}
	}

	runType := reflect.TypeOf(ProductionRun{})
	assertFieldTag(runType, "OutputVersionID", "output_version_id,omitempty", "type:varchar(36)")
	assertFieldTag(runType, "RawModelResponseDigest", "raw_model_response_digest,omitempty", "type:varchar(64)")
	assertFieldTag(runType, "WakeupVersion", "wakeup_version", "not null;default:0")
	assertFieldTag(runType, "WakeupEnqueuedVersion", "wakeup_enqueued_version", "not null;default:0")
	assertFieldTag(runType, "WorkflowPlanDigest", "workflow_plan_digest", "type:varchar(64);not null")
	assertFieldTag(runType, "CompletedAt", "completed_at,omitempty", "")

	callType := reflect.TypeOf(ProductionToolCall{})
	assertFieldTag(callType, "ResponseDigest", "response_digest,omitempty", "type:varchar(64)")
	assertFieldTag(callType, "ResponseEvidenceID", "response_evidence_id,omitempty", "type:varchar(36)")
	assertFieldTag(callType, "ApprovalRequestedAt", "approval_requested_at,omitempty", "")
	assertFieldTag(callType, "CompletedAt", "completed_at,omitempty", "")

	evidenceType := reflect.TypeOf(ProductionEvidenceSnapshot{})
	assertFieldTag(evidenceType, "CapturedByToolCallID", "captured_by_tool_call_id,omitempty", "type:varchar(36)")
}

func TestProductionToolEvidenceIDBindsExactPersistedInvocation(t *testing.T) {
	call := &ProductionToolCall{
		ID: "71000000-0000-4000-8000-000000000001", Attempt: 2,
		ProviderType: ProductionToolProviderMCP, ProviderID: "provider-1", ToolName: "lookup",
	}
	first, err := ProductionToolEvidenceID(call)
	require.NoError(t, err)
	second, err := ProductionToolEvidenceID(call)
	require.NoError(t, err)
	require.Equal(t, first, second)
	parsed, err := uuid.Parse(first)
	require.NoError(t, err)
	require.Equal(t, first, parsed.String())

	mutations := []*ProductionToolCall{
		{ID: "71000000-0000-4000-8000-000000000002", Attempt: 2, ProviderType: ProductionToolProviderMCP, ProviderID: "provider-1", ToolName: "lookup"},
		{ID: call.ID, Attempt: 3, ProviderType: ProductionToolProviderMCP, ProviderID: "provider-1", ToolName: "lookup"},
		{ID: call.ID, Attempt: 2, ProviderType: ProductionToolProviderDatasource, ProviderID: "provider-1", ToolName: "lookup"},
		{ID: call.ID, Attempt: 2, ProviderType: ProductionToolProviderMCP, ProviderID: "provider-2", ToolName: "lookup"},
		{ID: call.ID, Attempt: 2, ProviderType: ProductionToolProviderMCP, ProviderID: "provider-1", ToolName: "other"},
	}
	for _, mutated := range mutations {
		got, err := ProductionToolEvidenceID(mutated)
		require.NoError(t, err)
		require.NotEqual(t, first, got)
	}

	_, err = ProductionToolEvidenceID(nil)
	require.Error(t, err)
	_, err = ProductionToolEvidenceID(&ProductionToolCall{ID: "not-a-uuid", ProviderType: ProductionToolProviderMCP, ProviderID: "provider", ToolName: "lookup", Attempt: 1})
	require.Error(t, err)
}
