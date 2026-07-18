# 企业级知识生产中心设计

日期：2026-07-18

状态：待用户书面审核

适用基线：WeKnora `7b2c9e7`

## 1. 背景

WeKnora 当前已经具备知识库、文档解析、切片、Embedding、向量检索、知识图谱、Wiki、Agent Skills、MCP、数据源同步、异步任务、RBAC 和审计等能力。现有系统擅长把已经形成的资料导入知识库，但缺少资料进入知识库之前的企业级知识生产与治理流程。

本设计新增独立的“知识生产中心”。用户选择项目、时间范围和文档类型后，系统通过数据源、Skill 和 MCP 采集原始资料，AI 基于证据生成候选文档，人工进行标注和修订，指定角色完成会签，发布管理员确认目标知识库与处理配置，系统完成隔离构建并原子激活新版本。

首期只支持两类文档：

1. 软件研发基线
2. 项目复盘

## 2. 代码现状与设计约束

### 2.1 可直接复用的能力

- `KnowledgeBase` 已持有切片、Embedding、向量库、图谱、Wiki、存储和索引策略配置。
- `Knowledge` 已具备文档解析、后处理、启停、重解析和处理轨迹状态。
- Asynq 已按解析、后处理、摘要、图谱、Wiki、同步和维护划分队列。
- 图谱抽取已按 `KnowledgeBaseID + KnowledgeID` 建立命名空间。
- Wiki 已具备页面、目录、来源引用、版本、问题和操作日志。
- Skill 已具备预加载、按需读取和沙箱脚本执行。
- MCP 已具备服务配置、工具发现、OAuth 和工具审批策略。
- 数据源已具备飞书、Notion、语雀、RSS 连接器和定时同步框架。
- RBAC 已具备 Owner、Admin、Contributor、Viewer 四级空间角色和资源归属检查。
- `audit_logs` 是可扩展的通用不可变审计事件表。

### 2.2 不能直接复用的边界

- `Knowledge` 是知识库中的可检索投影，不是企业主文档。
- 手工知识 metadata 中的 `version` 只是当前内容版本号，没有不可变版本历史。
- `WikiPage.version` 只服务 Wiki 页面，不能表达标注、会签和发布快照。
- 当前 Skill HTTP 接口只提供预装 Skill 列表，不支持租户级版本和生产绑定。
- 当前 MCP 人工审批是交互式 Agent 调用中的等待机制，不适合长时间异步生产任务。
- 当前 RBAC 四级角色不能表达业务专家、研发专家、知识管理员等项目级职责。
- 当前知识处理在索引生成后会直接启用，不能保证旧版到新版的检索可见性原子切换。
- 当前 Wiki 是 KB 级持续合成结果，不属于首期原子发布门槛。

## 3. 目标与非目标

### 3.1 目标

- 在现有产品壳层中增加独立的知识生产一级入口。
- 管理项目、原始资料集、文档类型、主文档和不可变版本。
- AI 只能基于被确认的证据生成事实性内容，并保留来源追溯。
- 支持块级批注、修订建议和结构化质量标签。
- 支持按项目角色配置会签门槛和审核顺序。
- 审核通过后发布到一个或多个现有知识库。
- 新版本先隔离构建切片、向量和图谱，完成后按目标知识库原子激活。
- 保留历史发布并支持回滚。
- 全流程可审计、可重试、可解释。

### 3.2 非目标

- 首期不支持用户在线创建或上传自定义 Skill，只绑定系统预装 Skill。
- 首期不建设通用 BPMN 工作流设计器。
- 首期不支持多人同时编辑同一个内容块的实时协同算法。
- 首期不把 AI 自动生成的 Wiki 页面作为审核主文档。
- 首期不保证多个目标知识库之间的全局分布式事务；原子性边界是“主文档 × 目标知识库”。
- 首期不把 Wiki 页面重建纳入发布激活门槛。目标 KB 开启 Wiki 时，Wiki 作为激活后的派生任务运行。
- 首期不改变现有知识库、Agent、对话和发布集成的一级业务含义。

