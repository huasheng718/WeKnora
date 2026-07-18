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

func TestRetiredDocumentTypeIsImmutable(t *testing.T) {
	documentType := &ProductionDocumentType{Status: ProductionDocumentTypeRetired}
	require.ErrorIs(t, documentType.CanMutateDefinition(), ErrProductionDocumentTypeImmutable)
}

func TestProductionProjectStatusValidation(t *testing.T) {
	tests := []struct {
		status ProductionProjectStatus
		valid  bool
	}{
		{status: ProductionProjectActive, valid: true},
		{status: ProductionProjectArchived, valid: true},
		{status: ProductionProjectStatus(""), valid: false},
		{status: ProductionProjectStatus("draft"), valid: false},
		{status: ProductionProjectStatus("retired"), valid: false},
	}

	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			require.Equal(t, test.valid, test.status.IsValid())
		})
	}
}

func TestProductionDocumentTypeStatusValidation(t *testing.T) {
	tests := []struct {
		status ProductionDocumentTypeStatus
		valid  bool
	}{
		{status: ProductionDocumentTypeDraft, valid: true},
		{status: ProductionDocumentTypeActive, valid: true},
		{status: ProductionDocumentTypeRetired, valid: true},
		{status: ProductionDocumentTypeStatus(""), valid: false},
		{status: ProductionDocumentTypeStatus("archived"), valid: false},
	}

	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			require.Equal(t, test.valid, test.status.IsValid())
		})
	}
}

func TestProductionConflictSentinel(t *testing.T) {
	require.Error(t, ErrProductionConflict)
}
