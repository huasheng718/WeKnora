package service

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func productionValidatorFixture(t *testing.T) (*ProductionValidator, *types.ProductionRun, *productionWriterDocumentRepoStub, *productionWriterSourceRepoStub) {
	t.Helper()
	inputs := make([]types.ProductionDocumentBlockInput, 0)
	for index, section := range BuiltinSoftwareDevelopmentBaseline().RequiredSections {
		inputs = append(inputs, types.ProductionDocumentBlockInput{
			LogicalBlockID: fmt.Sprintf("section-%02d", index), BlockType: "heading",
			Content: types.JSON(strconv.Quote(section)), Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
		})
	}
	blocks, err := buildProductionDocumentBlocks(inputs)
	require.NoError(t, err)
	version := &types.ProductionDocumentVersion{
		ID: writerVersionID, TenantID: 7, ProjectID: writerProjectID, DocumentID: writerDocumentID,
		SourceSetID: writerSourceID, Origin: types.ProductionDocumentOriginHuman, Blocks: blocks,
		DocumentTypeCode: "software-development-baseline",
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	documents := &productionWriterDocumentRepoStub{
		document: &types.ProductionDocument{
			ID: writerDocumentID, TenantID: 7, ProjectID: writerProjectID, DocumentTypeID: writerTypeID,
			DocumentTypeSchemaVersion: 3, CurrentVersionID: stringPointer(writerVersionID),
		},
		version: version,
	}
	sources := &productionWriterSourceRepoStub{
		set: &types.ProductionSourceSet{ID: writerSourceID, TenantID: 7, ProjectID: writerProjectID, DocumentTypeID: writerTypeID, Status: types.ProductionSourceSetFrozen},
		evidence: []*types.ProductionEvidenceSnapshot{{
			ID: writerEvidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
			InlineContent: types.JSON(`"accepted"`), ContentDigest: productionToolDigest(types.JSON(`"accepted"`)),
		}},
	}
	run := &types.ProductionRun{
		ID: mcpAdapterRunID, TenantID: 7, ProjectID: writerProjectID, DocumentID: writerDocumentID,
		SourceSetID: writerSourceID, RunType: types.ProductionRunValidate, Status: types.ProductionRunRunning,
		Attempt: 1, InputVersionID: stringPointer(writerVersionID),
		DocumentTypeSnapshot: types.JSON(`{"id":"` + writerTypeID + `","code":"software-development-baseline","schema_version":3,"block_schema":{},"skill_bindings":{"skills":[],"version":1}}`),
	}
	return NewProductionValidator(documents, sources, nil), run, documents, sources
}

func TestProductionValidatorValidatesAuthoritativeVersionAndEvidence(t *testing.T) {
	validator, run, _, _ := productionValidatorFixture(t)
	require.NoError(t, validator.Validate(productionWriterTestContext(t, run), run))
}

func TestProductionValidatorRejectsInvalidDigestGroundingAndScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*types.ProductionRun, *productionWriterDocumentRepoStub, *productionWriterSourceRepoStub)
	}{
		{name: "corrupt digest", mutate: func(_ *types.ProductionRun, documents *productionWriterDocumentRepoStub, _ *productionWriterSourceRepoStub) {
			documents.version.Blocks[0].ContentDigest = "bad"
		}},
		{name: "unknown evidence", mutate: func(_ *types.ProductionRun, documents *productionWriterDocumentRepoStub, _ *productionWriterSourceRepoStub) {
			block := *documents.version.Blocks[0]
			block.LogicalBlockID = "claim"
			block.BlockType = "paragraph"
			block.Content = types.JSON(`"claim"`)
			block.Attributes = types.JSON(`{"factual":true,"needs_confirmation":false}`)
			block.EvidenceRefs = types.JSON(`["missing"]`)
			block.ContentDigest = types.ComputeProductionBlockDigest(&block)
			documents.version.Blocks = append(documents.version.Blocks, &block)
			documents.version.ContentDigest = types.ComputeProductionVersionDigest(documents.version)
		}},
		{name: "ungrounded fact", mutate: func(_ *types.ProductionRun, documents *productionWriterDocumentRepoStub, _ *productionWriterSourceRepoStub) {
			block := *documents.version.Blocks[0]
			block.LogicalBlockID = "claim"
			block.BlockType = "paragraph"
			block.Content = types.JSON(`"claim"`)
			block.Attributes = types.JSON(`{"factual":true,"needs_confirmation":false}`)
			block.EvidenceRefs = types.JSON(`[]`)
			block.ContentDigest = types.ComputeProductionBlockDigest(&block)
			documents.version.Blocks = append(documents.version.Blocks, &block)
			documents.version.ContentDigest = types.ComputeProductionVersionDigest(documents.version)
		}},
		{name: "project mismatch", mutate: func(run *types.ProductionRun, _ *productionWriterDocumentRepoStub, _ *productionWriterSourceRepoStub) {
			run.ProjectID = "70000000-0000-4000-8000-000000000099"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			validator, run, documents, sources := productionValidatorFixture(t)
			test.mutate(run, documents, sources)
			ctx := productionWriterTestContext(t, run)
			err := validator.Validate(ctx, run)
			require.Error(t, err)
		})
	}
}

func TestProductionValidatorRejectsMismatchedInternalPrincipal(t *testing.T) {
	validator, run, _, _ := productionValidatorFixture(t)
	ctx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: run.TenantID, ProjectID: run.ProjectID, RunID: "70000000-0000-4000-8000-000000000099",
	})
	require.NoError(t, err)
	require.ErrorIs(t, validator.Validate(ctx, run), types.ErrProductionForbidden)
}
