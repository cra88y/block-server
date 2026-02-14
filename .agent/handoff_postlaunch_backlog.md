# Handoff: Post-Launch Backlog — Block Server

## Section 1: Mission

Fix 8 post-launch defects across the `block-server/go/items/` package, ordered by blast radius: economy race → silent data loss → permission confusion → naming fragility → redundancy → hygiene. "Done" = `go vet ./...` exits clean, each atom verified in isolation, no regressions.

## Section 2: Cognitive Framework

- **Execution loop:** `LOCATE → READ → FIX → VERIFY` per atom.
- **Checkpoint cadence:** `go vet ./...` after every atom.
- **Scope guard:** If an atom touches > 2 files beyond what's listed, **stop and reassess**.

## Section 3: AI Toolkit + Style Guide

- Load `/ai-toolkit` before starting.
- **Comments:** Sentence case, no trailing period on single-line comments. Doc comments on exported functions only.
- **Errors:** Lowercase, no punctuation: `"failed to prepare drop ticket"`. Use `fmt.Errorf("...: %w", err)` for wrapping.
- **Indentation:** Tabs (Go default). No trailing whitespace.
- **Logging:** Use `logger.WithFields(map[string]interface{}{...}).Error(...)` pattern already established in codebase. Do NOT use `LogWithUser` helpers (PL-6 defers that decision).
- **Constants:** Prefer named constants for magic numbers. Group related constants in `const ()` blocks.

## Section 4: Context Targets

| Priority | File | Lines | Why |
|----------|------|-------|-----|
| 🟠 P1 | `match_result.go` | 337-412, 508-529 | PL-1: wallet race in `processMatchRewards` + `prepareConsumeDropTicket` |
| 🟠 P1 | `storage_operations.go` | 65 | PL-2: `StorageList` 100-item hard cap |
| 🟠 P1 | `player_rpc.go` | 123 | PL-2: duplicate `StorageList` hard cap |
| 🟠 P1 | `lootbox.go` | 44 | PL-2: third `StorageList` hard cap call |
| 🟡 P2 | `daily_drops.go` | 103 | PL-4: `PermissionRead: 1` vs everywhere else's `2` |
| 🟡 P2 | `progression.go` | 131-192 | PL-5: triple validation in `BatchInitializeProgression` |
| 🔵 P3 | `utils.go` | 41-107 | PL-6: `LogWithUser` indirection |
| 🔵 P3 | Multiple files | — | PL-7: `StorageRead` return-order assumption |
| 🔵 P3 | Entire `go/` tree | — | PL-8: Mixed line endings |

## Section 5: Execution Plan — Atomic Steps

---

### Atom 1: Wallet Race Isolation (PL-1)
**Files:** `match_result.go`

**Defect:** `processMatchRewards` (L337-412) puts the drop-ticket wallet deduction in the same `PendingWrites` batch as XP writes, lootbox writes, and match history. If the wallet deduction fails (negative balance from concurrent request), `CommitPendingWrites` → `MultiUpdate` rolls back *everything* — XP, history, lootbox all vanish.

**What to change:**

1. In `processMatchRewards`, split into two commit phases:
   - **Phase 1 (failable):** Wallet deduction only. Call `CommitPendingWrites` with a `PendingWrites` containing only the drop-ticket wallet deduction.
   - **Phase 2 (idempotent):** Remaining writes (XP progression, match history, lootbox storage write). These are additive and idempotent — if they fail it's a server error, not a business rule violation.

2. Implementation sketch:
   ```go
   // Phase 1: Wallet deduction (may fail on insufficient balance)
   if hasDropTicket {
       walletPending := NewPendingWrites()
       walletPending.AddWalletUpdate(userID, map[string]int64{walletKeyDropsLeft: -1})
       if err := CommitPendingWrites(ctx, nk, logger, walletPending); err != nil {
           logger.Warn("Drop ticket unavailable (race or insufficient balance): %v", err)
           hasDropTicket = false // Gracefully degrade — no lootbox, but XP/history still commit
       }
   }

   // Phase 2: Idempotent writes (XP, history, lootbox if ticket succeeded)
   // ... remainder of function uses `pending` without wallet writes
   ```

