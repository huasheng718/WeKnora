package types

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionReleaseStatusesAreStableAndValidated(t *testing.T) {
	statuses := []ProductionReleaseTargetStatus{
		ReleaseTargetBuilding,
		ReleaseTargetReady,
		ReleaseTargetActive,
		ReleaseTargetFailed,
		ReleaseTargetRolledBack,
		ReleaseTargetCleanupPending,
		ReleaseTargetCleaned,
	}
	for _, status := range statuses {
		require.Truef(t, status.IsValid(), "status %q", status)
	}
	require.False(t, ProductionReleaseTargetStatus("unknown").IsValid())
}

func TestRejectProductionProjectionMutationFailsClosedAndVerifiesContent(t *testing.T) {
	content := "# governed"
	digest := sha256.Sum256([]byte(content))
	knowledge := &Knowledge{ID: "knowledge-1", Type: KnowledgeTypeManual}
	meta := NewManualKnowledgeMetadata(content, ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &ProductionProjectionMetadata{
		DocumentID: "document-1", VersionID: "version-1", ReleaseTargetID: "target-1",
		ContentDigest: fmt.Sprintf("%x", digest[:]), SummaryModelID: "summary",
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))

	require.ErrorIs(t, RejectProductionProjectionMutation(knowledge), ErrProductionProjectionImmutable)
	meta.Content = "tampered"
	require.NoError(t, knowledge.SetManualMetadata(meta))
	err := RejectProductionProjectionMutation(knowledge)
	require.ErrorIs(t, err, ErrProductionProjectionImmutable)
	require.ErrorIs(t, err, ErrProductionContentDigestMismatch)

	knowledge.Metadata = JSON(`{"production_projection":`)
	err = RejectProductionProjectionMutation(knowledge)
	require.ErrorIs(t, err, ErrProductionProjectionImmutable)
}

func TestProductionReleaseTargetTransitionsDoNotSkipReadyOrBypassHead(t *testing.T) {
	require.True(t, CanTransitionReleaseTarget(ReleaseTargetBuilding, ReleaseTargetReady))
	require.False(t, CanTransitionReleaseTarget(ReleaseTargetBuilding, ReleaseTargetActive))
	require.True(t, CanTransitionReleaseTarget(ReleaseTargetReady, ReleaseTargetActive), "the head trigger owns this valid lifecycle edge")
	require.False(t, CanTransitionReleaseTarget(ReleaseTargetActive, ReleaseTargetRolledBack), "the head switch owns active rollback")
	require.True(t, CanTransitionReleaseTarget(ReleaseTargetFailed, ReleaseTargetBuilding))
	require.True(t, CanTransitionReleaseTarget(ReleaseTargetRolledBack, ReleaseTargetCleanupPending))
	require.True(t, CanTransitionReleaseTarget(ReleaseTargetCleanupPending, ReleaseTargetCleaned))
	require.False(t, CanTransitionReleaseTarget(ReleaseTargetCleaned, ReleaseTargetBuilding))
}

func TestCanonicalProductionReleaseTargetConfigUsesCanonicalJSON(t *testing.T) {
	left, leftDigest, err := CanonicalProductionReleaseTargetConfig(JSON(`{ "version": 1.0, "render": { "heading": true, "labels": ["b", "a"] } }`))
	require.NoError(t, err)
	right, rightDigest, err := CanonicalProductionReleaseTargetConfig(JSON(`{"render":{"labels":["b","a"],"heading":true},"version":1e0}`))
	require.NoError(t, err)
	require.Equal(t, left, right)
	require.Equal(t, leftDigest, rightDigest)
	require.Len(t, leftDigest, 64)
}

func TestCanonicalProductionReleaseTargetConfigRejectsInvalidOrCredentialBearingJSON(t *testing.T) {
	nested := "0"
	for range ProductionReleaseTargetConfigMaxDepth + 1 {
		nested = `{"x":` + nested + `}`
	}
	for _, raw := range []JSON{
		JSON(`[]`),
		JSON(`{"token":"secret"}`),
		JSON(strings.Repeat(" ", ProductionReleaseTargetConfigMaxBytes+1)),
		JSON(nested),
	} {
		_, _, err := CanonicalProductionReleaseTargetConfig(raw)
		require.Error(t, err)
	}
}
