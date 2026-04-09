# PicoClaw LiveKit Voice Agent — Production Hardening Design & Implementation Plan

## Goal

Transform the PicoClaw LiveKit Voice Agent from staging-ready to **production-hardened** by fixing critical security vulnerabilities, adding resilience patterns, improving observability, and cleaning up architectural debt — **without breaking existing functionality**.

---

## Architecture Impact Assessment

| Dimension | Impact | Notes |
|-----------|--------|-------|
| **Security** | 🔴 HIGH | Remove hardcoded credentials, add validation |
| **Resilience** | 🟡 MEDIUM | Circuit breakers, graceful degradation |
| **Observability** | 🟡 MEDIUM | Metrics, structured logging enhancements |
| **Performance** | 🟢 LOW | STT provider caching, minor optimizations |
| **Maintainability** | 🟡 MEDIUM | Extract AudioSessionManager, embed templates |
| **Breaking Changes** | 🟢 NONE | All changes are backward compatible |

---

## Design Decisions

### Decision 1: Credential Management Strategy

**Problem**: Hardcoded Supabase URL in `main.go`, no fail-fast on missing config

**Decision**: **Fail-fast validation at startup** — no hardcoded fallbacks

**Rationale**: 
- Security-first approach: better to crash than expose credentials
- Cerebrium deployment already uses env vars — no change needed there
- Local development requires `.env` file (already supported via `godotenv.Load()`)

**Trade-off**: Developers must configure `.env` before running locally (minor friction, major security win)

---

### Decision 2: STT Provider Caching

**Problem**: Database query on every session adds 50-200ms latency

**Decision**: **In-memory cache with 60s TTL** — simple, no external dependencies

**Rationale**:
- Adding Redis introduces infrastructure complexity
- STT provider config changes rarely (once per day/week)
- 60s TTL balances freshness vs performance
- Multi-instance consistency: new sessions on other instances pick up changes within 60s

**Trade-off**: Up to 60s delay in provider switching across instances (acceptable for operational changes, not for failover)

---

### Decision 3: Circuit Breaker Implementation

**Problem**: No protection against cascading API failures

**Decision**: **Lightweight custom circuit breaker** (no external dependency)

**Rationale**:
- `sony/gobreaker` adds dependency for simple state machine
- Custom implementation: ~50 lines, tailored to STT/TTS use case
- Three states: Closed → Open → Half-Open
- Configurable thresholds via environment variables

**Trade-off**: Less battle-tested than library, but trivial logic with unit tests

---

### Decision 4: AudioSessionManager Extraction

**Problem**: `RoomSession` violates Single Responsibility Principle

**Decision**: **Extract to new `pkg/livekit/audio_session.go`**

**Rationale**:
- `RoomSession` should manage room lifecycle only
- Audio setup (STT, VAD, PCM tracks) is separate concern
- Improves testability and future extensibility

**Trade-off**: Refactoring effort (~200 lines moved), but no behavioral changes

---

### Decision 5: Template Embedding

**Problem**: Hardcoded relative path `prompts/cheeko.tmpl` fails in Docker

**Decision**: **Go `embed` package** — bundle templates in binary

**Rationale**:
- Zero deployment complexity (no file mounting)
- Immutable templates at build time
- Dockerfile simplifies (no `COPY prompts/` line)

**Trade-off**: Requires rebuild to change templates (acceptable for production; dev can use flag)

---

## Implementation Plan

### Phase 1: Critical Security Fixes (Do FIRST)

- [ ] **Task 1.1**: Remove hardcoded Supabase credentials from `main.go:85-87` → Verify: Grep for `postgresql://` returns zero matches in source
- [ ] **Task 1.2**: Add startup config validation function `validateLiveKitConfig()` in `cmd/picoclaw-livekit/main.go` → Verify: Running without env vars prints clear error and exits with code 1
- [ ] **Task 1.3**: Add `defer sttFactory.Close()` in main function → Verify: No connection leak under load (check `pg_stat_activity` during test)
- [ ] **Task 1.4**: Add workspace deletion verification with retry logic in `agent_bridge.go:Close()` → Verify: Failed deletion logs error and queues cleanup file