## 4. 核心原则

1. 主文档与知识库投影分离。生产中心保存权威内容，知识库保存可检索发布物。
2. 版本不可变。保存新版本，不覆盖已存在版本内容。
3. 审核绑定版本。审核过程中冻结被审核版本，任何修改产生新版本并使旧审核失效。
4. 证据优先。AI 生成的事实必须引用证据项，无证据内容必须明确标记。
5. 业务状态持久化。数据库记录流程状态，队列只负责唤醒和执行。
6. 先构建后激活。新投影构建失败时，旧投影保持可检索。
7. 每目标独立激活。多目标发布可以部分成功，失败目标可独立重试。
8. 基础 RBAC 与项目职责分层。空间角色控制平台权限，项目角色控制生产职责。

## 5. 总体架构

```text
知识生产中心
  ├── 项目与文档类型
  ├── 原始资料与证据快照
  ├── AI 生产编排
  ├── 结构化块编辑与版本
  ├── 标注与质量门禁
  ├── 角色会签
  └── 发布与回滚
          │
          ▼
发布投影适配层
  ├── 活动投影指针
  ├── 隔离构建
  ├── 检索范围解析
  └── 激活后清理
          │
          ▼
现有 WeKnora 能力
  ├── Knowledge / Chunk
  ├── 文档解析与 Embedding
  ├── 向量检索
  ├── 图谱抽取
  ├── Wiki 派生
  └── Asynq / 审计 / RBAC
```

新增领域应放在独立的 `production` 边界内，不把生产状态散落进现有 Knowledge Service。现有知识处理服务只接受发布投影适配层提供的构建请求。

## 6. 领域模型

### 6.1 项目与成员

`production_projects`

- `id`
- `tenant_id`
- `name`
- `description`
- `owner_user_id`
- `status`: `active | archived`
- `created_at`, `updated_at`, `deleted_at`

`production_project_members`

- `project_id`
- `user_id`
- `role`: `project_owner | author | business_reviewer | engineering_reviewer | knowledge_admin | compliance_reviewer | publisher | observer`
- `assigned_by`
- `created_at`, `deleted_at`

一个用户可以在同一项目拥有多个角色。用户必须是当前 tenant 的有效成员。

### 6.2 文档类型

`production_document_types`

- `id`, `tenant_id`
- `code`, `name`, `description`
- `schema_version`
- `block_schema`: 内容块结构和必填章节
- `source_requirements`: 原始资料要求
- `skill_bindings`: Skill 名称、摘要和内容摘要哈希
- `quality_rules`: 自动校验和阻断规则
- `review_policy`: 必需角色、顺序和会签规则
- `publication_policy`: 允许的目标 KB 类型和默认处理策略
- `status`: `draft | active | retired`
- `created_by`, `created_at`, `updated_at`

激活后的文档类型版本不可原地修改。修改配置时创建新的 `schema_version`，已有文档继续引用原版本。

### 6.3 原始资料与证据

`production_source_sets`

- `id`, `project_id`
- `document_type_id`
- `time_range_start`, `time_range_end`
- `status`: `collecting | ready | failed | frozen`
- `created_by`, `created_at`

`production_source_items`

- `id`, `source_set_id`
- `source_kind`: `upload | datasource | mcp | skill | manual`
- `source_system`, `external_id`, `source_uri`
- `title`, `mime_type`
- `content_digest`
- `captured_at`
- `metadata`
- `status`: `candidate | accepted | rejected | unavailable`

`production_evidence_snapshots`

- `id`, `source_item_id`
- `snapshot_type`: `text | json | file | tool_result`
- `storage_path` 或受限内联内容
- `content_digest`
- `redaction_metadata`
- `captured_by_run_id`
- `created_at`

