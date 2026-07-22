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

	testWriterMaxEvidenceCount        = 128
	testWriterMaxInlineEvidenceBytes  = 64 * 1024
	testWriterMaxEvidenceBytes        = 512 * 1024
	testWriterMaxCurrentVersionBlocks = 512
	testWriterMaxCurrentVersionBytes  = 512 * 1024
	testWriterMaxContextBytes         = 1024 * 1024
	testWriterMaxRawResponseBytes     = 1024 * 1024
	testWriterMaxOutputBlocks         = 256
	testWriterMaxOutputBlockBytes     = 64 * 1024
	testWriterMaxOutputBytes          = 512 * 1024
	testWriterMaxCompletionTokens     = 8192
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
	if s.run.RawModelResponseDigest != nil {
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
		ID: input.VersionID, DocumentID: documentID, SourceSetID: input.SourceSetID,
		Origin: input.Origin, Blocks: blocks,
	}
	if version.ID == "" {
		version.ID = uuid.NewString()
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
		DocumentTypeSnapshot: productionWriterTestDocumentTypeSnapshot(t, types.JSON(`{"version":1,"skills":[{"name":"baseline","digest":"`+strings.Repeat("a", 64)+`"}]}`)),
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

func productionWriterTestDocumentTypeSnapshot(t *testing.T, skillBindings types.JSON) types.JSON {
	t.Helper()
	config, ok := legacyProductionDocumentTypeConfig("software-development-baseline")
	require.True(t, ok)
	input, err := productionDocumentTypeConfigInput(config)
	require.NoError(t, err)
	input.SkillBindings = skillBindings
	snapshot, _, err := canonicalProductionDocumentTypeSnapshot(&types.ProductionDocumentType{
		ID: writerTypeID, Code: "software-development-baseline", Name: "Baseline", SchemaVersion: 3,
		BlockSchema: input.BlockSchema, SourceRequirements: input.SourceRequirements,
		SkillBindings: input.SkillBindings, WorkflowPlan: input.WorkflowPlan,
		QualityRules: input.QualityRules, ReviewPolicy: input.ReviewPolicy, PublicationPolicy: input.PublicationPolicy,
	})
	require.NoError(t, err)
	return snapshot
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

func productionWriterAuditedRawSize(t *testing.T, raw string) int {
	t.Helper()
	snapshot, err := json.Marshal(raw)
	require.NoError(t, err)
	canonical, err := types.CanonicalProductionJSON(snapshot)
	require.NoError(t, err)
	return len(canonical)
}

func productionWriterOutputWithBlockCount(t *testing.T, count int) string {
	t.Helper()
	template := BuiltinSoftwareDevelopmentBaseline()
	require.GreaterOrEqual(t, count, len(template.RequiredSections))
	extras := make([]map[string]any, 0, count-len(template.RequiredSections))
	for index := len(template.RequiredSections); index < count; index++ {
		extras = append(extras, map[string]any{
			"logical_block_id": fmt.Sprintf("filler-%03d", index), "block_type": "heading",
			"content": fmt.Sprintf("filler %d", index), "evidence_refs": []string{}, "needs_confirmation": false,
		})
	}
	return productionWriterOutput(t, extras...)
}

func productionWriterTestContext(t *testing.T, run *types.ProductionRun) context.Context {
	t.Helper()
	ctx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: run.TenantID, ProjectID: run.ProjectID, RunID: run.ID,
	})
	require.NoError(t, err)
	return ctx
}

func writerEvidenceWithSize(t *testing.T, id string, size int) *types.ProductionEvidenceSnapshot {
	t.Helper()
	require.GreaterOrEqual(t, size, 2)
	inline := types.JSON(strconv.Quote(strings.Repeat("e", size-2)))
	require.Len(t, inline, size)
	sum := sha256.Sum256(inline)
	return &types.ProductionEvidenceSnapshot{
		ID: id, SourceItemID: uuid.NewString(), SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: inline, ContentDigest: hex.EncodeToString(sum[:]), RedactionMetadata: types.JSON(`{}`),
	}
}

