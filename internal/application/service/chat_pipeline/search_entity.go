package chatpipeline

import (
	"context"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PluginSearch implements search functionality for chat pipeline
type PluginSearchEntity struct {
	graphRepo     interfaces.RetrieveGraphRepository
	chunkRepo     interfaces.ChunkRepository
	knowledgeRepo interfaces.KnowledgeRepository
}

// NewPluginSearchEntity creates a new plugin search entity
func NewPluginSearchEntity(
	eventManager *EventManager,
	graphRepository interfaces.RetrieveGraphRepository,
	chunkRepository interfaces.ChunkRepository,
	knowledgeRepository interfaces.KnowledgeRepository,
) *PluginSearchEntity {
	res := &PluginSearchEntity{
		graphRepo:     graphRepository,
		chunkRepo:     chunkRepository,
		knowledgeRepo: knowledgeRepository,
	}
	eventManager.Register(res)
	return res
}

// ActivationEvents returns the list of event types this plugin responds to
func (p *PluginSearchEntity) ActivationEvents() []types.EventType {
	return []types.EventType{types.ENTITY_SEARCH}
}

// OnEvent processes triggered events
func (p *PluginSearchEntity) OnEvent(ctx context.Context,
	eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	entity := chatManage.Entity
	if len(entity) == 0 {
		logger.Infof(ctx, "No entity found")
		return next()
	}

	// Use EntityKBIDs (knowledge bases with ExtractConfig enabled)
	knowledgeBaseIDs := chatManage.EntityKBIDs
	// Use EntityKnowledge (KnowledgeID -> KnowledgeBaseID mapping for graph-enabled files)
	entityKnowledge := chatManage.EntityKnowledge

	if len(knowledgeBaseIDs) == 0 && len(entityKnowledge) == 0 {
		logger.Warnf(ctx, "No knowledge base IDs or knowledge IDs with ExtractConfig enabled for entity search")
		return next()
	}

	// Parallel search across multiple knowledge bases and individual files
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allNodes []*types.GraphNode
	var allRelations []*types.GraphRelation
	chunkTenants := make(map[string]uint64)
	chunkKnowledgeBases := make(map[string]string)
	ownerTenantByKB := entityOwnerTenants(chatManage.SearchTargets, chatManage.TenantID)

	// If specific KnowledgeIDs are provided, search by individual files
	if len(entityKnowledge) > 0 {
		logger.Infof(ctx, "Searching entities across %d knowledge file(s)", len(entityKnowledge))
		for knowledgeID, kbID := range entityKnowledge {
			wg.Add(1)
			go func(knowledgeBaseID, knowledgeID string) {
				defer wg.Done()

				graph, err := p.graphRepo.SearchNode(ctx, types.NameSpace{
					KnowledgeBase: knowledgeBaseID,
					Knowledge:     knowledgeID,
				}, entity)
				if err != nil {
					logger.Errorf(ctx, "Failed to search entity in Knowledge %s: %v", knowledgeID, err)
					return
				}

				logger.Infof(
					ctx,
					"Knowledge %s entity search result count: %d nodes, %d relations",
					knowledgeID,
					len(graph.Node),
					len(graph.Relation),
				)

				mu.Lock()
				allNodes = append(allNodes, graph.Node...)
				allRelations = append(allRelations, graph.Relation...)
				for _, node := range graph.Node {
					for _, chunkID := range node.Chunks {
						chunkTenants[chunkID] = ownerTenantByKB[knowledgeBaseID]
						chunkKnowledgeBases[chunkID] = knowledgeBaseID
					}
				}
				mu.Unlock()
			}(kbID, knowledgeID)
		}
	} else {
		// Otherwise, search by knowledge base
		logger.Infof(ctx, "Searching entities across %d knowledge base(s): %v", len(knowledgeBaseIDs), knowledgeBaseIDs)
		for _, kbID := range knowledgeBaseIDs {
			wg.Add(1)
			go func(knowledgeBaseID string) {
				defer wg.Done()

				graph, err := p.graphRepo.SearchNode(ctx, types.NameSpace{KnowledgeBase: knowledgeBaseID}, entity)
				if err != nil {
					logger.Errorf(ctx, "Failed to search entity in KB %s: %v", knowledgeBaseID, err)
					return
				}

				logger.Infof(
					ctx,
					"KB %s entity search result count: %d nodes, %d relations",
					knowledgeBaseID,
					len(graph.Node),
					len(graph.Relation),
				)

				mu.Lock()
				allNodes = append(allNodes, graph.Node...)
				allRelations = append(allRelations, graph.Relation...)
				for _, node := range graph.Node {
					for _, chunkID := range node.Chunks {
						chunkTenants[chunkID] = ownerTenantByKB[knowledgeBaseID]
						chunkKnowledgeBases[chunkID] = knowledgeBaseID
					}
				}
				mu.Unlock()
			}(kbID)
		}
	}

	wg.Wait()

	graph := &types.GraphData{
		Node:     allNodes,
		Relation: allRelations,
	}
	excludedKnowledgeIDs := excludedEntityKnowledgeIDs(chatManage.SearchTargets)
	if len(excludedKnowledgeIDs) > 0 {
		var err error
		graph, err = p.pruneExcludedProjectionGraph(ctx, chunkTenants, chunkKnowledgeBases, graph, excludedKnowledgeIDs)
		if err != nil {
			logger.Errorf(ctx, "Failed to prune excluded production projections from entity graph: %v", err)
			clearEntitySearchResults(chatManage)
			return next()
		}
	}
	chatManage.GraphResult = graph
	logger.Infof(ctx, "Total entity search result: %d nodes, %d relations", len(allNodes), len(allRelations))

	chunkIDs := filterSeenChunk(ctx, chatManage.GraphResult, chatManage.SearchResult)
	if len(chunkIDs) == 0 {
		logger.Infof(ctx, "No new chunk found")
		return next()
	}
	chunks, err := p.listEntityChunks(ctx, chunkTenants, chunkKnowledgeBases, chunkIDs)
	if err != nil {
		logger.Errorf(ctx, "Failed to list chunks, session_id: %s, error: %v", chatManage.SessionID, err)
		clearEntitySearchResults(chatManage)
		return next()
	}
	knowledgeIDs := []string{}
	for _, chunk := range chunks {
		knowledgeIDs = append(knowledgeIDs, chunk.KnowledgeID)
	}
	knowledges, err := p.listEntityKnowledges(ctx, chunks, knowledgeIDs)
	if err != nil {
		logger.Errorf(ctx, "Failed to list knowledge, session_id: %s, error: %v", chatManage.SessionID, err)
		clearEntitySearchResults(chatManage)
		return next()
	}

	knowledgeMap := map[string]*types.Knowledge{}
	for _, knowledge := range knowledges {
		knowledgeMap[knowledge.ID] = knowledge
	}
	var entityResults []*types.SearchResult
	for _, chunk := range chunks {
		if _, excluded := excludedKnowledgeIDs[chunk.KnowledgeID]; excluded {
			continue
		}
		if knowledgeMap[chunk.KnowledgeID] == nil {
			continue
		}
		searchResult := chunk2SearchResult(chunk, knowledgeMap[chunk.KnowledgeID])
		entityResults = append(entityResults, searchResult)
	}
	searchutil.EnrichSearchResultsImageInfo(ctx, p.chunkRepo, types.MustTenantIDFromContext(ctx), entityResults)
	chatManage.SearchResult = append(chatManage.SearchResult, entityResults...)
	// remove duplicate results
	chatManage.SearchResult = removeDuplicateResults(chatManage.SearchResult)
	if len(chatManage.SearchResult) == 0 {
		logger.Infof(ctx, "No new search result, session_id: %s", chatManage.SessionID)
		return ErrSearchNothing
	}
	logger.Infof(
		ctx,
		"search entity result count: %d, session_id: %s",
		len(chatManage.SearchResult),
		chatManage.SessionID,
	)
	return next()
}

func excludedEntityKnowledgeIDs(targets types.SearchTargets) map[string]struct{} {
	excluded := make(map[string]struct{})
	for _, target := range targets {
		if target == nil {
			continue
		}
		for _, id := range target.ExcludeKnowledgeIDs {
			if id != "" {
				excluded[id] = struct{}{}
			}
		}
	}
	return excluded
}

func entityOwnerTenants(targets types.SearchTargets, fallback uint64) map[string]uint64 {
	owners := make(map[string]uint64)
	for _, target := range targets {
		if target != nil && target.KnowledgeBaseID != "" {
			tenant := target.TenantID
			if tenant == 0 {
				tenant = fallback
			}
			owners[target.KnowledgeBaseID] = tenant
		}
	}
	return owners
}

func clearEntitySearchResults(chatManage *types.ChatManage) {
	chatManage.GraphResult = &types.GraphData{}
	chatManage.SearchResult = nil
}

func (p *PluginSearchEntity) listEntityChunks(ctx context.Context, chunkTenants map[string]uint64, chunkKnowledgeBases map[string]string, ids []string) ([]*types.Chunk, error) {
	byTenant := make(map[uint64][]string)
	expectedKBByChunk := make(map[string]string, len(ids))
	for _, id := range ids {
		tenant, ok := chunkTenants[id]
		if !ok || tenant == 0 {
			return nil, fmt.Errorf("entity chunk %s has no authorized owner", id)
		}
		expectedKB := chunkKnowledgeBases[id]
		if expectedKB == "" {
			return nil, fmt.Errorf("entity chunk %s has no authorized knowledge base", id)
		}
		if _, duplicate := expectedKBByChunk[id]; duplicate {
			continue
		}
		expectedKBByChunk[id] = expectedKB
		byTenant[tenant] = append(byTenant[tenant], id)
	}
	var out []*types.Chunk
	for tenant, chunkIDs := range byTenant {
		rows, err := p.chunkRepo.ListChunksByID(ctx, tenant, chunkIDs)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if row == nil {
				return nil, fmt.Errorf("entity chunk dependency returned nil row")
			}
			expectedKB, requested := expectedKBByChunk[row.ID]
			_, duplicate := seen[row.ID]
			if !requested || duplicate || row.TenantID != tenant || row.KnowledgeBaseID != expectedKB {
				return nil, fmt.Errorf("entity chunk scope mismatch")
			}
			seen[row.ID] = struct{}{}
			out = append(out, row)
		}
		if len(seen) != len(chunkIDs) {
			return nil, fmt.Errorf("entity chunk dependency returned incomplete rows")
		}
	}
	return out, nil
}

