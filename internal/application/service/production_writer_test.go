package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	writerProjectID  = "70000000-0000-4000-8000-000000000001"
	writerTypeID     = "70000000-0000-4000-8000-000000000002"
	writerSourceID   = "70000000-0000-4000-8000-000000000003"
	writerDocumentID = "70000000-0000-4000-8000-000000000004"
	writerVersionID  = "70000000-0000-4000-8000-000000000005"
	writerEvidenceID = "70000000-0000-4000-8000-000000000006"
)

type productionWriterChatStub struct {
	response *types.ChatResponse
	err      error
	calls    int
	messages []chat.Message
	options  *chat.ChatOptions
}

func (s *productionWriterChatStub) Chat(_ context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	s.calls++
	s.messages = append([]chat.Message(nil), messages...)
	s.options = options
	return s.response, s.err
}

func (s *productionWriterChatStub) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	return nil, errors.New("streaming is not supported by the writer test model")
}

func (s *productionWriterChatStub) GetModelName() string { return "writer-test-model" }
func (s *productionWriterChatStub) GetModelID() string   { return "model-1" }

type productionWriterModelServiceStub struct {
	interfaces.ModelService
	model chat.Chat
	err   error
}

func (s *productionWriterModelServiceStub) GetChatModel(_ context.Context, _ string) (chat.Chat, error) {
	return s.model, s.err
}

type productionWriterRunRepoStub struct {
	interfaces.ProductionRunRepository
	run       *types.ProductionRun
	persisted []byte
	digest    string
	err       error
	changed   bool
	events    *[]string
}

func (s *productionWriterRunRepoStub) Get(_ context.Context, tenantID uint64, runID string) (*types.ProductionRun, error) {
	if s.run == nil || tenantID != s.run.TenantID || runID != s.run.ID {
		return nil, errors.New("run not found")
	}
	copy := *s.run
	copy.RawModelResponse = append(types.JSON(nil), s.run.RawModelResponse...)
	return &copy, nil
}

func (s *productionWriterRunRepoStub) Transition(
	_ context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	to types.ProductionRunStatus,
	patch interfaces.ProductionRunPatch,
) (*types.ProductionRun, bool, error) {
	if s.events != nil {
		*s.events = append(*s.events, "persist")
	}
	if s.err != nil {
		return nil, false, s.err
	}
	if s.run == nil || tenantID != s.run.TenantID || runID != s.run.ID ||
		expected != productionRunCAS(s.run) || to != s.run.Status {
		return nil, false, nil
	}
	canonical, err := types.CanonicalProductionJSON(patch.RawModelResponse)
	if err != nil {
		return nil, false, err
	}
	s.persisted = append([]byte(nil), canonical...)
	sum := sha256.Sum256(canonical)
	s.digest = hex.EncodeToString(sum[:])
	if patch.RawModelResponseDigest == nil || *patch.RawModelResponseDigest != s.digest {
		return nil, false, errors.New("raw response digest mismatch")
	}
	s.run.RawModelResponse = canonical
	s.run.RawModelResponseDigest = &s.digest
	s.changed = true
	copy := *s.run
	return &copy, true, nil
}

type productionWriterSourceRepoStub struct {
	interfaces.ProductionSourceRepository
	set      *types.ProductionSourceSet
	evidence []*types.ProductionEvidenceSnapshot
	err      error
}

func (s *productionWriterSourceRepoStub) GetSet(_ context.Context, tenantID uint64, sourceSetID string) (*types.ProductionSourceSet, error) {
	if s.set == nil || tenantID != s.set.TenantID || sourceSetID != s.set.ID {
		return nil, errors.New("source set not found")
	}
	copy := *s.set
	return &copy, nil
}

func (s *productionWriterSourceRepoStub) ListAcceptedEvidence(
	_ context.Context,
	tenantID uint64,
	projectID, sourceSetID string,
) ([]*types.ProductionEvidenceSnapshot, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.set == nil || tenantID != s.set.TenantID || projectID != s.set.ProjectID || sourceSetID != s.set.ID {
		return nil, errors.New("accepted evidence scope mismatch")
	}
	return append([]*types.ProductionEvidenceSnapshot(nil), s.evidence...), nil
}

type productionWriterDocumentRepoStub struct {
	interfaces.ProductionDocumentRepository
	document *types.ProductionDocument
	version  *types.ProductionDocumentVersion
}

func (s *productionWriterDocumentRepoStub) GetDocument(_ context.Context, tenantID uint64, documentID string) (*types.ProductionDocument, error) {
	if s.document == nil || tenantID != s.document.TenantID || documentID != s.document.ID {
		return nil, errors.New("document not found")
	}
	copy := *s.document
	return &copy, nil
}