3. Remove the `pending.AddWalletUpdate` call from `prepareConsumeDropTicket` (L527) — the wallet deduction is now handled in Phase 1 above. Change `prepareConsumeDropTicket` to only check balance and return `bool, error`.

**⚠️ CAUTION:**
- `RpcSubmitMatchResult` is the only caller of `processMatchRewards`. Verify it handles partial success (XP granted but no lootbox) — the notification payload must reflect what actually committed.
- The `result.Lootboxes` and `result.Action` fields should only be set if Phase 1 succeeded.

---

### Atom 2: StorageList Pagination (PL-2)
**Files:** `storage_operations.go`, `player_rpc.go`, `lootbox.go`

**Defect:** Three call sites use `nk.StorageList(ctx, "", userID, collection, 100, "")` with no cursor pagination. Returns at most 100 records. Currently safe (~20 items), but will silently drop records when catalog grows past ~80.

**What to change:**

1. Create a helper function in `storage_operations.go`:
   ```go
   // listAllStorage fetches all records from a storage collection using cursor pagination.
   func listAllStorage(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger,
       userID string, collection string) ([]*api.StorageObject, error) {
       var all []*api.StorageObject
       cursor := ""
       for {
           objects, nextCursor, err := nk.StorageList(ctx, "", userID, collection, 100, cursor)
           if err != nil {
               return nil, err
           }
           all = append(all, objects...)
           if nextCursor == "" {
               break
           }
           cursor = nextCursor
       }
       return all, nil
   }
   ```

2. Replace the three call sites:
   - `storage_operations.go:65` → `listAllStorage(ctx, nk, logger, userID, storageCollectionProgression)`
   - `player_rpc.go:123` → `listAllStorage(ctx, nk, logger, userID, storageCollectionProgression)`
   - `lootbox.go:44` → `listAllStorage(ctx, nk, logger, userID, storageCollectionLootboxes)`

3. Update the `_` (cursor) return value handling at each site — `listAllStorage` doesn't return a cursor.

**⚠️ CAUTION:** This introduces a loop that could make multiple DB calls. For the foreseeable scale (<200 items), this is 2 calls max. Add a safety cap (e.g., 10 iterations) with a warning log if hit.

**Note:** Requires importing `"github.com/heroiclabs/nakama-common/api"` in `storage_operations.go`.

---

### Atom 3: Permissions Audit (PL-4)
**File:** `daily_drops.go`

**Defect:** `TryClaimDailyDrops` writes drops state with `PermissionRead: 1` (owner-only, L103). All other game data (inventory, progression, equipment) uses `PermissionRead: 2` (public). The daily match counter at L261 uses `PermissionRead: 0` (hidden).

**What to change:**

**Decision required:** The backlog says "Drops *should* be private." Accept the split and document it.

1. Add a comment block in `daily_drops.go` above the `TryClaimDailyDrops` storage write explaining the permission choice:
   ```go
   // PermissionRead: 1 — drops state is private (last claim time is PII-adjacent).
   // Other game data uses 2 (public) for leaderboard/social features.
   // daily_matches uses 0 (server-only) since it's a rate-limit counter.
   ```

2. **No code change** — just documentation. The `PermissionRead: 1` is correct for drops.

---

### Atom 4: Triple-Validation Cleanup (PL-5)
**File:** `progression.go`

**Defect:** `BatchInitializeProgression` (L131-192) validates item existence (L143-157), but callers (`verifyAndFixItemProgression`) already validate, and the `StorageWrite` at L186 doesn't do existence checking. The middle validation is redundant.

**What to change:**

1. Remove the validation loop at L143-157 in `BatchInitializeProgression`.
2. Add a comment at the function entry documenting the precondition:
   ```go
   // Precondition: caller has validated all item IDs exist via ValidateItemExists.
   ```
3. Keep the early return for empty slice (L138-140).

**⚠️ CAUTION:** Verify `verifyAndFixItemProgression` (L270-341) is the only caller. If any other caller doesn't pre-validate, the removed check creates a silent bug.