func (p *PluginSearchEntity) listEntityKnowledges(ctx context.Context, chunks []*types.Chunk, _ []string) ([]*types.Knowledge, error) {
	byTenant := make(map[uint64]map[string]string)
	for _, chunk := range chunks {
		if chunk == nil || chunk.TenantID == 0 || chunk.KnowledgeID == "" || chunk.KnowledgeBaseID == "" {
			return nil, fmt.Errorf("entity knowledge has incomplete chunk provenance")
		}
		if byTenant[chunk.TenantID] == nil {
			byTenant[chunk.TenantID] = make(map[string]string)
		}
		if expectedKB, exists := byTenant[chunk.TenantID][chunk.KnowledgeID]; exists && expectedKB != chunk.KnowledgeBaseID {
			return nil, fmt.Errorf("entity knowledge has conflicting knowledge base provenance")
		}
		byTenant[chunk.TenantID][chunk.KnowledgeID] = chunk.KnowledgeBaseID
	}
	var out []*types.Knowledge
	for tenant, expected := range byTenant {
		ids := make([]string, 0, len(expected))
		for id := range expected {
			ids = append(ids, id)
		}
		rows, err := p.knowledgeRepo.GetKnowledgeBatch(ctx, tenant, ids)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if row == nil {
				return nil, fmt.Errorf("entity knowledge dependency returned nil row")
			}
			expectedKB, requested := expected[row.ID]
			_, duplicate := seen[row.ID]
			if !requested || duplicate || row.TenantID != tenant || row.KnowledgeBaseID != expectedKB {
				return nil, fmt.Errorf("entity knowledge scope mismatch")
			}
			seen[row.ID] = struct{}{}
			out = append(out, row)
		}
		if len(seen) != len(expected) {
			return nil, fmt.Errorf("entity knowledge dependency returned incomplete rows")
		}
	}
	return out, nil
}