func writerEvidenceSetForTotal(t *testing.T, total int) []*types.ProductionEvidenceSnapshot {
	t.Helper()
	chunks := make([]int, 0)
	for total > 0 {
		size := testWriterMaxInlineEvidenceBytes
		if total < size {
			size = total
		}
		if size == 1 {
			chunks[len(chunks)-1]--
			size = 2
		}
		chunks = append(chunks, size)
		total -= size
	}
	evidence := make([]*types.ProductionEvidenceSnapshot, 0, len(chunks))
	for index, size := range chunks {
		id := writerEvidenceID
		if index > 0 {
			id = uuid.NewString()
		}
		evidence = append(evidence, writerEvidenceWithSize(t, id, size))
	}
	return evidence
}

func requireWriterInputRejectedBeforeModel(t *testing.T, fixture *productionWriterFixture) {
	t.Helper()
	_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
	require.ErrorContains(t, err, "writer input exceeds size limit")
	require.Zero(t, fixture.chat.calls)
	require.Empty(t, fixture.chat.messages)
	require.Empty(t, fixture.runs.persisted)
	require.Empty(t, *fixture.events)
	require.Zero(t, fixture.service.calls)
}

func TestProductionWriterNormalizesGroundedFactAndUsesServerOwnedContext(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t,
		writerFact("block-a", "已上线", []string{writerEvidenceID}, false),
	))
	callerCtx := productionWriterTestContext(t, fixture.run)

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
	require.Equal(t, testWriterMaxCompletionTokens, fixture.chat.options.MaxTokens)
	require.Len(t, fixture.chat.messages, 2)
	require.NotContains(t, fixture.chat.messages[1].Content, "api_key")
	require.NotContains(t, fixture.chat.messages[1].Content, "writer-secret")
	require.Contains(t, fixture.chat.messages[1].Content, strings.Repeat("a", 64))
	require.Contains(t, fixture.chat.messages[1].Content, "基线范围与目标")
	require.Contains(t, fixture.chat.messages[1].Content, writerVersionID)
}

func TestProductionWriterEnforcesEvidenceInputBoundariesBeforeModel(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		exact := newProductionWriterFixture(t, productionWriterOutput(t))
		exact.sources.evidence = make([]*types.ProductionEvidenceSnapshot, 0, testWriterMaxEvidenceCount)
		for index := 0; index < testWriterMaxEvidenceCount; index++ {
			exact.sources.evidence = append(exact.sources.evidence, writerEvidenceWithSize(t, uuid.NewString(), 3))
		}
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)
		require.Equal(t, 1, exact.chat.calls)

		over := newProductionWriterFixture(t, productionWriterOutput(t))
		over.sources.evidence = append([]*types.ProductionEvidenceSnapshot(nil), exact.sources.evidence...)
		over.sources.evidence = append(over.sources.evidence, writerEvidenceWithSize(t, uuid.NewString(), 3))
		requireWriterInputRejectedBeforeModel(t, over)
	})

	t.Run("per inline bytes", func(t *testing.T) {
		exact := newProductionWriterFixture(t, productionWriterOutput(t))
		exact.sources.evidence = []*types.ProductionEvidenceSnapshot{
			writerEvidenceWithSize(t, writerEvidenceID, testWriterMaxInlineEvidenceBytes),
		}
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)

		over := newProductionWriterFixture(t, productionWriterOutput(t))
		over.sources.evidence = []*types.ProductionEvidenceSnapshot{
			writerEvidenceWithSize(t, writerEvidenceID, testWriterMaxInlineEvidenceBytes+1),
		}
		requireWriterInputRejectedBeforeModel(t, over)
	})

	t.Run("aggregate inline bytes", func(t *testing.T) {
		exact := newProductionWriterFixture(t, productionWriterOutput(t))
		exact.sources.evidence = writerEvidenceSetForTotal(t, testWriterMaxEvidenceBytes)
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)

		over := newProductionWriterFixture(t, productionWriterOutput(t))
		over.sources.evidence = writerEvidenceSetForTotal(t, testWriterMaxEvidenceBytes+1)
		requireWriterInputRejectedBeforeModel(t, over)
	})
}