资料集冻结后，AI 编写只读取冻结快照。外部来源发生变化不会静默改变已经生成的文档。

### 6.4 主文档、版本和内容块

`production_documents`

- `id`, `tenant_id`, `project_id`
- `document_type_id`, `document_type_schema_version`
- `title`
- `current_version_id`
- `latest_approved_version_id`
- `status`: `draft | annotating | in_review | approved | publishing | published | archived`
- `created_by`, `created_at`, `updated_at`

`production_document_versions`

- `id`, `document_id`
- `version_number`
- `parent_version_id`
- `source_set_id`
- `origin`: `ai | human | mixed | rollback`
- `change_summary`
- `content_digest`
- `created_by`, `created_at`
- `frozen_at`

`production_document_blocks`

- `id`: 当前版本内的块记录 ID
- `logical_block_id`: 跨版本稳定的逻辑块 ID
- `version_id`
- `block_type`: `heading | paragraph | list | table | image | code | callout`
- `position`
- `content`
- `attributes`
- `evidence_refs`
- `ai_provenance`: 生成 run、模型、Skill、提示模板版本

编辑操作从当前版本派生新版本。未发生语义变化的块沿用 `logical_block_id`；拆分或合并块时保存前后逻辑块映射，保证历史批注可追溯。数据库以块记录 `id` 为主键，并对 `(version_id, logical_block_id)` 建立唯一约束。

### 6.5 AI 运行和工具调用

`production_runs`

- `id`, `tenant_id`, `project_id`, `document_id`
- `run_type`: `collect | write | rewrite | validate`
- `status`: `queued | running | waiting_approval | completed | failed | cancelled`
- `model_id`
- `document_type_snapshot`
- `input_version_id`, `output_version_id`
- `started_at`, `completed_at`, `error_code`, `error_message`

`production_tool_calls`

- `id`, `run_id`
- `provider_type`: `skill | mcp | datasource`
- `provider_id`, `tool_name`
- `request_snapshot`
- `response_evidence_id`
- `status`: `planned | pending_approval | approved | rejected | executing | completed | failed`
- `approved_by`, `approved_at`
- `error_code`, `error_message`

生产任务不得在 worker 内长时间等待人工审批。调用需要审批时，持久化 `pending_approval` 并结束当前任务；审批通过后重新入队继续执行。

### 6.6 标注和审核

`production_annotations`

- `id`, `document_id`, `version_id`, `block_id`
- `annotation_type`: `comment | suggestion | quality_tag`
- `quality_tag`: `missing_evidence | factual_risk | unclear | incomplete | conflict | compliance_risk`
- `severity`: `info | warning | blocking`
- `anchor`: 块内文本范围或结构路径
- `content`, `suggested_content`
- `status`: `open | resolved | dismissed`
- `created_by`, `resolved_by`, `created_at`, `resolved_at`

`production_review_requests`

- `id`, `document_id`, `version_id`
- `policy_snapshot`
- `status`: `pending | approved | rejected | obsolete | cancelled`
- `submitted_by`, `submitted_at`, `completed_at`

`production_review_steps`

- `id`, `review_request_id`
- `required_role`
- `sequence`
- `reviewer_user_id`
- `decision`: `pending | approved | changes_requested | rejected`
- `comment`, `decided_at`

存在未关闭的 blocking 标注时禁止提交审核。审核开始后版本冻结。任何内容修改产生新版本，并将未完成审核标记为 `obsolete`。

### 6.7 发布和知识库投影

`production_releases`

- `id`, `document_id`, `version_id`
- `release_number`
- `status`: `preparing | building | partially_published | published | failed | rolled_back`
- `created_by`, `confirmed_by`, `created_at`, `completed_at`

`production_release_targets`

- `id`, `release_id`, `target_knowledge_base_id`
- `process_config_snapshot`
- `status`: `pending | building | ready | activating | active | failed | rolled_back`
- `knowledge_id`
- `previous_projection_id`
- `error_code`, `error_message`

