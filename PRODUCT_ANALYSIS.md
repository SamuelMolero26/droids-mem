# droids-mem Product Analysis

**Date:** 2026-06-17  
**Analyst:** Claude Business Analyst  
**Status:** v1.0 GA shipped 2026-06-09; feat/p3-UX in-flight

---

## 1. Current Versioning State

### Released
- **v1.0.0** shipped **2026-06-09** (public GA)
- Four prebuilt binaries: `darwin/{amd64,arm64}`, `linux/{amd64,arm64}`
- All 4 binaries + SHA256 checksums on GitHub Releases page
- MIT license, open-source

### In-Flight
- **feat/p3-UX/ergonomics** branch (commit f467359)
  - Context modes (ADR-0012): full, compact, brief, custom granularity
  - Expand signal on memories (ADR-0013): reveal hidden context via mem_get
  - Prune-by-id (ADR-0014): granular delete instead of bulk prune
  - Memory inspector TUI: interactive terminal UI for memory exploration
  - Performance tuning: undocumented optimizations
  - Status: lint fixed; ready for review/merge

### Release Maturity
- **GA** (v1.0): Comprehensive e2e tests, race detector clean, CI passing (golangci-lint, gosec, CodeQL)
- **ADR-driven**: 15 ADRs document all major decisions (0001–0015)
- **Deferred to v1.1/v1.2**: Workspace isolation, git-sync, scrub tuning

---

## 2. Strengths

### A. Security & Privacy (Differentiator)
- **18-pattern PII scrub pipeline** (ADR-0008):
  - Provider class: 11 vendor-specific patterns (AWS, GitHub, GitLab, Google, npm, Stripe, Slack, Anthropic, OpenAI, etc.)
  - Usage class: 3 context-based patterns (Bearer header, assignment, URL credential) with entropy gating (≥3.5 bits/char)
  - PII class: 4 always-on patterns (phone, email, private IPv4)
- **Version-pinned declarative spec** (YAML, embedded, SHA-256 hash tracked)
- **Deterministic redaction** (same input → same output across machines)
- **Strict-reject routing keys**: task_type, tags, session_id must not match scrub patterns; corruption rejected (not silently stripped)
- **No external services**: Complete data residency. No telemetry. No API calls.
- **Bearer token security**: Auto-generated, 0600 permissions. HMAC-SHA256 identity proofs prevent port-squatting.

### B. Architecture & Engineering (Enterprise-Grade)
- **Single CGO-free binary** (18 MB pure-Go SQLite + FTS5)
- **Two-layer deduplication**:
  - Layer 1: SHA-256 fingerprint (title+learned+task_type+kind). Exact match → skip.
  - Layer 2: BM25 top-20 candidates re-ranked by Jaccard token-set similarity (threshold: ≥0.85). Catches paraphrases.
- **Two-tier context bundle**:
  - Always-tier: full bodies of latest session_summary + all user_rules (high-signal, full text)
  - Browse-tier: ≤10 error_resolution, ≤10 task_pattern, rule stubs (title-only for overflow)
  - Fixed ceiling: ~34 KB (predictable token budget for agents)
- **Idempotent zero-config startup** (ensure-server: ping /healthz, auto-spawn if down)
- **Parent-as-broker pattern** (ADR-0004): Only Root agent writes to droids-mem. Sub-agents get context injected, never direct write access.
- **Comprehensive e2e testing**: CLI + MCP bridge, race detector clean, corpus tests for scrub false-positive rate

### C. Product Design (Principled)
- **Frozen 4-kind enum** (session_summary, task_pattern, error_resolution, user_rule)
  - No observation kind (intentional: ADR-0004 defers high-volume step outputs to execution logs)
  - Prevents schema sprawl; keeps contract stable
- **No automatic retention** (ADR-0010):
  - Manual prune with dupe-cluster suggestions
  - Doctor warnings on size/row thresholds
  - Transparent user control (not silent cleanup)
- **Consumer pattern centralizes writes**: Prevents fanout; manages session lifecycle at Root
- **Clear subagent contract**: They consume Bundle, never write directly (no memory tools in subagent config)
- **Comprehensive ADR documentation**: 15 ADRs covering all architectural decisions (fingerprint scope, tier model, MCP bridge, workspace model, scrub engine, retention policy, etc.)

### D. Performance (Meets Targets)
| Metric | Target | Status |
|--------|--------|--------|
| Search latency p95 | <100ms | ✓ Achieved (FTS5 BM25) |
| Binary startup | <200ms | ✓ Achieved (pure-Go) |
| Binary size | <20 MB | ✓ Achieved (18 MB) |
| Scrub on 10 KB body p95 | <500 µs | ✓ Achieved (single-pass) |
| Context payload ceiling | ~34 KB | ✓ Achieved (two-tier caps) |
| ensure-server cold start | <300ms | ✓ Achieved |

