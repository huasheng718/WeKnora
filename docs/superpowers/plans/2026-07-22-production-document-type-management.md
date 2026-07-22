# Production Document Type Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a tenant-scoped frontend management surface for listing, creating, and activating production document-type versions.

**Architecture:** Keep API transport in the existing production API module, put pure permission/form/row behavior in a focused view model, and keep network orchestration in one Vue page. Reuse the existing idempotent create/activate commands and tenant role computed state.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue Router, Vue I18n, TDesign Vue Next, Node test runner, Vite.

## Global Constraints

- Preserve the existing production backend API contract.
- Tenant `admin` and `owner` may create and activate; other roles are read-only.
- JSON policy fields default to `{}` and must be parsed before submission.
- Match the existing production workbench visual language and responsive behavior.
- Preserve the user-owned `.gitignore` change.

---

### Task 1: Document-type view model

**Files:**
- Create: `frontend/src/views/production/models/documentTypeManagement.ts`
- Test: `frontend/src/views/production/models/documentTypeManagement.test.ts`

**Interfaces:**
- Produces: `canManageProductionDocumentTypes(role)`, `documentTypeListViewState(input)`, `parseDocumentTypeDraft(input)`, and `sortDocumentTypeVersions(items)`.

- [ ] Write tests proving admin/owner access, read-only roles, loading/error/empty/ready states, JSON parsing failures, payload construction, and stable version ordering.
- [ ] Run `npm test -- src/views/production/models/documentTypeManagement.test.ts` and verify the missing module or exports fail the test.
- [ ] Implement the pure helpers and typed draft model with no Vue dependency.
- [ ] Re-run the focused test and verify it passes.

### Task 2: Route and composition contract

**Files:**
- Create: `frontend/src/views/production/productionDocumentTypeManagement.test.ts`
- Create: `frontend/src/views/production/ProductionDocumentTypeManagement.vue`
- Modify: `frontend/src/router/index.ts`
- Modify: `frontend/src/components/menuActiveState.ts`
- Modify: `frontend/src/components/menuActiveState.test.ts`
- Modify: `frontend/src/views/production/ProductionProjectList.vue`

**Interfaces:**
- Consumes: view-model helpers from Task 1 and existing production API functions.
- Produces: named route `productionDocumentTypes` at `/platform/knowledge-production/document-types`.

- [ ] Add source-contract and menu activation tests for the new route, header entry, read-only controls, request coordination, and lifecycle cleanup.
- [ ] Run the focused tests and verify they fail because the route/page/entry do not exist.
- [ ] Add the route and project-list header entry.
- [ ] Implement list, retry, empty, create drawer, JSON validation, confirmation, activation, and responsive local table scrolling.
- [ ] Re-run the focused tests and keep the existing route-group tests green.

### Task 3: Localized product copy

**Files:**
- Modify: `frontend/src/i18n/locales/zh-CN.ts`
- Modify: `frontend/src/i18n/locales/en-US.ts`
- Modify: `frontend/src/i18n/locales/ko-KR.ts`
- Modify: `frontend/src/i18n/locales/ru-RU.ts`
- Modify: `frontend/src/i18n/locales/workspaceTerminology.test.ts`

**Interfaces:**
- Produces: complete `production.documentTypes` and related permission/message keys in all supported locales.

- [ ] Add locale parity assertions for every new key and verify RED.
- [ ] Add concise translations for title, statuses, form labels, validation, confirmations, empty/error states, and feedback messages.
- [ ] Re-run locale tests and verify GREEN.

### Task 4: Regression and runtime verification

**Files:**
- Generated: `frontend/dist/**`

**Interfaces:**
- Consumes: completed Tasks 1-3.
- Produces: deployable frontend assets served by the existing local frontend container.

- [ ] Run `npm test` and confirm zero failures.
- [ ] Run `npm run type-check` and confirm zero TypeScript errors.
- [ ] Run `npm run build` and confirm Vite exits successfully.
- [ ] Verify the new page against the local backend at desktop and mobile widths, including load state, read-only/admin behavior, JSON validation, local table scrolling, and absence of console errors.
- [ ] Confirm `http://127.0.0.1:5173/platform/knowledge-production/document-types` serves the rebuilt page.
