package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionDocumentBlockDigestCanonicalizesJSON(t *testing.T) {
	left := &ProductionDocumentBlock{
		LogicalBlockID: "block-a", BlockType: "paragraph", Position: 0,
		Content: JSON(`{"b":2,"a":1}`), Attributes: JSON(`{"z":false,"a":true}`),
		EvidenceRefs: JSON(`["evidence-b","evidence-a"]`), AIProvenance: JSON(`{"model":"baseline"}`),
	}
	right := &ProductionDocumentBlock{
		LogicalBlockID: "block-a", BlockType: "paragraph", Position: 0,
		Content: JSON(`{ "a": 1, "b": 2 }`), Attributes: JSON(`{ "a": true, "z": false }`),
		EvidenceRefs: JSON(`[ "evidence-b", "evidence-a" ]`), AIProvenance: JSON(`{ "model": "baseline" }`),
	}

	require.Equal(t, ComputeProductionBlockDigest(left), ComputeProductionBlockDigest(right))
	require.Len(t, ComputeProductionBlockDigest(left), 64)
}

func TestProductionDocumentVersionDigestIsStableAcrossLoadedBlockOrder(t *testing.T) {
	blockA := &ProductionDocumentBlock{
		LogicalBlockID: "block-a", BlockType: "paragraph", Position: 0,
		Content: JSON(`"alpha"`), Attributes: JSON(`{}`), EvidenceRefs: JSON(`[]`), AIProvenance: JSON(`{}`),
	}
	blockB := &ProductionDocumentBlock{
		LogicalBlockID: "block-b", BlockType: "paragraph", Position: 1,
		Content: JSON(`"beta"`), Attributes: JSON(`{}`), EvidenceRefs: JSON(`[]`), AIProvenance: JSON(`{}`),
	}
	blockA.ContentDigest = ComputeProductionBlockDigest(blockA)
	blockB.ContentDigest = ComputeProductionBlockDigest(blockB)

	forward := &ProductionDocumentVersion{Blocks: []*ProductionDocumentBlock{blockA, blockB}}
	reverse := &ProductionDocumentVersion{Blocks: []*ProductionDocumentBlock{blockB, blockA}}

	require.Equal(t, ComputeProductionVersionDigest(forward), ComputeProductionVersionDigest(reverse))
	require.Len(t, ComputeProductionVersionDigest(forward), 64)
}

func TestProductionDocumentBlockLineageRelationsAreClosed(t *testing.T) {
	for _, relation := range []ProductionBlockRelation{
		ProductionBlockRelationSame,
		ProductionBlockRelationSplit,
		ProductionBlockRelationMerged,
	} {
		require.True(t, relation.IsValid())
	}
	require.False(t, ProductionBlockRelation("copied").IsValid())
}
