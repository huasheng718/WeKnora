CREATE TABLE IF NOT EXISTS temporary_documents (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    session_id VARCHAR(36) NOT NULL,
    resource_ref TEXT NOT NULL,
    file_name VARCHAR(1024) NOT NULL,
    file_type VARCHAR(32) NOT NULL,
    mime_type VARCHAR(255) NOT NULL DEFAULT '',
    file_size INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'uploaded',
    content TEXT NOT NULL DEFAULT '',
    chunks TEXT NOT NULL DEFAULT '[]',
    image_refs TEXT NOT NULL DEFAULT '[]',
    metadata TEXT NOT NULL DEFAULT '{}',
    processing_options TEXT NOT NULL DEFAULT '{}',
    token_count INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    started_at DATETIME,
    ready_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_temporary_documents_scope ON temporary_documents(tenant_id, session_id);
CREATE INDEX IF NOT EXISTS idx_temporary_documents_status ON temporary_documents(status);
CREATE INDEX IF NOT EXISTS idx_temporary_documents_expires ON temporary_documents(expires_at);
