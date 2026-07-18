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

func TestProductionDocumentBlockDigestCanonicalizesEquivalentNumbersExactly(t *testing.T) {
	equivalent := []JSON{
		JSON(`{"value":1,"nested":[1.0,1e0,-0]}`),
		JSON(`{"nested":[1e0,1.00,0.0],"value":1.000e+0}`),
	}
	digests := make([]string, 0, len(equivalent))
	for _, content := range equivalent {
		block := &ProductionDocumentBlock{
			LogicalBlockID: "block-a", BlockType: "paragraph", Content: content,
			Attributes: JSON(`{}`), EvidenceRefs: JSON(`[]`), AIProvenance: JSON(`{}`),
		}
		digests = append(digests, ComputeProductionBlockDigest(block))
	}
	require.Equal(t, digests[0], digests[1])
}

func TestProductionDocumentBlockDigestPreservesLargeNumberPrecision(t *testing.T) {
	equivalent := []JSON{
		JSON(`{"integer":123456789012345678901234567890,"decimal":0.0000000000000000001234500}`),
		JSON(`{"decimal":1.2345e-19,"integer":12345678901234567890123456789e1}`),
	}
	blocks := make([]*ProductionDocumentBlock, 0, len(equivalent))
	for _, content := range equivalent {
		blocks = append(blocks, &ProductionDocumentBlock{
			LogicalBlockID: "block-a", BlockType: "paragraph", Content: content,
			Attributes: JSON(`{}`), EvidenceRefs: JSON(`[]`), AIProvenance: JSON(`{}`),
		})
	}
	require.Equal(t, ComputeProductionBlockDigest(blocks[0]), ComputeProductionBlockDigest(blocks[1]))

	different := *blocks[1]
	different.Content = JSON(`{"decimal":1.2346e-19,"integer":12345678901234567890123456789e1}`)
	require.NotEqual(t, ComputeProductionBlockDigest(blocks[0]), ComputeProductionBlockDigest(&different))
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