---

## 3. Gaps & Weaknesses

### A. Workspace Isolation (Blocks Team Adoption)
- **Issue**: v1.0 ships single-DB local-only. Multi-project teams must share one memory store.
- **Impact**: Cannot isolate by project, workspace, or deployment stage. Lessons from Project A pollute Project B's context.
- **Status**: Deferred to v1.1 (ADR-0005, 106 lines, mostly design complete)
- **Effort**: Medium (3–4 weeks). Requires schema versioning, workspace.yml config, scope-aware filters.

### B. Git-Native Sync (Blocks Team Collaboration)
- **Issue**: No built-in way for teams to share learned lessons across machines.
- **Impact**: Each developer maintains isolated memory. JSONL export exists but requires manual round-trip.
- **Status**: Deferred to v1.2 (ADR-0006, 169 lines, design complete, implementation pending)
- **Effort**: High (4–6 weeks). Requires JSONL determinism, conflict resolution, audit trail.
- **Gap vs Engram**: Engram emphasizes per-project memory and cross-session persistence; droids-mem roadmap includes this but not yet shipped.

### C. Scrub Policy Tuning (Blocks Custom Enterprises)
- **Issue**: v1.0 has hardcoded pattern set and order. No per-workspace extra_patterns or disabled_patterns config.
- **Impact**: Cannot suppress false positives (e.g., allow company-specific token prefix that collides with scrub pattern).
- **Status**: Deferred to v1.1, depends on workspace isolation
- **Effort**: Medium (2–3 weeks). Requires workspace.yml config, pattern registry refactor.

### D. Rule Accumulation Without Compaction
- **Issue**: user_rule rows grow unbounded over time. No auto-compaction of superseded or stale rules.
- **Impact**: Always-tier could grow beyond ideal token budget if rules accumulate without curation.
- **Status**: Acknowledged (ADR-0002 "Consequences" section). Not yet implemented.
- **Effort**: Medium (2–3 weeks). Requires deprecation markers, signal collection (recency, usage), compaction rules.

### E. No Audit Log or Change History
- **Issue**: Cannot audit who/what changed a memory, when, or why. No export for compliance.
- **Impact**: Debugging is hard (lost context on when a lesson was added/modified). Compliance is impossible.
- **Status**: Not yet planned. Would be valuable for enterprise adoption.
- **Effort**: Medium (2–3 weeks). Requires audits table, indexed queries, export commands.

### F. Context Modes (In-Flight, Not Shipped)
- **Issue**: ADR-0012 defines context modes (full, compact, brief) but implementation is on feat/p3-UX branch.
- **Impact**: Agents cannot request context at different granularities per task. One size fits all.
- **Status**: ~70% done on feat/p3-UX. Ready for merge.
- **Effort**: Low-Medium (1–2 weeks remaining on that branch).

### G. No Enterprise SaaS Offering
- **Issue**: Self-hosted only. No managed service, no multi-tenancy, no deployment automation.
- **Impact**: Teams prefer SaaS (zero ops overhead). Competitor mem0.ai has managed offering.
- **Status**: Not planned for open-source. Potential future commercial version.
- **Effort**: High (6–8 weeks). Requires multi-tenant schema, auth layer, deployment ops.

### H. Limited Retrieval Granularity (vs Engram)
- **Issue**: Two-tier bundle is fixed; no topic keys or timeline navigation like engram.
- **Impact**: Agents browse all rules at once; cannot navigate by topic (e.g., "show me rules about caching").
- **Status**: Partially addressed by ADR-0012 context modes (in-flight) and ADR-0013 expand signal.
- **Effort**: Medium. Requires ranking refinement, filtering, optional topic tagging.

---

## 4. Differentiation vs Engram

### Head-to-Head Comparison

