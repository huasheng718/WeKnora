package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionReviewDecisionTransitions(t *testing.T) {
	require.True(t, CanTransitionReviewStep(ProductionReviewPending, ProductionReviewApproved))
	require.True(t, CanTransitionReviewStep(ProductionReviewPending, ProductionReviewChangesRequested))
	require.True(t, CanTransitionReviewStep(ProductionReviewPending, ProductionReviewRejected))
	require.False(t, CanTransitionReviewStep(ProductionReviewPending, ProductionReviewCancelled))
	require.False(t, CanTransitionReviewStep(ProductionReviewApproved, ProductionReviewPending))
	require.False(t, CanTransitionReviewStep(ProductionReviewApproved, ProductionReviewRejected))
}

func productionReviewNestedObject(depth int) JSON {
	value := "0"
	for range depth {
		value = `{"x":` + value + `}`
	}
	return JSON(value)
}

func TestProductionReviewPolicyResourceBounds(t *testing.T) {
	exactBytes := JSON(`{"x":"` + strings.Repeat("a", ProductionReviewPolicyMaxBytes-8) + `"}`)
	_, _, err := CanonicalProductionReviewPolicy(exactBytes)
	require.NoError(t, err)
	_, _, err = CanonicalProductionReviewPolicy(append(exactBytes[:len(exactBytes)-2], []byte(`a"}`)...))
	require.ErrorIs(t, err, ErrProductionJSONResourceLimit)

	_, _, err = CanonicalProductionReviewPolicy(productionReviewNestedObject(ProductionReviewPolicyMaxDepth))
	require.NoError(t, err)
	_, _, err = CanonicalProductionReviewPolicy(productionReviewNestedObject(ProductionReviewPolicyMaxDepth + 1))
	require.ErrorIs(t, err, ErrProductionJSONResourceLimit)
}

func TestProductionReviewEnumsMirrorSchema(t *testing.T) {
	for _, annotationType := range []ProductionAnnotationType{
		ProductionAnnotationComment,
		ProductionAnnotationSuggestion,
		ProductionAnnotationQualityTag,
	} {
		require.True(t, annotationType.IsValid())
	}
	for _, qualityTag := range []ProductionAnnotationCategory{
		ProductionQualityTagMissingEvidence,
		ProductionQualityTagFactualRisk,
		ProductionQualityTagUnclear,
		ProductionQualityTagIncomplete,
		ProductionQualityTagConflict,
		ProductionQualityTagComplianceRisk,
	} {
		require.True(t, qualityTag.IsValid())
	}
	for _, status := range []ProductionReviewStatus{
		ProductionReviewPending,
		ProductionReviewApproved,
		ProductionReviewRejected,
		ProductionReviewObsolete,
		ProductionReviewCancelled,
		ProductionReviewChangesRequested,
	} {
		require.True(t, status.IsValid())
	}
	for _, decision := range []ProductionReviewDecision{
		ProductionReviewPending,
		ProductionReviewApproved,
		ProductionReviewChangesRequested,
		ProductionReviewRejected,
		ProductionReviewCancelled,
	} {
		require.True(t, decision.IsValid())
	}
	require.False(t, ProductionReviewDecision(ProductionReviewObsolete).IsValid())
	// Cancelled is a system terminalization state, never a professional decision.
	require.False(t, ProductionReviewDecision(ProductionReviewCancelled).IsProfessionalDecision())
}

func TestProductionReviewPolicyCanonicalizationAndDigest(t *testing.T) {
	canonicalA, digestA, err := CanonicalProductionReviewPolicy(JSON(`{ "roles": ["business_reviewer"], "n": 1.0 }`))
	require.NoError(t, err)
	canonicalB, digestB, err := CanonicalProductionReviewPolicy(JSON(`{"n":1e0,"roles":["business_reviewer"]}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"n":1,"roles":["business_reviewer"]}`, string(canonicalA))
	require.Equal(t, canonicalA, canonicalB)
	require.Equal(t, digestA, digestB)
	require.Regexp(t, `^[0-9a-f]{64}$`, digestA)

	_, _, err = CanonicalProductionReviewPolicy(JSON(`[]`))
	require.Error(t, err)
	_, _, err = CanonicalProductionReviewPolicy(JSON(`{"a":1} trailing`))
	require.Error(t, err)
}
