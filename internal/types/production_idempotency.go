package types

import (
	"time"
)

// ProductionIdempotencyKey stores the durable result of a production write
// request, keyed by its tenant, actor, route, and idempotency key.
type ProductionIdempotencyKey struct {
	ID             string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID       uint64     `json:"tenant_id" gorm:"not null;index"`
	ActorUserID    string     `json:"actor_user_id" gorm:"type:varchar(36);not null"`
	Route          string     `json:"route" gorm:"type:varchar(255);not null"`
	IdempotencyKey string     `json:"idempotency_key" gorm:"type:varchar(255);not null"`
	RequestDigest  string     `json:"request_digest" gorm:"type:varchar(64);not null"`
	StatusCode     *int       `json:"status_code,omitempty"`
	ResponseBody   JSON       `json:"response_body,omitempty" gorm:"type:jsonb"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// TableName binds ProductionIdempotencyKey to the production_idempotency_keys table.
func (ProductionIdempotencyKey) TableName() string { return "production_idempotency_keys" }