| Dimension | droids-mem | engram | Winner |
|-----------|-----------|--------|--------|
| **Privacy Model** | Deterministic local PII scrub (18 patterns, version-pinned) | Implicit (no public scrub spec) | droids-mem (auditable, explicit) |
| **Deduplication** | Two-layer (fingerprint + Jaccard 0.85) | Implicit (not documented) | droids-mem (transparent) |
| **Write Pattern** | Parent-as-broker (Root writes, subagents read) | Passive capture (auto-saves) | Engram (simpler, less coordination) |
| **Context Delivery** | Two-tier bundle (always + browse, ~34 KB) | Flexible retrieval + topic keys | Engram (more flexible) |
| **Workspace Isolation** | Deferred v1.1 (planned) | Per-project (shipped) | Engram (available now) |
| **Team Sync** | Deferred v1.2 (git-JSONL planned) | Cross-session persistence (implicit) | Engram (simpler) |
| **Observation Kind** | Rejected (ADR-0004, deferred to exec logs) | Supported (high-volume step outputs) | Engram (more granular) |
| **Licensing** | MIT open-source | Proprietary (Anthropic internal) | droids-mem (open) |
| **Maturity** | v1.0 GA (just shipped) | Mature (integrated in Claude Code) | Engram (battle-tested) |
| **Audit Trail** | Not implemented | Not documented | Neither (both gaps) |
| **Semantic Search** | BM25 only (no embeddings) | Not documented | Tie (both keyword-based in v1) |

### droids-mem Wins
1. **Security & transparency**: Explicit, auditable PII scrub pipeline. Version-pinned patterns. Strict-reject routing keys.
2. **Determinism**: Same input always produces same output (no surprise redactions on re-scrub).
3. **Open-source**: MIT licensed, full source available, community-auditable.
4. **Two-layer dedupe**: Reduces noise more aggressively than implicit dedup.
5. **Principled design**: ADR-driven decisions, frozen schema, no automatic retention (user control).

### Engram Wins
1. **Workspace isolation**: Per-project memory shipped (droids-mem deferred to v1.1).
2. **Passive capture**: Auto-saves without explicit Root coordination (less friction for agents).
3. **Observations**: First-class support for high-volume, low-distillation step outputs.
4. **Maturity**: Integrated in Claude Code, battle-tested with real agents.
5. **Flexible retrieval**: Topic keys, timeline navigation (droids-mem context modes coming).
6. **Seamless integration**: Built into Claude ecosystem (engram seamless; droids-mem requires explicit MCP setup).

### Market Position
- **droids-mem**: Open-source, locally-first, security-focused, ideal for **teams that want full control, on-premise data, and auditable redactions**.
- **engram**: Proprietary, integrated, capture-focused, ideal for **Anthropic Claude users who want minimal friction and implicit memory**.
- **Non-overlapping niche**: Teams doing compliance-heavy AI (healthcare, finance) will prefer droids-mem. Teams seeking convenience will prefer engram (if using Claude). droid-mem will appeal to the open-source community and enterprises that don't use Claude exclusively.

---

## 5. Proposed New Features (Prioritized Roadmap)

### P0: Must Ship Before v1.1 Release

#### 1. Workspace Isolation (ADR-0005 Implementation)
**Why**: Gate for team adoption. Multi-project teams need isolated memory per project.

**What**:
- Multi-DB support: droids-mem init-workspace, switch-workspace, list-workspaces
- workspace.yml: metadata, optional config stubs (scrub tuning, context modes)
- Schema versioning: scope-aware schema migration
- Scope-aware filters: mem_context, mem_search filter by workspace

