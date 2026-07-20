package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ProductionValidator validates one authoritative persisted version without creating a new version.
type ProductionValidator struct {
	documents interfaces.ProductionDocumentRepository
	sources   interfaces.ProductionSourceRepository
	resources interfaces.ResourceCatalog
}

func NewProductionValidator(documents interfaces.ProductionDocumentRepository, sources interfaces.ProductionSourceRepository, resources interfaces.ResourceCatalog) *ProductionValidator {
	return &ProductionValidator{documents: documents, sources: sources, resources: resources}
}

func (v *ProductionValidator) Validate(ctx context.Context, run *types.ProductionRun) error {
	if v == nil || v.documents == nil || v.sources == nil {
		return errors.New("production validator dependencies are required")
	}
	if run == nil || run.RunType != types.ProductionRunValidate || run.Status != types.ProductionRunRunning || run.TenantID == 0 || run.ProjectID == "" || run.DocumentID == "" || run.SourceSetID == "" {
		return errors.New("production validator run scope is invalid")
	}
	principal, ok := types.ProductionInternalPrincipalFromContext(ctx)
	if !ok || !principal.Matches(run.TenantID, run.ProjectID, run.ID) {
		return types.ErrProductionForbidden
	}
	documentType, err := decodeProductionWriterDocumentType(run.DocumentTypeSnapshot)
	if err != nil {
		return err
	}
	document, err := v.documents.GetDocument(ctx, run.TenantID, string(run.DocumentID))
	if err != nil {
		return err
	}
	if document == nil || document.ID != string(run.DocumentID) || document.TenantID != run.TenantID || document.ProjectID != run.ProjectID || document.DocumentTypeID != documentType.ID || document.DocumentTypeSchemaVersion != documentType.SchemaVersion {
		return errors.New("production validator document scope is invalid")
	}
	versionID := ""
	if run.InputVersionID != nil {
		versionID = *run.InputVersionID
	} else if document.CurrentVersionID != nil {
		versionID = *document.CurrentVersionID
	}
	if versionID == "" {
		return errors.New("production validator input version is required")
	}
	version, err := v.documents.GetVersion(ctx, run.TenantID, versionID)
	if err != nil {
		return err
	}
	if version == nil || version.ID != versionID || version.DocumentID != document.ID || version.TenantID != run.TenantID || version.ProjectID != run.ProjectID || version.SourceSetID != run.SourceSetID {
		return errors.New("production validator version scope is invalid")
	}
	version.DocumentTypeCode = documentType.Code
	sourceSet, err := v.sources.GetSet(ctx, run.TenantID, run.SourceSetID)
	if err != nil {
		return err
	}
	if sourceSet == nil || sourceSet.ID != run.SourceSetID || sourceSet.TenantID != run.TenantID || sourceSet.ProjectID != run.ProjectID || sourceSet.DocumentTypeID != documentType.ID || sourceSet.Status != types.ProductionSourceSetFrozen {
		return types.ErrProductionDocumentSourceSetInvalid
	}
	evidence, err := v.sources.ListAcceptedEvidence(ctx, run.TenantID, run.ProjectID, run.SourceSetID)
	if err != nil {
		return err
	}
	accepted := make(map[string]struct{}, len(evidence))
	evidenceByID := make(map[string]*types.ProductionEvidenceSnapshot, len(evidence))
	for _, snapshot := range evidence {
		if snapshot == nil || snapshot.ID == "" {
			return &ProductionDocumentValidationError{Cause: errors.New("accepted evidence snapshot is invalid")}
		}
		copy := *snapshot
		if copy.StoragePath != "" {
			if v.resources == nil {
				return &ProductionDocumentValidationError{Cause: types.ErrProductionEvidenceResourceInvalid}
			}
			resource, resolveErr := v.resources.ResolveBound(ctx, copy.StoragePath, interfaces.ResourceBindingRequirement{TenantID: run.TenantID, OwnerType: types.ResourceOwnerTypeProductionProject, OwnerID: run.ProjectID})
			if resolveErr != nil || resource == nil || resource.TenantID != run.TenantID || resource.State != types.ResourceStateActive || resource.Lifecycle != types.ResourceLifecyclePersistent {
				return &ProductionDocumentValidationError{Cause: types.ErrProductionEvidenceResourceInvalid}
			}
			copy.ResolvedContentDigest = resource.ContentHash
		}
		accepted[copy.ID] = struct{}{}
		evidenceByID[copy.ID] = &copy
	}
	validation := ValidateProductionVersion(version, accepted)
	if len(validation.Errors) != 0 {
		return &ProductionDocumentValidationError{Issues: validation.Errors}
	}
	if err := VerifyProductionVersionDigests(version, evidenceByID); err != nil {
		return &ProductionDocumentValidationError{Cause: err}
	}
	return nil
}
