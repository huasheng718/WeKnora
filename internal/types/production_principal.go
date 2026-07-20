package types

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	ProductionSystemActorID       = "00000000-0000-4000-8000-000000000001"
	ProductionInternalActorWorker = "production_worker"
)

type ProductionInternalPrincipal struct {
	ActorID   string
	ActorKind string
	TenantID  uint64
	ProjectID string
	RunID     string
}

type productionInternalPrincipalKey struct{}

func WithProductionInternalPrincipal(ctx context.Context, principal ProductionInternalPrincipal) (context.Context, error) {
	if ctx == nil || principal.ActorID != ProductionSystemActorID || principal.ActorKind != ProductionInternalActorWorker ||
		principal.TenantID == 0 || !canonicalPrincipalUUID(principal.ProjectID) || !canonicalPrincipalUUID(principal.RunID) {
		return nil, errors.New("invalid production internal principal")
	}
	ctx = context.WithValue(ctx, productionInternalPrincipalKey{}, principal)
	ctx = context.WithValue(ctx, TenantIDContextKey, principal.TenantID)
	ctx = context.WithValue(ctx, UserIDContextKey, principal.ActorID)
	return ctx, nil
}

func ProductionInternalPrincipalFromContext(ctx context.Context) (ProductionInternalPrincipal, bool) {
	if ctx == nil {
		return ProductionInternalPrincipal{}, false
	}
	principal, ok := ctx.Value(productionInternalPrincipalKey{}).(ProductionInternalPrincipal)
	return principal, ok
}

func (p ProductionInternalPrincipal) Matches(tenantID uint64, projectID, runID string) bool {
	return p.ActorID == ProductionSystemActorID && p.ActorKind == ProductionInternalActorWorker &&
		p.TenantID == tenantID && p.ProjectID == projectID && p.RunID == runID
}

func canonicalPrincipalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