**Dependencies**: None — these are independent fixes  
**Critical Path**: Tasks 1.1-1.4 can be done in parallel by different developers

---

### Phase 2: Resilience Patterns

- [ ] **Task 2.1**: Implement lightweight circuit breaker struct in `pkg/livekit/circuit_breaker.go` → Verify: Unit tests cover Closed→Open→Half-Open→Closed transitions
- [ ] **Task 2.2**: Wrap STT `SendAudio` calls with circuit breaker in `room_session.go` → Verify: After 10 consecutive failures, circuit opens and logs warning
- [ ] **Task 2.3**: Add TTS fallback phrase on synthesis failure in `audio_pipeline.go:synthesizeAndPlay()` → Verify: Failed TTS plays "Sorry, I had trouble..." instead of silence
- [ ] **Task 2.4**: Add maximum session duration (default 2 hours) to `RoomSessionConfig` → Verify: Session auto-disconnects after timeout with farewell message

**Dependencies**: Task 2.1 must complete before 2.2  
**Critical Path**: 2.1 → 2.2, then 2.3-2.4 in parallel

---

### Phase 3: Performance Optimization

- [ ] **Task 3.1**: Add in-memory cache to `stt.Factory` with 60s TTL → Verify: Second call to `GetActiveProvider()` returns cached result (check logs for "using cached provider")
- [ ] **Task 3.2**: Embed prompt templates using `//go:embed` in `main.go` → Verify: Binary runs without `prompts/` directory present
- [ ] **Task 3.3**: Add provider capability discovery caching in `Factory.GetProviderCapabilities()` → Verify: No duplicate DB queries for same provider within TTL window

**Dependencies**: None — independent optimizations  
**Critical Path**: All tasks parallelizable

---

### Phase 4: Architectural Cleanup

- [ ] **Task 4.1**: Extract `AudioSessionManager` from `RoomSession.handleTrackSubscribed()` → Verify: New file `pkg/livekit/audio_session.go` with clear interface, `RoomSession` delegates to it
- [ ] **Task 4.2**: Add structured request ID to all log entries per session → Verify: Every log line in session includes `session_id` field
- [ ] **Task 4.3**: Add graceful shutdown handler for worker that closes STT factory and drains active sessions → Verify: SIGTERM causes all sessions to finish current utterance before disconnecting

**Dependencies**: Task 4.1 depends on Phase 2 completion (circuit breaker integration)  
**Critical Path**: 4.1 after Phase 2, then 4.2-4.3 in parallel

---

### Phase 5: Observability (Post-Deployment Prep)

- [ ] **Task 5.1**: Add Prometheus metrics endpoint (`/metrics`) to health server → Verify: `curl localhost:8192/metrics` returns `picoclaw_sessions_active`, `picoclaw_stt_errors_total`, etc.
- [ ] **Task 5.2**: Add session lifecycle metrics (start, end, duration, error) → Verify: Metrics increment correctly during test session
- [ ] **Task 5.3**: Add STT/TTS provider latency histograms → Verify: Histogram buckets show p50, p95, p99 latencies in Grafana

**Dependencies**: None — additive changes  
**Critical Path**: All tasks parallelizable

---

## File Change Summary