`production_projection_heads`

- `tenant_id`
- `document_id`
- `target_knowledge_base_id`
- `active_release_target_id`
- `lock_version`
- `updated_at`

每个 `(document_id, target_knowledge_base_id)` 只能有一个活动投影。`lock_version` 用于乐观并发控制，防止两个发布并发覆盖。

## 7. 两类首期文档

### 7.1 软件研发基线

必需章节：

1. 基线范围与目标
2. 需求基线
3. 产品与交互设计基线
4. 技术方案与架构基线
5. 代码仓库、分支与提交基线
6. 依赖和运行环境基线
7. 测试、质量和安全基线
8. 发布、部署和回滚基线
9. 已知风险、例外和遗留项
10. 证据清单

质量门禁至少检查：必需章节、基线时间、仓库和提交引用、测试结果、发布版本、风险责任人、证据覆盖率。

默认会签角色：产品或业务专家、研发专家、知识管理员。启用合规规则时追加合规审核人。

### 7.2 项目复盘

必需章节：

1. 项目背景和目标
2. 关联研发基线
3. 计划与实际结果
4. 需求和范围变化
5. 质量、交付和运营数据
6. 事故、偏差和影响
7. 根因分析
8. 有效实践和经验
9. 改进行动项、负责人和截止时间
10. 证据清单

质量门禁至少检查：关联基线、目标数据、事实与观点区分、根因和现象区分、行动项责任人、截止时间、证据覆盖率。

默认会签角色：项目负责人、业务专家、研发专家、知识管理员。

## 8. 端到端流程

### 8.1 创建与采集

1. 用户创建项目或进入已有项目。
2. 选择文档类型、时间范围和资料来源。
3. 系统创建资料集并异步采集。
4. 每个来源生成不可变证据快照。
5. 用户接受或拒绝候选资料。
6. 用户确认后冻结资料集。

### 8.2 AI 编写

1. 系统载入文档类型快照和冻结资料集。
2. 编排器按 Skill 指令生成采集和写作步骤。
3. MCP 工具遵守已有工具审批策略。
4. 工具输出先保存证据，再进入模型上下文。
5. 模型按结构化块协议输出候选内容和证据引用。
6. 服务端校验块结构、引用和内容摘要。
7. 校验通过后创建不可变文档版本。

AI 无法找到证据时，只能生成明确的“待确认”块或缺失证据标签，不能把推断写成已确认事实。

### 8.3 人工标注与修订

1. 用户在块内添加批注、修订建议或质量标签。
2. 接受修订时创建新版本并保留旧版本。
3. 版本对比按稳定块 ID 显示新增、删除、修改和移动。
4. blocking 标注全部关闭后才能提交审核。

### 8.4 角色会签

1. 提交时生成审核单并冻结版本。
2. 系统按文档类型策略生成审核步骤。
3. 审核人通过、要求修改或拒绝。
4. 要求修改后，编写人派生新版本并重新提交。
5. 所有必需角色通过后，版本状态变为 `approved`。

### 8.5 发布确认与隔离构建

1. 发布管理员选择一个或多个目标知识库。
2. 系统校验目标 KB 写权限、模型、存储、向量库和图谱配置。
3. 用户预览 Markdown 发布快照、切片配置和图谱配置。
4. 确认后为每个目标创建独立 release target。
5. 适配层创建新的 Knowledge 投影和隔离构建任务。
6. 构建完成后执行质量检查，目标状态进入 `ready`。

### 8.6 原子激活

检索可见性不能依赖“先启用新版，再禁用旧版”的两次外部写操作。设计采用活动投影指针：

