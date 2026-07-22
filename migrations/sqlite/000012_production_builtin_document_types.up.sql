ALTER TABLE production_document_types
    ADD COLUMN template_key VARCHAR(255) NULL;

ALTER TABLE production_document_types
    ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT 'custom'
    CONSTRAINT chk_production_document_types_origin
        CHECK (origin IN ('builtin', 'custom'))
    CONSTRAINT chk_production_document_types_builtin_template_key
        CHECK (origin <> 'builtin' OR (template_key IS NOT NULL AND template_key IN (
            'sop', 'policy_process', 'product_service_guide', 'faq', 'incident_playbook'
        )));

CREATE UNIQUE INDEX uq_production_document_types_live_template_version
    ON production_document_types (tenant_id, template_key, schema_version)
    WHERE template_key IS NOT NULL AND deleted_at IS NULL;

DROP TRIGGER IF EXISTS trg_production_document_types_prevent_active_definition_update;

CREATE TRIGGER trg_production_document_types_prevent_active_definition_update
    BEFORE UPDATE OF tenant_id, code, name, description, schema_version, block_schema,
        source_requirements, skill_bindings, workflow_plan, quality_rules, review_policy,
        publication_policy, origin, template_key, created_by ON production_document_types
    FOR EACH ROW
    WHEN OLD.status IN ('active', 'retired') AND (
        NEW.tenant_id IS NOT OLD.tenant_id OR
        NEW.code IS NOT OLD.code OR
        NEW.name IS NOT OLD.name OR
        NEW.description IS NOT OLD.description OR
        NEW.schema_version IS NOT OLD.schema_version OR
        NEW.block_schema IS NOT OLD.block_schema OR
        NEW.source_requirements IS NOT OLD.source_requirements OR
        NEW.skill_bindings IS NOT OLD.skill_bindings OR
        NEW.workflow_plan IS NOT OLD.workflow_plan OR
        NEW.quality_rules IS NOT OLD.quality_rules OR
        NEW.review_policy IS NOT OLD.review_policy OR
        NEW.publication_policy IS NOT OLD.publication_policy OR
        NEW.origin IS NOT OLD.origin OR
        NEW.template_key IS NOT OLD.template_key OR
        NEW.created_by IS NOT OLD.created_by
    )
BEGIN
    SELECT RAISE(ABORT, 'active production document type definitions are immutable');
END;

WITH builtin_definitions (
    code, name, description, block_schema, source_requirements, skill_bindings,
    workflow_plan, quality_rules, review_policy, publication_policy
) AS (
    VALUES
    (
        'sop', '标准作业程序', '面向可重复执行作业的受治理步骤、异常和证据规范',
        '{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["目的与范围","角色职责","前置条件","操作步骤","异常处理","风险控制","验证记录","证据清单"],"version":1}',
        '{"allow_unsupported_facts":false,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}',
        '{"skills":[],"version":1}', '{"steps":[],"version":1}',
        '{"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","sop_exception_path"],"require_evidence_for_facts":true,"version":1}',
        '{"steps":["business_reviewer"]}',
        '{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}'
    ),
    (
        'policy_process', '制度与流程规范', '面向制度依据、权责、审批和例外控制的企业规范',
        '{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["制定依据","适用范围","术语定义","职责权限","制度要求","业务流程","审批控制","例外处理","监督机制","证据清单"],"version":1}',
        '{"allow_unsupported_facts":false,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}',
        '{"skills":[],"version":1}', '{"steps":[],"version":1}',
        '{"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","policy_approval_control"],"require_evidence_for_facts":true,"version":1}',
        '{"steps":["business_reviewer","compliance_reviewer"]}',
        '{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}'
    ),
    (
        'product_service_guide', '产品与服务知识', '面向产品能力、适用边界、服务标准和升级路径的知识指南',
        '{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["产品定位","核心能力","适用场景","使用前提","配置与使用","限制条件","服务标准","常见故障","升级路径","证据清单"],"version":1}',
        '{"allow_unsupported_facts":false,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}',
        '{"skills":[],"version":1}', '{"steps":[],"version":1}',
        '{"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","product_scope_boundary"],"require_evidence_for_facts":true,"version":1}',
        '{"steps":["business_reviewer"]}',
        '{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}'
    ),
    (
        'faq', '常见问题与标准回答', '面向适用条件、标准回答、例外和时效的问答知识',
        '{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["问题分类","适用条件","标准问题与回答","例外情况","处理建议","升级路径","来源与生效日期"],"version":1}',
        '{"allow_unsupported_facts":false,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}',
        '{"skills":[],"version":1}', '{"steps":[],"version":1}',
        '{"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","faq_effective_date"],"require_evidence_for_facts":true,"version":1}',
        '{"steps":["business_reviewer"]}',
        '{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}'
    ),
    (
        'incident_playbook', '故障处理与案例复盘', '面向故障止损、诊断恢复、根因和改进行动的处置手册',
        '{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["现象与影响","事件等级","止损措施","诊断过程","恢复步骤","结果验证","沟通升级","根因分析","改进行动","证据清单"],"version":1}',
        '{"allow_unsupported_facts":false,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}',
        '{"skills":[],"version":1}', '{"steps":[],"version":1}',
        '{"block_needs_confirmation":true,"gates":["section_completeness","fact_evidence","no_unconfirmed","incident_timeline"],"require_evidence_for_facts":true,"version":1}',
        '{"steps":["engineering_reviewer","business_reviewer"]}',
        '{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}'
    )
)
INSERT INTO production_document_types (
    id, tenant_id, code, name, description, schema_version, block_schema,
    source_requirements, skill_bindings, workflow_plan, quality_rules,
    review_policy, publication_policy, status, origin, template_key, created_by
)
SELECT
    lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
        substr(lower(hex(randomblob(2))), 2, 3) || '-' ||
        substr('89ab', 1 + abs(random()) % 4, 1) ||
        substr(lower(hex(randomblob(2))), 2, 3) || '-' || lower(hex(randomblob(6))),
    tenants.id, definitions.code, definitions.name, definitions.description, 1,
    definitions.block_schema, definitions.source_requirements, definitions.skill_bindings,
    definitions.workflow_plan, definitions.quality_rules, definitions.review_policy,
    definitions.publication_policy, 'active', 'builtin', definitions.code,
    'system:builtin-document-types'
FROM tenants
CROSS JOIN builtin_definitions AS definitions
WHERE tenants.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM production_document_types AS existing
      WHERE existing.tenant_id = tenants.id
        AND existing.code = definitions.code
        AND existing.deleted_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM production_document_types AS existing
      WHERE existing.tenant_id = tenants.id
        AND existing.template_key = definitions.code
        AND existing.deleted_at IS NULL
  );