| File | Changes | Risk |
|------|---------|------|
| `cmd/picoclaw-livekit/main.go` | Remove credentials, add validation, embed templates, close factory | 🔴 HIGH |
| `pkg/livekit/worker.go` | Add graceful shutdown handler | 🟡 MEDIUM |
| `pkg/livekit/room_session.go` | Add circuit breaker, extract audio session, session timeout | 🟡 MEDIUM |
| `pkg/livekit/audio_session.go` | **NEW** — extracted audio setup logic | 🟢 LOW |
| `pkg/livekit/agent_bridge.go` | Add workspace deletion verification | 🟢 LOW |
| `pkg/livekit/audio_pipeline.go` | Add TTS fallback phrase | 🟢 LOW |
| `pkg/livekit/circuit_breaker.go` | **NEW** — lightweight circuit breaker | 🟢 LOW |
| `pkg/voice/stt/factory.go` | Add provider caching with TTL | 🟡 MEDIUM |
| `pkg/config/config.go` | Add `MaxSessionDuration` field | 🟢 LOW |

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Config validation breaks existing deployments | Provide migration guide with required env vars |
| Circuit breaker false positives | Start with high threshold (50 failures), monitor and tune |
| Template embed breaks dev workflow | Keep file-based fallback with `--dev-templates` flag |
| STT cache delays provider switch | Document 60s TTL in ops runbook; manual cache invalidation endpoint if needed |

---

## Testing Strategy

### Unit Tests (Must Pass)
- Circuit breaker state transitions
- Config validation with missing/invalid fields
- STT factory cache TTL behavior
- Workspace deletion retry logic

### Integration Tests (Should Pass)
- Mock LiveKit room join with audio session setup
- STT provider switch under load (verify cache invalidation)
- Session timeout triggers graceful disconnect

### Manual Testing (Before Deploy)
1. Start worker without env vars → verify clear error message
2. Join room, speak 10 sentences → verify metrics increment
3. Kill STT API (block port) → verify circuit breaker opens after 10 failures
4. Wait 2 hours → verify session disconnects with farewell message
5. Delete `prompts/` directory → verify binary still runs (embedded templates)

---

## Rollout Plan

### Stage 1: Local Development (Day 1)
- Implement Phase 1 (security fixes)
- Run existing test suite → all must pass
- Manual smoke test with local LiveKit

### Stage 2: Staging Deployment (Day 2-3)
- Implement Phase 2-3 (resilience + performance)
- Deploy to staging Cerebrium instance
- Run load test: 10 concurrent sessions → verify no errors

### Stage 3: Production Deployment (Day 4-5)
- Implement Phase 4-5 (cleanup + observability)
- Blue-green deploy to production
- Monitor for 24 hours → verify metrics, error rates

---

## Done When

- [ ] All hardcoded credentials removed and config validation passes
- [ ] Circuit breaker protects STT/TTS calls with graceful degradation
- [ ] STT provider caching reduces session startup latency by >50ms
- [ ] AudioSessionManager extracted with no behavioral changes
- [ ] Prompt templates embedded in binary (Docker works without `prompts/`)
- [ ] Prometheus metrics endpoint returns session/STT/TTS metrics
- [ ] All existing tests pass + new unit tests added for circuit breaker/cache
- [ ] Manual smoke test passes (5 scenarios listed above)
- [ ] Deployment documentation updated with new env vars

---

## Notes

1. **No Breaking Changes**: All additions are backward compatible. Existing deployments with `config.json` continue to work.

2. **Environment Variable Additions**:
   - `PICOCLAW_LIVEKIT_MAX_SESSION_DURATION` (optional, default 2h)
   - `PICOCLAW_STT_CIRCUIT_BREAKER_THRESHOLD` (optional, default 10)
   - `PICOCLAW_STT_CACHE_TTL` (optional, default 60s)

3. **Database Schema**: No changes needed — caching is in-memory only.

4. **Go Version**: Requires Go 1.25+ (already in `go.mod`) for `embed` support.

5. **Dependencies Added**: None — circuit breaker is custom, caching uses `sync` package.

---

**Estimated effort**: 2-3 developer-days (not weeks — scope is focused and well-defined)  
**Risk level**: MEDIUM (security fixes are critical, rest are additive)  
**Recommendation**: Proceed with Phase 1 immediately, then iterate through remaining phases.