**Business Value**: Enables teams to adopt droids-mem without mixing lessons across projects. Differentiator vs engram (which has per-project but droid's explicit config is more transparent).

**Effort**: 3–4 weeks (medium). Blockers: schema migration tooling, workspace.yml parser.

**Success Metrics**: 
- >80% of test scenarios pass with multi-workspace
- <150ms cold-start per workspace
- Zero data leakage across workspaces

---

#### 2. Scrub Policy Tuning (Per-Workspace Config)
**Why**: Reduces false-positive redactions; enables custom enterprises with vendor-specific prefixes.

**What**:
- workspace.yml: extra_patterns, disabled_patterns, enabled_optional_patterns
- Pattern registry refactor: move from hardcoded slice to YAML+loader
- doctor --scrub-stats per-workspace: aggregation, per-pattern hit counts
- scrub_pattern_version tracking per memory

**Business Value**: Reduces support requests from teams with custom secrets (internal APIs, etc.). Improves signal quality.

**Effort**: 2–3 weeks (depends on workspace isolation).

**Success Metrics**:
- >95% of custom patterns can be tuned via workspace.yml
- Zero breaking changes to v1.0 scrub behavior when pattern set unchanged
- doctor --scrub-stats completes in <500ms

---

### P1: High Value, Ship in v1.1 or v1.2

#### 3. Workspace-Aware Context Modes (ADR-0012 Implementation)
**Why**: Agents need context at different granularities per task (brief for quick checks, full for complex decisions).

**What** (mostly done on feat/p3-UX):
- mem_context(context_mode: full | compact | brief | custom)
  - full: current always-tier + full browse (30–50 items)
  - compact: latest summary + top 5 rules (2–3 KB, 50–100 tokens)
  - brief: just session ID (500 bytes, 20 tokens)
  - custom: caller-specified tier sizes
- Adjust tier caps per mode
- Scope-aware rule filtering (tag-based, type-based)
- Re-rank browse by relevance signal (unused rules deprioritized)

**Business Value**: Reduces token overhead for routine tasks (brief). Enables richer context for complex decisions (full). Differentiator: engram has flexible retrieval; droids-mem with context modes matches that.

**Effort**: 1–2 weeks (API surface defined; implementation 70% done on feat/p3-UX).

**Success Metrics**:
- brief mode: <1 KB context payload
- compact mode: <5 KB context payload
- <50ms penalty for mode switching

---

#### 4. Git-Native JSONL Sync (ADR-0006 Implementation)
**Why**: Enables teams to share learned lessons across machines/repos via git. Distributed, async, peer-to-peer.

**What**:
- mem export --format jsonl: serialize memories to .droids-mem/sync/memories.jsonl
- mem import --format jsonl: deserialize and merge
- Conflict resolution: last-write-wins, merge-base detection, optional manual merge
- Audit trail: who imported, when, which memories changed
- droids-mem sync subcommand: git subdir for team-shared memory
- Determinism testing: export→import→export byte-identical

**Business Value**: Huge for team adoption. Reduces onboarding time. Differentiator vs engram (which doesn't emphasize git sync). Enables distributed teams to benefit from shared knowledge.

**Effort**: 4–6 weeks (high). Depends on workspace isolation. Roundtrip determinism is non-trivial.

**Success Metrics**:
- >99% JSONL roundtrip determinism (export→import→export byte-identical)
- Merge handles <5s conflict resolution time
- <100ms import on 10k memories

---

#### 5. Audit Log & Change History
**Why**: No way to audit memory changes. Critical for compliance, debugging, and enterprise adoption.

**What**:
- New audits table: (id, memory_id, action, actor, timestamp, delta_before, delta_after)
- doctor --audit-summary: summary of recent changes, top editors, change patterns
- mem audit --id <id>: full change history for one memory
- Export: audit export --format csv/json (for compliance reporting)
- Change tracking: auto-log all save/delete/update operations

**Business Value**: Required for compliance (finance, healthcare). Improves debuggability (when did this rule get added?). Builds trust (transparent change history).

**Effort**: 2–3 weeks (medium). Schema migration, indexed queries.

**Success Metrics**:
- >99% audit coverage (no lost writes)
- doctor --audit-summary completes in <500ms
- Export completes in <1s for 10k audits

---

### P2: Nice-to-Have, Ship in v1.3 or Later

#### 6. Rule Compaction & Auto-Deprecation
**Why**: user_rule rows accumulate over time. Need to compact stale rules and prevent context bloat.

**What**:
- Deprecation markers: deprecated_at, superseded_by fields on user_rule
- Signal collection: track which rules are accessed in context bundles, how often
- Auto-deprecation policy: rules unused for >90 days marked deprecated
- Compaction: doctor --compact auto-deprecates + suggests pruning
- Browse-tier ranking: deprioritize deprecated rules

**Business Value**: Keeps context lean over months/years of use. Prevents signal decay.

**Effort**: 2–3 weeks (medium). Requires signal collection, compaction rules.

**Success Metrics**:
- >80% of superseded rules auto-detected and deprecated
- Context bundle size stays <40 KB over 1 year of active use
- <100ms overhead for deprecation queries

---

#### 7. Memory Attribution & Provenance
**Why**: Agents save from different sources (error logs, manual input, synthesis). Need to track origin and confidence.

**What**:
- New source field: source IN (manual, log, synthesis, external, human_correction)
- Confidence field: 1–5 (optional)
- Search filters: mem_search(source=log, confidence>=4) for high-signal memories
- doctor --provenance-stats: breakdown by source, confidence distribution
- Browse-tier tagging: indicate source/confidence in response

**Business Value**: Improves trust (filter out low-confidence AI synthesis). Enables better signal filtering. Differentiator vs engram (which doesn't surface provenance).

**Effort**: 1–2 weeks (low). Schema update, filter logic, tag display.

**Success Metrics**:
- >95% of saved memories have source metadata
- doctor --provenance-stats completes in <100ms
- Search filtering adds <20ms latency

---

#### 8. Semantic Search via Optional Embeddings
**Why**: BM25 is great for keyword recall; embeddings enable semantic similarity search.

**What**:
- Optional embedding model integration (local: all-minilm, or cloud: OpenAI)
- mem_search(query, search_type=keyword|semantic|hybrid) 
- workspace.yml: embedding_provider (none, local, openai)
- Configurable: local models bundled optionally; cloud models require API key
- Hybrid search: combine BM25 + semantic, re-rank by both

**Business Value**: Better search quality for semantic queries ("show me lessons about performance optimization"). Differentiator vs droids-mem v1 (keyword-only).

**Effort**: 4–6 weeks (high). Model packaging, inference cost, optional deps, latency tuning.

**Success Metrics**:
- Semantic search latency: <500ms p95 (local), <1s p95 (cloud)
- >90% relevance improvement on semantic queries (user testing)
- Optional deps don't increase binary size >30 MB

---

### P3: Strategic, Post-v1.3

#### 9. Knowledge Graph Extraction (Advanced)
**Why**: Extract entities, relations, decision trees from memory. Enable higher-order reasoning.

**What**:
- LLM-powered extraction: entities (function, bug, API), relations (caused-by, fixed-by, depends-on, improves)
- Graph storage: new tables (entities, relations) or optional graph DB (DuckDB + Cypher-lite)
- Query: mem_graph_query("what patterns fix this bug?") → traversal
- Visualization: optional HTML graph export

**Business Value**: Advanced knowledge management. Enterprise feature. Enables "explain this bug using the graph of all past fixes".

**Effort**: Very high (8–10 weeks). LLM integration, schema design, graph query language.

**Success Metrics**:
- >80% of memories can be parsed into entity+relation tuples
- Graph queries complete in <1s for 10k memories
- Zero hallucinated relations (human verification on sample)

---

#### 10. Managed SaaS Edition (Commercial)
**Why**: Teams prefer managed services (no self-host overhead). Revenue opportunity.

**What**:
- Multi-tenant schema (user_id partition, workspace-scoped data)
- Hosted on vercel.com or similar: HA, backups, monitoring
- Managed auth: OAuth2, SAML for teams
- Sync service: auto-sync JSONL across team members
- Telemetry: anonymized usage stats (opt-in)
- Freemium model: free tier (1 workspace, 100k memories), paid (multi-workspace, priority support)

**Business Value**: Revenue stream. Easier team onboarding. Competition with mem0, engram ecosystem.

**Effort**: High (6–8 weeks). New business model, deployment ops, auth, multi-tenancy testing.

**Success Metrics**:
- <100ms p95 latency from global CDN
- >99.9% uptime SLA
- <1% churn on paid tier (if launched)

---

## Summary

### Current State
- **v1.0 GA shipped 2026-06-09** with comprehensive PII scrub, two-layer dedupe, two-tier context bundle, and MCP bridge
- **feat/p3-UX in-flight**: context modes, expand signal, prune-by-id, Memory inspector TUI (70% done, ready for merge)
- **15 ADRs** document all major decisions; fully designed but not all implemented

### Strengths
1. **Security & transparency**: Auditable PII scrub (18 patterns, version-pinned)
2. **Engineering rigor**: Two-layer dedupe, two-tier context, parent-as-broker pattern
3. **Open-source**: MIT licensed, community-auditable
4. **Principled design**: No automatic retention, frozen schema, clear subagent contract

### Top 3 Blockers for Adoption
1. **No workspace isolation** (needed for multi-project teams) — v1.1
2. **No git-sync** (needed for team collaboration) — v1.2
3. **No scrub tuning** (needed for enterprises with custom secrets) — v1.1

### vs Engram
- **droids-mem wins on**: Security, determinism, open-source, auditability
- **engram wins on**: Passive capture (simpler), workspace isolation (shipped), observations (granular)
- **Overlap strategy**: droids-mem is for control-focused, compliance-heavy teams; engram is for convenience-focused Claude users

### Highest-ROI Next Features (in priority order)
1. **Workspace isolation** (P0, v1.1) — gates team adoption
2. **Scrub policy tuning** (P0, v1.1) — reduces false positives
3. **Context modes** (P1, v1.1) — in-flight, near-ready
4. **Git-JSONL sync** (P1, v1.2) — biggest team collaboration feature
5. **Audit log** (P1, v1.2) — required for enterprise

---

## Files for Reference
- Architecture: /Users/samuel/droids-mem/CLAUDE.md
- Full PRD: /Users/samuel/droids-mem/files/Droids-mem-PRD.md
- ADRs (0001–0015): /Users/samuel/droids-mem/docs/adr/
- Project breakdown: /Users/samuel/droids-mem/docs/project-breakdown.md
- CHANGELOG: /Users/samuel/droids-mem/CHANGELOG.md