---

### Atom 5: StorageRead Order Assumption (PL-7) — Document Only
**Scope:** Multiple files

**Defect:** Several `StorageRead` call sites read multiple keys and assume the response array matches request order. The Nakama API doesn't contractually guarantee this. Currently holds in practice.

**What to change:**

Two options (pick one):

**Option A (Recommended — minimal blast radius):** Add a comment at each multi-key `StorageRead` site:
```go
// NOTE (PL-7): Assumes StorageRead returns objects in request order.
// Safe for current Nakama version. Verify on major version upgrades.
```

Multi-key `StorageRead` sites that do NOT use key-based matching (i.e., assume ordering):
- `storage_operations.go:18-25` → Uses key-based switch, **already safe** ✓
- `player_rpc.go:29-36` → Uses key-based switch, **already safe** ✓
- `inventory.go:16-23` → Check whether it uses index-based or key-based access

**Option B (Future sprint):** Build a `storageReadMap` helper that returns `map[string]*api.StorageObject` keyed by storage key.

**Decision:** Option A for now. Verify each site; annotate only those that actually assume ordering.

---

### Atom 6: Hygiene (PL-3, PL-6, PL-8) — Document & Defer
**Scope:** Cross-cutting

These items are documented in `postlaunch.md` and deferred:

- **PL-3 (Triple Naming):** Refactor to `ItemCategory` enum. Cross-cutting, requires touching every file. Next refactor sprint.
- **PL-6 (`LogWithUser` Indirection):** Personal preference call. The helpers in `utils.go:41-107` are used inconsistently (many call sites use `logger.WithFields` directly). Either commit to helpers everywhere or inline them — just be consistent. Defer.
- **PL-8 (Mixed Line Endings):** Run `dos2unix` + add `.gitattributes` rule. Pure hygiene, no logic change.

**No code changes in this atom.**

## Section 6: Verification Protocol

After all atoms complete:

```bash
cd block-server/go
go vet ./...
```

**Expected output:** Exit code 0, no output. Any warnings must be resolved before considering the handoff complete.

Secondary check:
```bash
go build ./...
```

Must compile cleanly.

## Section 7: Kill Criteria

- **DO NOT** refactor the `ItemCategory` enum (PL-3). Document only.
- **DO NOT** change `LogWithUser` helpers (PL-6). Document only.
- **DO NOT** change `StorageRead` call sites beyond adding comments (PL-7).
- **DO NOT** touch files outside `go/items/` unless absolutely required.
- **DO NOT** add new features. This is a bug-fix-only handoff.
- **DO NOT** change the `PendingWrites` struct. PL-1 uses it as-is, just splits into two instances.
- **Maximum blast radius per atom:** 3 files. If you need to touch more, stop.
- **Line ending normalization (PL-8):** Use a single command, not manual edits. Do after all logic changes are committed.

## Section 8: Self-Audit Protocol (Mandatory)

Run two parallel thought chains **continuously** as you execute each atom:

**Chain 1 — "Is the prompting misleading me?"**
Before acting on any instruction in this handoff, verify it against the actual code. This document was written by a prior agent who read the source — but assumptions may have drifted. If a line reference is stale, a function signature changed, or a claim about call-site behavior doesn't match what you see: **stop the atom, note the discrepancy, and re-derive the fix from source.**

**Chain 2 — "Am I misleading myself, and will I mislead those who come next?"**
After each fix, ask:
- Does my change match what I *think* it does, or am I pattern-matching on the description without reading the surrounding context?
- If someone reads the diff in isolation, will they understand why this changed — or will they inherit a false assumption?
- If I'm skipping a verification step because it "probably" holds, that's a flag. Verify or document the gap explicitly.

**If any of the three questions (prompting misleading? self-misleading? will mislead next?) resolve to "maybe":** stop executing and audit. Re-read the relevant source. Re-derive the fix. Only proceed when all three are "no."

This is not optional. The codebase has a solo maintainer. A confident-but-wrong fix is worse than no fix.