func TestProductionWriterEnforcesCurrentVersionBoundariesBeforeModel(t *testing.T) {
	t.Run("block count", func(t *testing.T) {
		exact := newProductionWriterFixture(t, productionWriterOutput(t))
		for index := 0; index < testWriterMaxCurrentVersionBlocks; index++ {
			exact.documents.version.Blocks = append(exact.documents.version.Blocks, &types.ProductionDocumentBlock{
				ID: uuid.NewString(), VersionID: writerVersionID, LogicalBlockID: fmt.Sprintf("existing-%03d", index),
				BlockType: "paragraph", Position: index, Content: types.JSON(`"existing"`),
				Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
			})
		}
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)

		over := newProductionWriterFixture(t, productionWriterOutput(t))
		over.documents.version.Blocks = append([]*types.ProductionDocumentBlock(nil), exact.documents.version.Blocks...)
		over.documents.version.Blocks = append(over.documents.version.Blocks, &types.ProductionDocumentBlock{Content: types.JSON(`"over"`)})
		requireWriterInputRejectedBeforeModel(t, over)
	})

	t.Run("serialized bytes", func(t *testing.T) {
		fixtureAtSize := func(size int) *productionWriterFixture {
			fixture := newProductionWriterFixture(t, productionWriterOutput(t))
			block := &types.ProductionDocumentBlock{
				ID: uuid.NewString(), VersionID: writerVersionID, LogicalBlockID: "existing-large", BlockType: "paragraph",
				Content: types.JSON(`""`), Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
			}
			fixture.documents.version.Blocks = []*types.ProductionDocumentBlock{block}
			encoded, err := json.Marshal(fixture.documents.version)
			require.NoError(t, err)
			require.LessOrEqual(t, len(encoded), size)
			block.Content = types.JSON(strconv.Quote(strings.Repeat("v", size-len(encoded))))
			encoded, err = json.Marshal(fixture.documents.version)
			require.NoError(t, err)
			require.Len(t, encoded, size)
			return fixture
		}

		exact := fixtureAtSize(testWriterMaxCurrentVersionBytes)
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)
		over := fixtureAtSize(testWriterMaxCurrentVersionBytes + 1)
		requireWriterInputRejectedBeforeModel(t, over)
	})
}

func TestProductionWriterEnforcesEncodedContextBoundaryBeforeModel(t *testing.T) {
	fixtureAtSize := func(size int) *productionWriterFixture {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		fixture.documents.document.Title = ""
		messages, err := productionWriterMessages(
			fixture.run, mustDecodeProductionWriterDocumentType(t, fixture.run.DocumentTypeSnapshot),
			fixture.documents.document, fixture.sources.set, fixture.documents.version, fixture.sources.evidence,
		)
		require.NoError(t, err)
		require.LessOrEqual(t, len(messages[1].Content), size)
		fixture.documents.document.Title = strings.Repeat("t", size-len(messages[1].Content))
		messages, err = productionWriterMessages(
			fixture.run, mustDecodeProductionWriterDocumentType(t, fixture.run.DocumentTypeSnapshot),
			fixture.documents.document, fixture.sources.set, fixture.documents.version, fixture.sources.evidence,
		)
		require.NoError(t, err)
		require.Len(t, messages[1].Content, size)
		return fixture
	}

	exact := fixtureAtSize(testWriterMaxContextBytes)
	_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
	require.NoError(t, err)
	over := fixtureAtSize(testWriterMaxContextBytes)
	over.documents.document.Title += "t"
	requireWriterInputRejectedBeforeModel(t, over)
}

