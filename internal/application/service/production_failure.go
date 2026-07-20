package service

import (
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
)

type productionFailureMetadata struct {
	code    string
	message string
}

var productionFailureByCode = map[string]productionFailureMetadata{
	"STEP_EXECUTION_FAILED":                  {code: "STEP_EXECUTION_FAILED", message: "production step execution failed"},
	"TOOL_EXECUTION_FAILED":                  {code: "TOOL_EXECUTION_FAILED", message: "production tool execution failed"},
	"TOOL_RECONCILIATION_REQUIRED":           {code: "TOOL_RECONCILIATION_REQUIRED", message: "production tool execution requires reconciliation"},
	"TOOL_CALL_INVALID":                      {code: "TOOL_CALL_INVALID", message: "production tool call is invalid"},
	"TOOL_CALL_SCOPE_INVALID":                {code: "TOOL_CALL_SCOPE_INVALID", message: "production tool call is outside its run scope"},
	"PROVIDER_EXECUTION_FAILED":              {code: "PROVIDER_EXECUTION_FAILED", message: "production provider execution failed"},
	"PROVIDER_OUTPUT_UNSAFE":                 {code: "PROVIDER_OUTPUT_UNSAFE", message: "production provider output is unsafe"},
	"PROVIDER_CONFIGURATION_INVALID":         {code: "PROVIDER_CONFIGURATION_INVALID", message: "production provider configuration is invalid"},
	"PROVIDER_DIGEST_MISMATCH":               {code: "PROVIDER_DIGEST_MISMATCH", message: "production provider digest does not match the pinned request"},
	"WRITER_CONFIGURATION_INVALID":           {code: "WRITER_CONFIGURATION_INVALID", message: "production writer configuration is invalid"},
	"WRITER_SCOPE_INVALID":                   {code: "WRITER_SCOPE_INVALID", message: "production writer input is outside the governed run scope"},
	"WRITER_OUTPUT_INVALID":                  {code: "WRITER_OUTPUT_INVALID", message: "production writer model output is invalid"},
	"WRITER_AUDIT_CONFLICT":                  {code: "WRITER_AUDIT_CONFLICT", message: "production writer raw response audit lost its run fence"},
	"WRITER_EVIDENCE_NORMALIZATION_REQUIRED": {code: "WRITER_EVIDENCE_NORMALIZATION_REQUIRED", message: "production writer requires inline normalized evidence"},
	"WRITER_INPUT_LIMIT_EXCEEDED":            {code: "WRITER_INPUT_LIMIT_EXCEEDED", message: "production writer input exceeds size limit"},
	"WRITER_OUTPUT_LIMIT_EXCEEDED":           {code: "WRITER_OUTPUT_LIMIT_EXCEEDED", message: "production writer output exceeds size limit"},
	"DOCUMENT_VALIDATION_FAILED":             {code: "DOCUMENT_VALIDATION_FAILED", message: "production document version failed governed validation"},
	"DOCUMENT_STALE_PARENT":                  {code: "DOCUMENT_STALE_PARENT", message: "production document parent is not the current head"},
	"DOCUMENT_SOURCE_SET_INVALID":            {code: "DOCUMENT_SOURCE_SET_INVALID", message: "production document source set is invalid"},
	"PRODUCTION_CONFLICT":                    {code: "PRODUCTION_CONFLICT", message: "production resource conflict"},
	"PRODUCTION_FORBIDDEN":                   {code: "PRODUCTION_FORBIDDEN", message: "production operation forbidden"},
}

func productionExecutionFailure(err error) productionFailureMetadata {
	for _, candidate := range []struct {
		target error
		code   string
	}{
		{types.ErrProductionToolReconciliationRequired, "TOOL_RECONCILIATION_REQUIRED"},
		{errProductionToolCallInvalid, "TOOL_CALL_INVALID"},
		{errProductionToolScope, "TOOL_CALL_SCOPE_INVALID"},
		{errProductionProviderExecution, "PROVIDER_EXECUTION_FAILED"},
		{errProductionProviderOutputUnsafe, "PROVIDER_OUTPUT_UNSAFE"},
		{errProductionProviderConfiguration, "PROVIDER_CONFIGURATION_INVALID"},
		{errProductionProviderDigestMismatch, "PROVIDER_DIGEST_MISMATCH"},
		{errProductionWriterConfiguration, "WRITER_CONFIGURATION_INVALID"},
		{errProductionWriterScope, "WRITER_SCOPE_INVALID"},
		{errProductionWriterOutput, "WRITER_OUTPUT_INVALID"},
		{errProductionWriterAuditConflict, "WRITER_AUDIT_CONFLICT"},
		{errProductionWriterEvidenceNormalization, "WRITER_EVIDENCE_NORMALIZATION_REQUIRED"},
		{errProductionWriterInputLimit, "WRITER_INPUT_LIMIT_EXCEEDED"},
		{errProductionWriterOutputLimit, "WRITER_OUTPUT_LIMIT_EXCEEDED"},
		{types.ErrProductionDocumentValidation, "DOCUMENT_VALIDATION_FAILED"},
		{types.ErrProductionDocumentStaleParent, "DOCUMENT_STALE_PARENT"},
		{types.ErrProductionDocumentSourceSetInvalid, "DOCUMENT_SOURCE_SET_INVALID"},
		{types.ErrProductionConflict, "PRODUCTION_CONFLICT"},
		{types.ErrProductionForbidden, "PRODUCTION_FORBIDDEN"},
	} {
		if errors.Is(err, candidate.target) {
			return productionFailureByCode[candidate.code]
		}
	}
	return productionFailureByCode["STEP_EXECUTION_FAILED"]
}

func productionToolResultFailure(code string) productionFailureMetadata {
	switch code {
	case "TOOL_EXECUTION_FAILED", "TOOL_RECONCILIATION_REQUIRED", "TOOL_CALL_INVALID", "TOOL_CALL_SCOPE_INVALID",
		"PROVIDER_EXECUTION_FAILED", "PROVIDER_OUTPUT_UNSAFE", "PROVIDER_CONFIGURATION_INVALID", "PROVIDER_DIGEST_MISMATCH":
		return productionFailureByCode[code]
	default:
		return productionFailureByCode["TOOL_EXECUTION_FAILED"]
	}
}