func (p *PluginSearchEntity) pruneExcludedProjectionGraph(
	ctx context.Context,
	chunkTenants map[string]uint64,
	chunkKnowledgeBases map[string]string,
	graph *types.GraphData,
	excluded map[string]struct{},
) (*types.GraphData, error) {
	if graph == nil || len(graph.Node) == 0 || len(excluded) == 0 {
		return graph, nil
	}
	chunkIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, node := range graph.Node {
		if node == nil {
			continue
		}
		for _, chunkID := range node.Chunks {
			if _, ok := seen[chunkID]; !ok && chunkID != "" {
				seen[chunkID] = struct{}{}
				chunkIDs = append(chunkIDs, chunkID)
			}
		}
	}
	chunks, err := p.listEntityChunks(ctx, chunkTenants, chunkKnowledgeBases, chunkIDs)
	if err != nil {
		return nil, err
	}
	allowedChunks := make(map[string]struct{}, len(chunks))
	for _, chunk := range chunks {
		if chunk == nil {
			continue
		}
		if _, excluded := excluded[chunk.KnowledgeID]; !excluded {
			allowedChunks[chunk.ID] = struct{}{}
		}
	}
	pruned := &types.GraphData{Text: graph.Text}
	allowedNodes := make(map[string]struct{})
	for _, node := range graph.Node {
		if node == nil {
			continue
		}
		chunks := make([]string, 0, len(node.Chunks))
		for _, chunkID := range node.Chunks {
			if _, ok := allowedChunks[chunkID]; ok {
				chunks = append(chunks, chunkID)
			}
		}
		if len(chunks) == 0 {
			continue
		}
		copy := *node
		copy.Chunks = chunks
		pruned.Node = append(pruned.Node, &copy)
		allowedNodes[node.Name] = struct{}{}
	}
	for _, relation := range graph.Relation {
		if relation == nil {
			continue
		}
		blocked := len(relation.KnowledgeIDs) == 0
		for _, knowledgeID := range relation.KnowledgeIDs {
			if _, excluded := excluded[knowledgeID]; excluded {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		if _, left := allowedNodes[relation.Node1]; !left {
			continue
		}
		if _, right := allowedNodes[relation.Node2]; right {
			pruned.Relation = append(pruned.Relation, relation)
		}
	}
	return pruned, nil
}

// filterSeenChunk filters seen chunks from the graph
func filterSeenChunk(ctx context.Context, graph *types.GraphData, searchResult []*types.SearchResult) []string {
	seen := map[string]bool{}
	for _, chunk := range searchResult {
		seen[chunk.ID] = true
	}
	logger.Infof(ctx, "filterSeenChunk: seen count: %d", len(seen))

	chunkIDs := []string{}
	for _, node := range graph.Node {
		for _, chunkID := range node.Chunks {
			if seen[chunkID] {
				continue
			}
			seen[chunkID] = true
			chunkIDs = append(chunkIDs, chunkID)
		}
	}
	logger.Infof(ctx, "filterSeenChunk: new chunkIDs count: %d", len(chunkIDs))
	return chunkIDs
}

// chunk2SearchResult converts a chunk to a search result
func chunk2SearchResult(chunk *types.Chunk, knowledge *types.Knowledge) *types.SearchResult {
	return &types.SearchResult{
		ID:                chunk.ID,
		Content:           chunk.Content,
		KnowledgeID:       chunk.KnowledgeID,
		ChunkIndex:        chunk.ChunkIndex,
		KnowledgeTitle:    knowledge.Title,
		StartAt:           chunk.StartAt,
		EndAt:             chunk.EndAt,
		Seq:               chunk.ChunkIndex,
		Score:             1.0,
		MatchType:         types.MatchTypeGraph,
		Metadata:          knowledge.GetMetadata(),
		ChunkType:         string(chunk.ChunkType),
		ParentChunkID:     chunk.ParentChunkID,
		ImageInfo:         chunk.ImageInfo,
		KnowledgeFilename: knowledge.FileName,
		KnowledgeSource:   knowledge.Source,
		KnowledgeChannel:  knowledge.Channel,
		ChunkMetadata:     chunk.Metadata,
		KnowledgeBaseID:   knowledge.KnowledgeBaseID,
	}
}