func TestProductionWriterEnforcesRawModelResponseBoundaryBeforeAudit(t *testing.T) {
	fixtureAtSize := func(size int) *productionWriterFixture {
		raw := productionWriterOutput(t)
		base := productionWriterAuditedRawSize(t, raw)
		require.LessOrEqual(t, base, size)
		raw += strings.Repeat(" ", size-base)
		require.Equal(t, size, productionWriterAuditedRawSize(t, raw))
		return newProductionWriterFixture(t, raw)
	}

	exact := fixtureAtSize(testWriterMaxRawResponseBytes)
	_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
	require.ErrorContains(t, err, "writer output exceeds size limit")
	require.Len(t, exact.runs.persisted, testWriterMaxRawResponseBytes)
	require.Equal(t, 1, exact.chat.calls)
	require.Zero(t, exact.service.calls)

	over := fixtureAtSize(testWriterMaxRawResponseBytes + 1)
	_, err = over.writer.Write(productionWriterTestContext(t, over.run), over.run)
	require.ErrorContains(t, err, "writer output exceeds size limit")
	require.LessOrEqual(t, len(err.Error()), 128)
	require.Empty(t, over.runs.persisted)
	require.Empty(t, *over.events)
	require.Equal(t, 1, over.chat.calls)
	require.Zero(t, over.service.calls)
}

func TestProductionWriterEnforcesStructuredOutputBoundaries(t *testing.T) {
	t.Run("block count", func(t *testing.T) {
		exact := newProductionWriterFixture(t, productionWriterOutputWithBlockCount(t, testWriterMaxOutputBlocks))
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)
		require.Len(t, exact.service.input.Blocks, testWriterMaxOutputBlocks)

		over := newProductionWriterFixture(t, productionWriterOutputWithBlockCount(t, testWriterMaxOutputBlocks+1))
		_, err = over.writer.Write(productionWriterTestContext(t, over.run), over.run)
		require.ErrorContains(t, err, "writer output exceeds size limit")
		require.NotEmpty(t, over.runs.persisted)
		require.Zero(t, over.service.calls)
	})

	t.Run("per block content bytes", func(t *testing.T) {
		fixtureAtSize := func(size int) *productionWriterFixture {
			return newProductionWriterFixture(t, productionWriterOutput(t, map[string]any{
				"logical_block_id": "large-block", "block_type": "paragraph",
				"content": strings.Repeat("b", size-2), "evidence_refs": []string{}, "needs_confirmation": true,
			}))
		}
		exact := fixtureAtSize(testWriterMaxOutputBlockBytes)
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)
		require.Len(t, exact.service.input.Blocks[len(exact.service.input.Blocks)-1].Content, testWriterMaxOutputBlockBytes)

		over := fixtureAtSize(testWriterMaxOutputBlockBytes + 1)
		_, err = over.writer.Write(productionWriterTestContext(t, over.run), over.run)
		require.ErrorContains(t, err, "writer output exceeds size limit")
		require.Zero(t, over.service.calls)
	})

	t.Run("total output bytes", func(t *testing.T) {
		fixtureAtSize := func(size int) *productionWriterFixture {
			raw := productionWriterOutput(t)
			require.LessOrEqual(t, len(raw), size)
			raw += strings.Repeat(" ", size-len(raw))
			require.Len(t, raw, size)
			return newProductionWriterFixture(t, raw)
		}
		exact := fixtureAtSize(testWriterMaxOutputBytes)
		_, err := exact.writer.Write(productionWriterTestContext(t, exact.run), exact.run)
		require.NoError(t, err)
		over := fixtureAtSize(testWriterMaxOutputBytes + 1)
		_, err = over.writer.Write(productionWriterTestContext(t, over.run), over.run)
		require.ErrorContains(t, err, "writer output exceeds size limit")
		require.NotEmpty(t, over.runs.persisted)
		require.Zero(t, over.service.calls)
	})
}

func mustDecodeProductionWriterDocumentType(t *testing.T, raw types.JSON) *productionWriterDocumentTypeSnapshot {
	t.Helper()
	snapshot, err := decodeProductionWriterDocumentType(raw)
	require.NoError(t, err)
	return snapshot
}

