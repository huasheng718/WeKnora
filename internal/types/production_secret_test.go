package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionCredentialKeyPolicyUsesExactNormalizedMatches(t *testing.T) {
	for _, key := range []string{
		"token", "Access-Token", "refresh.token", "CLIENT SECRET", "apiKey", "authorization",
		"password", "secret", "private_key", "Credentials", "app-secret", "AUTH_HEADERS",
	} {
		require.True(t, IsProductionCredentialKey(key), key)
	}
	for _, key := range []string{"tokenized", "passwordless", "secretariat", "authorization_note", "credential_count"} {
		require.False(t, IsProductionCredentialKey(key), key)
	}
}

func TestRejectProductionCredentialFieldsRecursesNestedMapsAndArrays(t *testing.T) {
	err := RejectProductionCredentialFields(JSON(`{"safe":[{"nested":{"Auth.Headers":"Bearer x"}}]}`))
	require.ErrorIs(t, err, ErrProductionCredentialField)
	require.NoError(t, RejectProductionCredentialFields(JSON(`{"safe":[{"tokenized":"word"}]}`)))
}