1. 新 Knowledge 使用新的 Knowledge ID，因此 Chunk、向量和图谱与旧版天然隔离。
2. 构建阶段允许索引存在，但普通检索只接受 `production_projection_heads` 解析出的活动 Knowledge ID。
3. 激活时锁定 projection head，比较 `lock_version`，一次数据库事务切换活动 target。
4. 事务提交后，新请求只检索新 Knowledge ID，旧投影立即不可见。
5. 后台异步关闭旧 Knowledge/Chunk/向量的 enabled 状态并执行保留策略。
6. 后台清理失败不会改变活动指针，可通过维护任务重试。

图谱查询同样必须带活动 Knowledge ID 范围，避免 KB 级查询同时返回新旧版本节点。

多目标发布按 target 独立激活。部分目标失败时，成功目标保持 active，release 标记为 `partially_published`，用户可以只重试失败目标。

### 8.7 回滚

1. 发布管理员选择历史活动 target。
2. 系统确认对应索引仍在保留期内且可查询。
3. 使用相同 projection head 事务切换回历史 target。
4. 回滚动作创建新的审计事件，不修改历史 release。
5. 若历史索引已清理，系统先重建，完成后再激活。

## 9. 后端组件边界

### 9.1 Production Service

负责项目、文档类型、资料集、主文档、版本、标注、审核和发布业务规则。它不直接实现解析、向量和图谱算法。

建议目录：

- `internal/types/production_*.go`
- `internal/types/interfaces/production_*.go`
- `internal/application/repository/production_*.go`
- `internal/application/service/production_*.go`
- `internal/handler/production_*.go`

### 9.2 Production Orchestrator

负责采集、Skill/MCP 调用、模型写作、校验和持久化运行状态。每一步是可恢复状态机，不能依赖单个进程内存。

### 9.3 Publication Projection Adapter

负责把 approved version 渲染为现有 Knowledge 可处理的输入，创建隔离投影、监听处理完成、执行激活和回滚。

它是 Production Service 与现有 Knowledge Service 之间唯一的写入接口。禁止前端绕过发布流程直接把主文档内容提交到知识库。

### 9.4 Active Projection Resolver

负责在检索、知识问答和图谱查询前，把目标 KB 扩展为当前活动的生产 Knowledge ID。普通手工上传和外部导入的 Knowledge 不属于生产投影，继续按现有 KB 规则检索。

检索范围为：

```text
不属于生产投影的普通启用 Knowledge
  UNION
每个生产主文档在该 KB 的活动 Knowledge 投影
```

Resolver 先通过 release target 关系识别所有生产投影，将它们从普通 Knowledge 集合中排除，再加入 projection head 指向的活动 Knowledge ID。生产投影的非活动版本必须从检索范围排除，即使其 Chunk 或外部向量索引暂时仍处于 enabled 状态。

## 10. API 设计

API 前缀：`/api/v1/production`

主要资源：

- `GET/POST /projects`
- `GET/PUT /projects/:id`
- `GET/POST/DELETE /projects/:id/members`
- `GET/POST /document-types`
- `PUT /document-types/:id/activate`
- `GET/POST /projects/:id/source-sets`
- `POST /source-sets/:id/collect`
- `PUT /source-items/:id/decision`
- `POST /source-sets/:id/freeze`
- `GET/POST /projects/:id/documents`
- `GET /documents/:id`
- `GET /documents/:id/versions`
- `POST /documents/:id/runs`
- `POST /documents/:id/versions`
- `GET/POST/PUT /documents/:id/annotations`
- `POST /documents/:id/reviews`
- `POST /reviews/:id/steps/:step_id/decision`
- `POST /tool-calls/:id/decision`
- `POST /documents/:id/releases`
- `POST /release-targets/:id/activate`
- `POST /release-targets/:id/retry`
- `POST /release-targets/:id/rollback`

所有写接口接受 `Idempotency-Key`。版本更新和活动投影切换同时使用 `If-Match` 或显式 `lock_version` 防止丢失更新。

## 11. 异步任务

