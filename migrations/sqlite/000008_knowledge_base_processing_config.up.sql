ALTER TABLE knowledge_bases ADD COLUMN wiki_config TEXT DEFAULT NULL;

ALTER TABLE knowledge_bases ADD COLUMN indexing_strategy TEXT NOT NULL
    DEFAULT '{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false}';