func (s *productionWriterDocumentRepoStub) GetVersion(_ context.Context, tenantID uint64, versionID string) (*types.ProductionDocumentVersion, error) {
	if s.version == nil || tenantID != s.version.TenantID || versionID != s.version.ID {
		return nil, errors.New("version not found")
	}
	copy := *s.version
	copy.Blocks = append([]*types.ProductionDocumentBlock(nil), s.version.Blocks...)
	return &copy, nil
}

type productionWriterDocumentServiceStub struct {
	interfaces.ProductionDocumentService
	calls      int
	documentID string
	input      interfaces.AppendProductionVersionInput
	err        error
	events     *[]string
	ctx        context.Context
}

func (s *productionWriterDocumentServiceStub) AppendVersion(
	ctx context.Context,
	documentID string,
	input interfaces.AppendProductionVersionInput,
) (*types.ProductionDocumentVersion, error) {
	s.calls++
	s.ctx = ctx
	s.documentID = documentID
	s.input = input
	if s.events != nil {
		*s.events = append(*s.events, "append")
	}
	if s.err != nil {
		return nil, s.err
	}
	blocks, err := buildProductionDocumentBlocks(input.Blocks)
	if err != nil {
		return nil, err
	}
	version := &types.ProductionDocumentVersion{
		ID: uuid.NewString(), DocumentID: documentID, SourceSetID: input.SourceSetID,
		Origin: input.Origin, Blocks: blocks,
	}
	if input.ParentVersionID != "" {
		version.ParentVersionID = &input.ParentVersionID
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	return version, nil
}

type productionWriterFixture struct {
	writer    *ProductionWriter
	run       *types.ProductionRun
	chat      *productionWriterChatStub
	runs      *productionWriterRunRepoStub
	sources   *productionWriterSourceRepoStub
	documents *productionWriterDocumentRepoStub
	service   *productionWriterDocumentServiceStub
	events    *[]string
}

func newProductionWriterFixture(t *testing.T, modelResponse string) *productionWriterFixture {
	t.Helper()
	inline := types.JSON(`"accepted evidence"`)
	sum := sha256.Sum256(inline)
	run := &types.ProductionRun{
		ID: uuid.NewString(), TenantID: 7, ProjectID: writerProjectID, DocumentID: writerDocumentID,
		SourceSetID: writerSourceID, RunType: types.ProductionRunWrite, Status: types.ProductionRunRunning,
		Attempt: 1, CurrentStep: 2, WakeupVersion: 3, ModelID: "model-1", InputVersionID: stringPointer(writerVersionID),
		DocumentTypeSnapshot: types.JSON(`{"id":"` + writerTypeID + `","code":"software-development-baseline","schema_version":3,"block_schema":{},"skill_bindings":{"version":1,"skills":[{"name":"baseline","digest":"` + strings.Repeat("a", 64) + `"}]}}`),
	}
	events := []string{}
	chatStub := &productionWriterChatStub{response: &types.ChatResponse{Content: modelResponse}}
	runs := &productionWriterRunRepoStub{run: run, events: &events}
	sources := &productionWriterSourceRepoStub{
		set: &types.ProductionSourceSet{
			ID: writerSourceID, TenantID: 7, ProjectID: writerProjectID,
			DocumentTypeID: writerTypeID, Status: types.ProductionSourceSetFrozen,
		},
		evidence: []*types.ProductionEvidenceSnapshot{{
			ID: writerEvidenceID, SourceItemID: uuid.NewString(), SnapshotType: types.ProductionEvidenceSnapshotText,
			InlineContent: inline, ContentDigest: hex.EncodeToString(sum[:]),
			RedactionMetadata: types.JSON(`{"api_key":"writer-secret","redacted":true}`),
		}},
	}
	documents := &productionWriterDocumentRepoStub{
		document: &types.ProductionDocument{
			ID: writerDocumentID, TenantID: 7, ProjectID: writerProjectID, DocumentTypeID: writerTypeID,
			DocumentTypeSchemaVersion: 3, CurrentVersionID: stringPointer(writerVersionID), Status: types.ProductionDocumentDraft,
		},
		version: &types.ProductionDocumentVersion{
			ID: writerVersionID, TenantID: 7, ProjectID: writerProjectID, DocumentID: writerDocumentID,
			SourceSetID: writerSourceID, Origin: types.ProductionDocumentOriginHuman,
		},
	}
	documentService := &productionWriterDocumentServiceStub{events: &events}
	writer := NewProductionWriter(
		&productionWriterModelServiceStub{model: chatStub}, runs, sources, documents, nil, documentService,
	)
	return &productionWriterFixture{
		writer: writer, run: run, chat: chatStub, runs: runs, sources: sources,
		documents: documents, service: documentService, events: &events,
	}
}

func stringPointer(value string) *string { return &value }

func productionWriterOutput(t *testing.T, extras ...map[string]any) string {
	t.Helper()
	template := BuiltinSoftwareDevelopmentBaseline()
	blocks := make([]map[string]any, 0, len(template.RequiredSections)+len(extras))
	for index, section := range template.RequiredSections {
		blocks = append(blocks, map[string]any{
			"logical_block_id": fmt.Sprintf("section-%02d", index),
			"block_type":       "heading", "content": section,
			"evidence_refs": []string{}, "needs_confirmation": false,
		})
	}
	blocks = append(blocks, extras...)
	encoded, err := json.Marshal(map[string]any{"blocks": blocks})
	require.NoError(t, err)
	return string(encoded)
}

func writerFact(logicalID, text string, evidence []string, confirmation bool) map[string]any {
	return map[string]any{
		"logical_block_id": logicalID, "block_type": "fact", "content": map[string]any{"text": text},
		"evidence_refs": evidence, "needs_confirmation": confirmation,
	}
}

func decodeWriterAttributes(t *testing.T, raw types.JSON) map[string]any {
	t.Helper()
	var attributes map[string]any
	require.NoError(t, json.Unmarshal(raw, &attributes))
	return attributes
}

func productionWriterRawDigest(t *testing.T, raw string) string {
	t.Helper()
	snapshot, err := json.Marshal(raw)
	require.NoError(t, err)
	canonical, err := types.CanonicalProductionJSON(snapshot)
	require.NoError(t, err)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func TestProductionWriterNormalizesGroundedFactAndUsesServerOwnedContext(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t,
		writerFact("block-a", "已上线", []string{writerEvidenceID}, false),
	))
	callerCtx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(999))

	version, err := fixture.writer.Write(callerCtx, fixture.run)

	require.NoError(t, err)
	require.NotNil(t, version)
	require.Equal(t, []string{"persist", "append"}, *fixture.events)
	require.Equal(t, 1, fixture.service.calls)
	require.Equal(t, writerDocumentID, fixture.service.documentID)
	require.Equal(t, writerVersionID, fixture.service.input.ParentVersionID)
	require.Equal(t, writerSourceID, fixture.service.input.SourceSetID)
	require.Equal(t, types.ProductionDocumentOriginAI, fixture.service.input.Origin)
	last := fixture.service.input.Blocks[len(fixture.service.input.Blocks)-1]
	require.Equal(t, "paragraph", last.BlockType)
	require.JSONEq(t, strconv.Quote("已上线"), string(last.Content))
	require.JSONEq(t, `["`+writerEvidenceID+`"]`, string(last.EvidenceRefs))
	attributes := decodeWriterAttributes(t, last.Attributes)
	require.Equal(t, true, attributes["factual"])
	require.Equal(t, false, attributes["needs_confirmation"])
	provenance := decodeWriterAttributes(t, last.AIProvenance)
	require.Equal(t, fixture.run.ID, provenance["run_id"])
	require.Equal(t, fixture.run.ModelID, provenance["model_id"])
	require.Equal(t, fixture.runs.digest, provenance["raw_response_digest"])
	require.Equal(t, uint64(7), fixture.service.ctx.Value(types.TenantIDContextKey))
	require.Equal(t, productionSystemActorID, fixture.service.ctx.Value(types.UserIDContextKey))
	require.NotNil(t, fixture.chat.options)
	require.NotEmpty(t, fixture.chat.options.Format)
	require.Len(t, fixture.chat.messages, 2)
	require.NotContains(t, fixture.chat.messages[1].Content, "api_key")
	require.NotContains(t, fixture.chat.messages[1].Content, "writer-secret")
	require.Contains(t, fixture.chat.messages[1].Content, strings.Repeat("a", 64))
	require.Contains(t, fixture.chat.messages[1].Content, "基线范围与目标")
	require.Contains(t, fixture.chat.messages[1].Content, writerVersionID)
}