新增独立队列 `production`，避免长写作任务占用交互式对话和知识解析容量。

任务类型：

- `production:collect`
- `production:write`
- `production:validate`
- `production:build`
- `production:activate`
- `production:cleanup`

每个任务 payload 只保存业务记录 ID、tenant ID、attempt 和 tracing context。完整输入和输出保存在数据库或对象存储中。

任务必须满足：

- 幂等执行
- attempt 可追踪
- 超时和取消
- 可重试错误与不可重试错误分类
- 失败写入业务状态和死信记录
- 服务重启后可恢复

## 12. 权限设计

### 12.1 空间角色底线

- Viewer：只读被授权项目和已发布内容。
- Contributor：可被分配 author、reviewer、observer 项目角色。
- Admin/Owner：可创建文档类型、配置 Skill/MCP 绑定、分配项目角色和管理发布策略。
- Owner：保留 tenant 删除、所有权转移等现有专属权限。

### 12.2 项目角色能力

- `project_owner`：管理项目范围和成员。
- `author`：采集资料、运行 AI、编辑和提交审核。
- `business_reviewer`：业务事实和目标审核。
- `engineering_reviewer`：技术、代码、测试和发布基线审核。
- `knowledge_admin`：结构、证据、知识质量和目标 KB 审核。
- `compliance_reviewer`：合规和敏感信息审核。
- `publisher`：确认发布、激活和回滚。
- `observer`：只读项目过程。

审核决定还必须满足当前审核单的 required role，不能仅凭空间 Admin 绕过。Admin/Owner 可执行紧急管理员否决或取消，但不能伪造某个专业角色的通过记录。

## 13. 审计与安全

新增审计 action 至现有 `audit_logs`：

- `production.project_created`
- `production.source_collected`
- `production.source_frozen`
- `production.ai_run_started`
- `production.ai_run_completed`
- `production.tool_call_approved`
- `production.version_created`
- `production.annotation_resolved`
- `production.review_submitted`
- `production.review_decided`
- `production.release_confirmed`
- `production.projection_activated`
- `production.projection_rolled_back`

安全要求：

- MCP 和数据源凭据继续使用现有凭据存储，不写入证据快照。
- 工具请求和响应保存前执行字段级脱敏。
- 证据下载使用 tenant 和项目权限校验。
- 生产 Skill 仍在现有沙箱内执行，默认无网络和无项目目录写权限。
- 文档块、证据和工具结果保存内容摘要，审核和发布前重新校验。
- 发布快照不可包含未关闭的合规阻断标签。

## 14. 前端信息架构

### 14.1 一级入口

在现有侧栏“知识库”之后增加“知识生产”。它是跨知识库流程，不放进单个知识库详情页。

现有“发布集成”仍属于 Settings/Integrations，不与生产文档发布混为一谈。

### 14.2 页面

1. 生产项目列表：项目、文档数量、待审核、待发布、最近活动。
2. 项目工作台：资料、文档、审核、发布四个视图。
3. 文档工作台：左侧目录和证据，中间结构化块编辑器，右侧 AI、标注、质量和版本面板。
4. 审核视图：冻结版本、版本差异、证据、阻断项、角色会签状态。
5. 发布确认：目标 KB、配置快照、预览、逐目标构建和激活状态。

页面复用现有 Platform 壳层、TDesign 组件、权限 Store、任务状态和消息模式。生产工作台不复制知识库列表、设置页或 Agent 编辑器。

## 15. 错误处理与并发

- 采集失败：保留成功来源，资料集显示部分失败，可按来源重试。
- 工具审批拒绝：运行进入 failed 或等待用户调整范围，不自动绕过工具。
- AI 输出结构非法：保存原始运行结果用于审计，不创建文档版本，允许校验重试。
- 内容摘要不一致：阻止审核或发布并记录安全审计。
- 审核并发：版本冻结后禁止写；新修改只能派生新版本。
- 发布并发：projection head 使用行锁和 `lock_version`，旧 lock 失败返回冲突。
- 目标 KB 配置变化：构建使用确认时快照；激活前重新校验关键资源仍可用。
- 构建失败：旧活动投影不变，失败 target 可独立重试。
- 激活后清理失败：新投影保持活动，维护任务继续清理旧索引。
- 用户取消：仅取消未激活 target；已激活 target 必须通过回滚处理。