func TestProductionWriterRejectsInvalidGovernanceForLegacyCode(t *testing.T) {
	raw := types.JSON(`{
		"id":"22222222-2222-4222-8222-222222222222","code":"software-development-baseline","schema_version":9,
		"block_schema":{"version":1,"required_sections":["Snapshot section"],"allowed_block_types":["heading","paragraph"]},
		"source_requirements":{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["manual"],"require_evidence_section":true,"allow_unsupported_facts":false},
		"skill_bindings":{"version":1,"skills":[]},"workflow_plan":{"version":1,"steps":[]},
		"quality_rules":{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed"]},
		"review_policy":{"steps":["business_reviewer"]},
		"publication_policy":{"version":1,"target_type":"external_wiki","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}
	}`)

	_, err := decodeProductionWriterDocumentType(raw)

	require.Error(t, err)
	require.ErrorContains(t, err, "invalid document type governance snapshot")
}

func TestProductionDocumentTypeConfigRejectsInvalidGovernanceForLegacyCode(t *testing.T) {
	documentType := &types.ProductionDocumentType{
		Code:               "software-development-baseline",
		BlockSchema:        types.JSON(`{"version":1,"required_sections":["Snapshot section"],"allowed_block_types":["heading","paragraph"]}`),
		SourceRequirements: types.JSON(`{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["manual"],"require_evidence_section":true,"allow_unsupported_facts":false}`),
		SkillBindings:      types.JSON(`{"version":1,"skills":[]}`),
		WorkflowPlan:       types.JSON(`{"version":1,"steps":[]}`),
		QualityRules:       types.JSON(`{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed"]}`),
		ReviewPolicy:       types.JSON(`{"steps":["business_reviewer"]}`),
		PublicationPolicy:  types.JSON(`{"version":1,"target_type":"external_wiki","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
	}

	_, err := productionDocumentTypeConfig(documentType)

	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrProductionDocumentTypeConfigInvalid)
}

func TestProductionLegacyAdapterRequiresEveryGovernanceFieldEmpty(t *testing.T) {
	allEmpty := types.JSON(`{
		"id":"22222222-2222-4222-8222-222222222222","code":"software-development-baseline","schema_version":3,
		"block_schema":{},"source_requirements":{},"skill_bindings":{},"workflow_plan":{},
		"quality_rules":{},"review_policy":{},"publication_policy":{}
	}`)
	snapshot, err := decodeProductionWriterDocumentType(allEmpty)
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.BlockSchema.Version)
	require.Equal(t, 1, snapshot.PublicationPolicy.Version)

	partial := types.JSON(`{
		"id":"22222222-2222-4222-8222-222222222222","code":"software-development-baseline","schema_version":3,
		"block_schema":{},"source_requirements":{},"skill_bindings":{"version":1,"skills":[]},"workflow_plan":{},
		"quality_rules":{},"review_policy":{},"publication_policy":{}
	}`)
	_, err = decodeProductionWriterDocumentType(partial)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid document type governance snapshot")

	workflowWithStep := types.JSON(`{
		"id":"22222222-2222-4222-8222-222222222222","code":"software-development-baseline","schema_version":3,
		"block_schema":{},"source_requirements":{},"skill_bindings":{},
		"workflow_plan":{"version":1,"steps":[{"provider_type":"mcp","provider_id":"33333333-3333-4333-8333-333333333333","tool_name":"lookup","request":{}}]},
		"quality_rules":{},"review_policy":{},"publication_policy":{}
	}`)
	_, err = decodeProductionWriterDocumentType(workflowWithStep)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid document type governance snapshot")
}

func TestProductionWriterPromptUsesExactSnapshotGovernance(t *testing.T) {
	raw := types.JSON(`{
		"id":"22222222-2222-4222-8222-222222222222","code":"sop","schema_version":9,
		"block_schema":{"version":1,"required_sections":["Snapshotted Runbook Section"],"allowed_block_types":["heading","paragraph"]},
		"source_requirements":{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["manual"],"require_evidence_section":true,"allow_unsupported_facts":false},
		"skill_bindings":{"version":1,"skills":[]},"workflow_plan":{"version":1,"steps":[]},
		"quality_rules":{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","sop_exception_path"]},
		"review_policy":{"steps":["business_reviewer"]},
		"publication_policy":{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}
	}`)
	documentType, err := decodeProductionWriterDocumentType(raw)
	require.NoError(t, err)
	inputVersionID := "version-1"
	run := &types.ProductionRun{ID: "run-1", ProjectID: "project-1", DocumentID: "document-1", SourceSetID: "source-set-1", InputVersionID: &inputVersionID}
	messages, err := productionWriterMessages(
		run,
		documentType,
		&types.ProductionDocument{ID: "document-1", Title: "Snapshot governed SOP"},
		&types.ProductionSourceSet{ID: "source-set-1", Status: types.ProductionSourceSetFrozen},
		&types.ProductionDocumentVersion{ID: inputVersionID},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	var prompt struct {
		DocumentType struct {
			RequiredSections []string `json:"required_sections"`
			QualityGates     []string `json:"quality_gates"`
		} `json:"document_type"`
	}
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &prompt))
	require.Equal(t, []string{"Snapshotted Runbook Section"}, prompt.DocumentType.RequiredSections)
	require.Equal(t, []string{"section_completeness", "fact_evidence", "no_unconfirmed", "sop_exception_path"}, prompt.DocumentType.QualityGates)
}

func TestProductionWriterRequiresExactInternalRunPrincipal(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t,
		writerFact("block-a", "grounded", []string{writerEvidenceID}, false),
	))
	ctx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: fixture.run.TenantID, ProjectID: fixture.run.ProjectID, RunID: fixture.run.ID,
	})
	require.NoError(t, err)
	_, err = fixture.writer.Write(ctx, fixture.run)
	require.NoError(t, err)
	require.Equal(t, types.ProductionSystemActorID, fixture.service.ctx.Value(types.UserIDContextKey))
	_, roleSet := fixture.service.ctx.Value(types.TenantRoleContextKey).(types.TenantRole)
	require.False(t, roleSet)

	mismatch, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: fixture.run.TenantID, ProjectID: fixture.run.ProjectID, RunID: "72000000-0000-4000-8000-000000000099",
	})
	require.NoError(t, err)
	_, err = fixture.writer.Write(mismatch, fixture.run)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionWriterMarksUnsupportedFactsForConfirmation(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t,
		writerFact("block-a", "转化率提升 30%", []string{}, false),
	))

	_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.NoError(t, err)
	last := fixture.service.input.Blocks[len(fixture.service.input.Blocks)-1]
	attributes := decodeWriterAttributes(t, last.Attributes)
	require.Equal(t, true, attributes["factual"])
	require.Equal(t, true, attributes["needs_confirmation"])
}

func TestProductionWriterNormalizesFactToGovernedParagraph(t *testing.T) {
	for _, test := range []struct {
		name              string
		evidence          []string
		needsConfirmation bool
	}{
		{name: "accepted evidence", evidence: []string{writerEvidenceID}},
		{name: "no accepted evidence", evidence: []string{}, needsConfirmation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionWriterFixture(t, productionWriterOutput(t,
				writerFact("fact-a", "governed claim", test.evidence, false),
			))

			_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

			require.NoError(t, err)
			persisted := fixture.service.input.Blocks[len(fixture.service.input.Blocks)-1]
			require.Equal(t, "paragraph", persisted.BlockType)
			var content string
			require.NoError(t, json.Unmarshal(persisted.Content, &content))
			require.Equal(t, "governed claim", content)
			attributes := decodeWriterAttributes(t, persisted.Attributes)
			require.Equal(t, true, attributes["factual"])
			require.Equal(t, test.needsConfirmation, attributes["needs_confirmation"])
		})
	}
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

			_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

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

func TestProductionWriterRequiresInlineNormalizedEvidenceBeforeModelCall(t *testing.T) {
	for _, test := range []struct {
		name  string
		mixed bool
	}{
		{name: "resource only"},
		{name: "mixed inline and resource", mixed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionWriterFixture(t, productionWriterOutput(t))
			resourceID := "70000000-0000-4000-8000-000000000099"
			resourcePath := "resource://tenant-private/source.pdf"
			resource := &types.ProductionEvidenceSnapshot{
				ID: resourceID, SourceItemID: uuid.NewString(), SnapshotType: types.ProductionEvidenceSnapshotFile,
				StoragePath: resourcePath, ContentDigest: strings.Repeat("b", 64), RedactionMetadata: types.JSON(`{}`),
			}
			fixture.sources.evidence = []*types.ProductionEvidenceSnapshot{resource}
			if test.mixed {
				fixture.sources.evidence = append(fixture.sources.evidence, &types.ProductionEvidenceSnapshot{
					ID: writerEvidenceID, SourceItemID: uuid.NewString(), SnapshotType: types.ProductionEvidenceSnapshotText,
					InlineContent: types.JSON(`"inline"`), ContentDigest: strings.Repeat("c", 64), RedactionMetadata: types.JSON(`{}`),
				})
			}

			_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

			require.ErrorContains(t, err, "inline normalized evidence")
			require.Zero(t, fixture.chat.calls)
			require.Empty(t, fixture.chat.messages)
			require.Empty(t, fixture.runs.persisted)
			require.Empty(t, *fixture.events)
			require.Zero(t, fixture.service.calls)
			encoded, marshalErr := json.Marshal(fixture.chat.messages)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), resourceID)
			require.NotContains(t, string(encoded), resourcePath)
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
		{name: "unknown table content field", raw: productionWriterOutput(t, map[string]any{
			"logical_block_id": "table-a", "block_type": "table",
			"content":       map[string]any{"headers": []string{"A"}, "rows": [][]string{}, "ignored": true},
			"evidence_refs": []string{}, "needs_confirmation": true,
		})},
		{name: "unknown image content field", raw: productionWriterOutput(t, map[string]any{
			"logical_block_id": "image-a", "block_type": "image",
			"content":       map[string]any{"alt": "diagram", "url": "https://example.com/a.png", "ignored": true},
			"evidence_refs": []string{}, "needs_confirmation": false,
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionWriterFixture(t, test.raw)

			_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

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

func TestProductionWriterRejectsUnsafeImageURLBeforeAppend(t *testing.T) {
	fixture := newProductionWriterFixture(t, productionWriterOutput(t, map[string]any{
		"logical_block_id": "image-a", "block_type": "image",
		"content":       map[string]any{"alt": "diagram", "url": "https://user:secret@example.com/a.png"},
		"evidence_refs": []string{}, "needs_confirmation": false,
	}))

	_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.ErrorIs(t, err, types.ErrProductionDocumentValidation)
	require.Equal(t, 0, fixture.service.calls)
	require.Equal(t, []string{"persist"}, *fixture.events)
}

func TestProductionWriterReplaysAuditedRawInsteadOfReplacementModelResponse(t *testing.T) {
	firstRaw := productionWriterOutput(t, writerFact("block-a", "first", []string{writerEvidenceID}, false))
	fixture := newProductionWriterFixture(t, firstRaw)

	_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
	require.NoError(t, err)
	firstDigest := fixture.runs.digest
	fixture.chat.response.Content = productionWriterOutput(t, writerFact("block-a", "replacement", []string{writerEvidenceID}, false))

	_, err = fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.NoError(t, err)
	require.Equal(t, 2, fixture.service.calls)
	require.Equal(t, 1, fixture.chat.calls)
	require.Equal(t, firstDigest, fixture.runs.digest)
	var persisted string
	require.NoError(t, json.Unmarshal(fixture.runs.persisted, &persisted))
	require.Equal(t, firstRaw, persisted)
}

func TestProductionWriterReusesCanonicalAuditedRawWithoutSecondModelCall(t *testing.T) {
	raw := productionWriterOutput(t, writerFact("block-a", "replayed", []string{writerEvidenceID}, false))
	fixture := newProductionWriterFixture(t, raw)
	digest := productionWriterRawDigest(t, raw)
	canonical, err := types.CanonicalProductionJSON(types.JSON(strconv.Quote(raw)))
	require.NoError(t, err)
	fixture.run.RawModelResponse = canonical
	fixture.run.RawModelResponseDigest = &digest
	fixture.runs.run = fixture.run
	fixture.chat.err = errors.New("model must not be called")

	version, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.NoError(t, err)
	require.NotNil(t, version)
	require.Zero(t, fixture.chat.calls)
	require.Equal(t, []string{"append"}, *fixture.events)
	require.Equal(t, productionRunVersionID(fixture.run.ID), fixture.service.input.VersionID)
	require.Equal(t, 1, fixture.service.calls)
}

func TestProductionWriterRejectsAuditedRawDigestMismatchWithoutModelOrAppend(t *testing.T) {
	raw := productionWriterOutput(t)
	fixture := newProductionWriterFixture(t, raw)
	canonical, err := types.CanonicalProductionJSON(types.JSON(strconv.Quote(raw)))
	require.NoError(t, err)
	badDigest := strings.Repeat("f", 64)
	fixture.run.RawModelResponse = canonical
	fixture.run.RawModelResponseDigest = &badDigest
	fixture.runs.run = fixture.run

	_, err = fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.ErrorIs(t, err, errProductionWriterAuditConflict)
	require.Zero(t, fixture.chat.calls)
	require.Zero(t, fixture.service.calls)
}

func TestProductionWriterValidatesCompleteCandidateBeforeAppend(t *testing.T) {
	fixture := newProductionWriterFixture(t, `{"blocks":[{"logical_block_id":"block-a","block_type":"fact","content":{"text":"claim"},"evidence_refs":[],"needs_confirmation":false}]}`)

	_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

	require.ErrorIs(t, err, types.ErrProductionDocumentValidation)
	require.Equal(t, 0, fixture.service.calls)
	require.Equal(t, []string{"persist"}, *fixture.events)
}

func TestProductionWriterReturnsModelPersistenceAndAppendErrors(t *testing.T) {
	t.Run("model lookup", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("model unavailable")
		fixture.writer.modelService = &productionWriterModelServiceStub{err: expected}
		_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Empty(t, *fixture.events)
	})

	t.Run("model call", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("model failed")
		fixture.chat.err = expected
		_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Empty(t, *fixture.events)
	})

	t.Run("nil model", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		fixture.writer.modelService = &productionWriterModelServiceStub{}

		_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)

		require.ErrorIs(t, err, errProductionWriterConfiguration)
		require.Empty(t, *fixture.events)
	})

	t.Run("raw persistence", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("database unavailable")
		fixture.runs.err = expected
		_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
		require.ErrorIs(t, err, expected)
		require.Equal(t, 0, fixture.service.calls)
	})

	t.Run("append", func(t *testing.T) {
		fixture := newProductionWriterFixture(t, productionWriterOutput(t))
		expected := errors.New("stale parent")
		fixture.service.err = expected
		_, err := fixture.writer.Write(productionWriterTestContext(t, fixture.run), fixture.run)
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

	firstVersion, err := first.writer.Write(productionWriterTestContext(t, first.run), first.run)
	require.NoError(t, err)
	secondVersion, err := second.writer.Write(productionWriterTestContext(t, second.run), second.run)
	require.NoError(t, err)

	firstInputs := first.service.input.Blocks
	require.Equal(t, "block-b", firstInputs[len(firstInputs)-2].LogicalBlockID)
	require.Equal(t, "block-a", firstInputs[len(firstInputs)-1].LogicalBlockID)
	require.Equal(t, firstVersion.ContentDigest, secondVersion.ContentDigest)
}