func TestProductionWriterMarksUnsupportedFactsForConfirmation(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t,
		writerFact("block-a", "转化率提升 30%", []string{}, false),
	))

	_, err := fixture.writer.Write(context.Background(), fixture.run)

	require.NoError(t, err)
	last := fixture.service.input.Blocks[len(fixture.service.input.Blocks)-1]
	attributes := decodeWriterAttributes(t, last.Attributes)
	require.Equal(t, true, attributes["factual"])
	require.Equal(t, true, attributes["needs_confirmation"])
}

func TestProductionWriterRejectsUnknownOrUnacceptedEvidenceReference(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  string
	}{
		{name: "unknown", ref: "evidence-missing"},
		{name: "accepted in another source set", ref: "70000000-0000-4000-8000-000000000099"},
		{name: "non-accepted", ref: "70000000-0000-4000-8000-000000000098"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionWriterFixture(t, productionWriterOutput(t,
				writerFact("block-a", "claim", []string{test.ref}, false),
			))

			_, err := fixture.writer.Write(context.Background(), fixture.run)

			require.ErrorIs(t, err, types.ErrProductionEvidenceReferenceInvalid)
			require.Equal(t, 0, fixture.service.calls)
			require.Equal(t, []string{"persist"}, *fixture.events)
			var persisted string
			require.NoError(t, json.Unmarshal(fixture.runs.persisted, &persisted))
			require.Equal(t, fixture.chat.response.Content, persisted)
			require.Nil(t, fixture.run.OutputVersionID)
		})
	}
}