## 16. 兼容与迁移

- 现有 Knowledge、Wiki、Agent、会话和数据源记录不迁移。
- 现有手工 Markdown 知识不自动转换为主文档。
- 生产发布的 Knowledge 在 metadata 中保存 `production_document_id`、`production_version_id`、`release_target_id` 和内容摘要。
- Knowledge API 增加只读来源标识，防止用户在知识库界面直接编辑生产投影。
- 删除生产投影时必须通过发布服务，普通 Knowledge 删除接口拒绝删除活动投影。
- PostgreSQL 和 Lite/SQLite 都要提供等价表结构与状态机语义。

## 17. 测试策略

### 17.1 单元测试

- 文档状态机和非法跃迁
- 版本派生和稳定块 ID 映射
- 证据引用完整性
- 阻断标签门禁
- 审核策略快照和角色匹配
- projection head 乐观锁和回滚
- 幂等键和重复任务处理

### 17.2 Repository 测试

- tenant 隔离
- 不可变版本约束
- 唯一活动投影约束
- 并发审核和发布事务
- PostgreSQL 与 SQLite 行为一致性

### 17.3 集成测试

- 资料采集到 AI 版本生成
- MCP 需要审批时的暂停和恢复
- 审核版本冻结和 obsolete 处理
- 新版构建期间旧版持续可检索
- projection head 切换后只返回新版
- 图谱查询排除非活动 Knowledge
- 多目标部分失败和独立重试
- 回滚到保留期内历史版本

### 17.4 前端端到端测试

- 不同空间角色和项目角色的入口可见性
- 创建资料集、确认资料、生成文档
- 块级标注、修订、版本对比
- 会签通过和要求修改
- 发布预览、构建状态、激活和回滚
- 桌面和移动宽度下无内容遮挡和布局溢出

## 18. 验收标准

1. 用户可以从现有侧栏进入知识生产中心，不影响原有一级导航。
2. 用户可以创建研发基线或项目复盘，并通过半自动方式形成冻结资料集。
3. AI 生成的每个事实性块都能查看证据来源或明确显示缺失证据。
4. 人工可以添加批注、修订建议和 blocking 质量标签。
5. blocking 标签未关闭时系统拒绝提交审核。
6. 审核单绑定不可变版本，并按角色门槛完成会签。
7. 审核通过后发布管理员可以选择一个或多个现有知识库。
8. 新版构建期间旧版继续可检索，构建失败不影响旧版。
9. 激活后检索和图谱只返回活动投影，不混入旧版内容。
10. 多目标发布允许部分成功并可独立重试失败目标。
11. 用户可以回滚到仍在保留期内的历史投影。
12. 关键采集、AI、标注、审核、发布和回滚动作都有审计记录。

## 19. 后续实施分解边界

本设计范围较大，实施计划必须拆成可独立验证的阶段，但保持同一领域模型和 API 契约：

1. 领域基础：表结构、Repository、状态机、项目权限和审计。
2. 文档生产：资料集、证据、结构化版本、标注和两类模板。
3. AI 编排：Skill、MCP、持久化工具审批和运行恢复。
4. 会签治理：审核策略、版本冻结和质量门禁。
5. 发布投影：隔离构建、活动范围解析、激活、清理和回滚。
6. 前端工作台：项目、文档、审核和发布全流程。

每个阶段完成后都必须通过对应单元测试、集成测试和回归测试，不能在发布投影尚未具备活动范围隔离时对用户开放“发布”入口。
