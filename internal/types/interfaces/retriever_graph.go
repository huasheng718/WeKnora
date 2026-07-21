package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// RetrieveGraphRepository is a repository for retrieving graphs
type RetrieveGraphRepository interface {
	// AddGraph adds a graph to the repository
	AddGraph(ctx context.Context, namespace types.NameSpace, graphs []*types.GraphData) error
	// DelGraph deletes a graph from the repository
	DelGraph(ctx context.Context, namespace []types.NameSpace) error
	// SearchNode searches for nodes in the repository
	SearchNode(ctx context.Context, namespace types.NameSpace, nodes []string) (*types.GraphData, error)
	// SearchNodeInNamespaces searches an explicit allowed namespace set in one
	// repository operation. Implementations must enforce Knowledge provenance.
	SearchNodeInNamespaces(ctx context.Context, namespaces []types.NameSpace, nodes []string) (*types.GraphData, error)
}

// KnowledgeGraphQueryService scopes graph reads to visible Knowledge documents
// before querying the graph repository.
type KnowledgeGraphQueryService interface {
	SearchKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID string, nodes []string) (*types.GraphData, error)
}

// ExplicitKnowledgeGraphQueryService applies production visibility to a
// graph read that targets one Knowledge document.
type ExplicitKnowledgeGraphQueryService interface {
	SearchExplicitKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID, knowledgeID string, nodes []string) (*types.GraphData, error)
}