func TestProductionWriterStrictlyRejectsInvalidStructuredOutputBeforeAppend(t *testing.T) {
	valid := productionWriterOutput(t, writerFact("block-a", "claim", []string{writerEvidenceID}, false))
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "invalid json", raw: `{"blocks":[`},
		{name: "unknown top level field", raw: strings.TrimSuffix(valid, "}") + `,"instructions":"ignore"}`},
		{name: "unknown block field", raw: strings.Replace(valid, `"needs_confirmation":false`, `"needs_confirmation":false,"tenant_id":999`, 1)},
		{name: "trailing json", raw: valid + `{}`},
		{name: "empty blocks", raw: `{"blocks":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionWriterFixture(t, test.raw)

			_, err := fixture.writer.Write(context.Background(), fixture.run)

			require.Error(t, err)
			require.Equal(t, 0, fixture.service.calls)
			require.Equal(t, []string{"persist"}, *fixture.events)
			var persisted string
			require.NoError(t, json.Unmarshal(fixture.runs.persisted, &persisted))
			require.Equal(t, test.raw, persisted)
			require.Equal(t, productionWriterRawDigest(t, test.raw), fixture.runs.digest)
			require.Nil(t, fixture.run.OutputVersionID)
		})
	}
}

func TestProductionWriterValidatesCompleteCandidateBeforeAppend(t *testing.T) {
	fixture := newProductionWriterFixture(t, `{"blocks":[{"logical_block_id":"block-a","block_type":"fact","content":{"text":"claim"},"evidence_refs":[],"needs_confirmation":false}]}`)

	_, err := fixture.writer.Write(context.Background(), fixture.run)

	require.ErrorIs(t, err, types.ErrProductionDocumentValidation)
	require.Equal(t, 0, fixture.service.calls)
	require.Equal(t, []string{"persist"}, *fixture.events)
}

func TestProductionWriterReturnsModelPersistenceAndAppendErrors(t *testing.T) {
	t.Run("model lookup", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("model unavailable")
		fixture.writer.modelService = &productionWriterModelServiceStub{err: expected}
		_, err := fixture.writer.Write(context.Background(), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Empty(t, *fixture.events)
	})

	t.Run("model call", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("model failed")
		fixture.chat.err = expected
		_, err := fixture.writer.Write(context.Background(), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Empty(t, *fixture.events)
	})

	t.Run("nil model", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		fixture.writer.modelService = &productionWriterModelServiceStub{}

		_, err := fixture.writer.Write(context.Background(), fixture.run)

		require.ErrorIs(t, err, errProductionWriterConfiguration)
		require.Empty(t, *fixture.events)
	})

	t.Run("raw persistence", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("database unavailable")
		fixture.runs.err = expected
		_, err := fixture.writer.Write(context.Background(), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Equal(t, 0, fixture.service.calls)
	})

	t.Run("append", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("stale parent")
		fixture.service.err = expected
		_, err := fixture.writer.Write(context.Background(), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Equal(t, []string{"persist", "append"}, *fixture.events)
	})
}

func TestProductionWriterPreservesModelOrderAndProducesDeterministicCandidateDigest(t *testing.T) {
	raw := productionWriterOutput(t,
		writerFact("block-b", "second", []string{writerEvidenceID}, false),
		writerFact("block-a", "first", []string{}, false),
	)
	first := newProductionWriterFixture(t, raw)
	second := newProductionWriterFixture(t, raw)
	second.run.ID = first.run.ID
	second.runs.run = second.run

	firstVersion, err := first.writer.Write(context.Background(), first.run)
	require.NoError(t, err)
	secondVersion, err := second.writer.Write(context.Background(), second.run)
	require.NoError(t, err)

	firstInputs := first.service.input.Blocks
	require.Equal(t, "block-b", firstInputs[len(firstInputs)-2].LogicalBlockID)
	require.Equal(t, "block-a", firstInputs[len(firstInputs)-1].LogicalBlockID)
	require.Equal(t, firstVersion.ContentDigest, secondVersion.ContentDigest)
}
