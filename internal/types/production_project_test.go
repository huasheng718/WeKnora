package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionRoleValidation(t *testing.T) {
	require.True(t, ProductionRoleAuthor.IsValid())
	require.True(t, ProductionRolePublisher.IsValid())
	require.False(t, ProductionRole("owner").IsValid())
}

func TestActivatedDocumentTypeIsImmutable(t *testing.T) {
	documentType := &ProductionDocumentType{Status: ProductionDocumentTypeActive}
	require.ErrorIs(t, documentType.CanMutateDefinition(), ErrProductionDocumentTypeImmutable)
}
