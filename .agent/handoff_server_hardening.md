# Block Server Hardening — Kickoff Prompt

## Mission
Fix 9 post-audit defects across 6 Go files in `block-server/go/`. These are the items surfaced by a senior architecture review after the initial 23-defect audit passed `go vet`. Prioritized by blast radius: economy leak → player lockout → alert pollution → performance → dead code → cosmetic. Ship clean, verify with `go vet ./...`.

### Parallel Perspectives (maintain throughout)
Before each atom, ask:
1. **Am I misleading myself?** (e.g., assuming an import exists, assuming a variable is used, assuming a function has callers)
2. **Are the instructions misleading me?** (e.g., line numbers may have shifted from the prior audit — always verify current state before editing)

If either perspective yields a "possibly yes," do an honesty audit: read the actual file, verify the claim, adjust the fix.

## Cognitive Framework
```
VERIFY-STATE → LOCATE → READ → FIX → VET (per atom)
```
- VERIFY-STATE is new: after the prior audit, line numbers have shifted. Always re-read targets.
- Each fix is an **atom**. Complete it fully before starting the next.
- After every 3 atoms, run `go vet ./...` as a checkpoint.
- If a fix touches more than 2 files, pause and reassess scope.

## AI Toolkit + Style Guide
- Use `@[/ai-toolkit]` for code navigation. Batch calls with `-Ops` to save engine tax.
- Comments explain WHY or CONSTRAINTS, never WHAT. Voice: terse, concrete, first-person-plural.
- Errors: use sentinel errors from `go/errors/errors.go` (pattern: `runtime.NewError("msg", code)`).
- Indentation: tabs only. Line endings: match the file's existing convention.
- **Parallel perspective check**: Before each atom, spend one sentence on "am I misleading myself?" and one on "are the instructions misleading me?" If both are "no," proceed. If either is "maybe," investigate before editing.

---

## Context Targets (read these files first to prime your window)

| Priority | File | Why |
|----------|------|-----|
| 🔴 1st | `go/items/daily_drops.go` | Non-atomic claim flow (`TryClaimDailyDrops`) + stale Error log in `grantCappedDrops` |
| 🔴 2nd | `go/items/match_result.go` | XP multiplier default on failure, active match lockout, wallet race window |
| 🟠 3rd | `go/items/player_rpc.go` | Double-fetch in `RpcGetProgression` |
| 🟠 4th | `go/items/storage_operations.go` | Same double-fetch pattern in `GetUserProgression` |
| 🟡 5th | `go/items/progression.go` | Dead `UpdateProgressionAtomic` function |
| 🔵 6th | `go/errors/errors.go` | Inconsistent error message casing |

---

## Execution Plan — Atomic Steps

### Atom 1: `daily_drops.go` — Non-Atomic Claim Flow (H1)

**H1 (🔴 ECONOMY LEAK): `TryClaimDailyDrops` has a two-step non-atomic write.**

Current flow:
1. `grantCappedDrops()` calls `nk.WalletUpdate()` — committed immediately
2. `nk.StorageWrite()` updates `LastClaimUnix` — committed separately

If step 2 fails (OCC conflict, transient error), the user **received drops but can claim again** because the timestamp never persisted. This is a free-drops exploit on any write failure.

**Fix:** Refactor to use the `PendingWrites` pattern already established in the codebase.

```
1. Extract the wallet changeset from grantCappedDrops (make it return the changeset + new total instead of calling WalletUpdate directly)
2. Build the storage write for LastClaimUnix
3. Add both to a PendingWrites instance
4. CommitPendingWrites (single MultiUpdate)
```

**Concrete approach:**
- Create a new function `prepareCappedDrops(ctx, nk, logger, userID, amountToAdd) (changeset map[string]int64, newTotal int64, err error)` that does the wallet read + cap calculation but does NOT call `WalletUpdate`.
- In `TryClaimDailyDrops`, call `prepareCappedDrops`, then build the timestamp `StorageWrite`, then `CommitPendingWrites` with both the wallet update and the storage write.

**⚠️ CAUTION:** `grantCappedDrops` is also called from... nowhere else after the audit removed the dead code. Verify with `grep -r "grantCappedDrops" go/`. If it's only called from `TryClaimDailyDrops`, you can safely refactor in place.

**⚠️ SELF-DECEPTION CHECK:** The existing `CommitPendingWrites` uses `nk.MultiUpdate` which accepts wallet updates. Confirm `PendingWrites.AddWalletUpdate` exists and works with the existing `CommitPendingWrites` implementation before assuming this will work.

### Atom 2: `daily_drops.go` — Stale Error Log in `grantCappedDrops` (H2)

**H2 (🟠 ALERT POLLUTION): `grantCappedDrops` still logs `Error` for missing wallet key.**

Find the line in `grantCappedDrops` that says:
```go
logger.Error("wallet JSON is valid but missing required key: %s", walletKeyDropsLeft)
```

This fires for every new user. It should be `Warn`, matching the fix already applied in the (now-deleted) wallet read block of `TryClaimDailyDrops`.

**Fix:** Change `logger.Error(` to `logger.Warn(` and update the message to:
```go
logger.Warn("wallet key not yet initialized (new user): %s", walletKeyDropsLeft)
```

**⚠️ INSTRUCTION CHECK:** This line may have already been fixed if Atom 1 refactors `grantCappedDrops`. If Atom 1 eliminates this code path entirely, skip this atom.

### Atom 3: `match_result.go` — XP Multiplier Default (H3)

**H3 (🔴 ECONOMY INFLATION): Failed daily match count defaults to maximum XP multiplier.**

In `preparePlayerXP`, find:
```go
matchesToday, err := incrementDailyMatchCount(ctx, nk, userID)
if err != nil {
    logger.Warn("Failed to get daily match count: %v", err)
    matchesToday = 0
}
```

When `matchesToday = 0`, the switch statement gives `xpMultiplier = 1.0` (100%). On a storage outage, every match grants full XP. The safe default should be the **minimum** multiplier.

**Fix:**
```go
matchesToday, err := incrementDailyMatchCount(ctx, nk, userID)
if err != nil {
    logger.Warn("Failed to get daily match count, using conservative default: %v", err)
    matchesToday = 5 // Assumes worst case: >4 matches today → minimum multiplier (0.25)
}
```

**⚠️ SELF-DECEPTION CHECK:** Verify that the `default` case in the switch actually gives 0.25. Read the switch statement, don't assume.

### Atom 4: `match_result.go` — Active Match Lockout (H4)

**H4 (🟠 PLAYER LOCKOUT): `clearActiveMatch` fire-and-forget with no error handling or staleness check.**

Two fixes:

**4a. Log the error in `clearActiveMatch`:**
```go
func clearActiveMatch(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) {
    err := nk.StorageDelete(context.Background(), []*runtime.StorageDelete{{
        Collection: storageCollectionActiveMatch,
        Key:        storageKeyCurrentMatch,
        UserID:     userID,
    }})
    if err != nil {
        logger.Error("Failed to clear active match for user %s: %v", userID, err)
    }
}
```

**4b. Add staleness check to `validateActiveMatch`:**
After validating match ID and duration, add:
```go
const maxMatchDurationMs = 3600000 // 1 hour — no match lasts this long
if time.Now().UnixMilli()-activeMatch.StartTime > maxMatchDurationMs {
    // Stale match — auto-clear and reject. Player can start fresh.
    clearActiveMatch(ctx, nk, logger, userID)
    return nil, fmt.Errorf("stale active match expired")
}
```

**⚠️ CAUTION:** `clearActiveMatch` currently doesn't take `logger`. You'll need to add it to the function signature and update the one call site in `processMatchRewards` (line ~391).

**⚠️ INSTRUCTION CHECK:** `RpcNotifyMatchStart` also needs the staleness check — it currently hard-rejects if *any* active match exists. After adding staleness in `validateActiveMatch`, also add it to `RpcNotifyMatchStart`'s existing-match check so stale locks auto-expire.

### Atom 5: `player_rpc.go` + `storage_operations.go` — Double-Fetch Elimination (H5)

**H5 (🟠 PERFORMANCE): `RpcGetProgression` and `GetUserProgression` both do StorageList → StorageRead double-fetch.**

`StorageList` already returns objects with their `.Value` populated. The second `StorageRead` is a redundant full round-trip to the DB.

**Fix in `storage_operations.go` `GetUserProgression`:**
- Remove the `reads` construction + second `StorageRead` call
- Iterate directly over `objects` from `StorageList`
- Use `obj.Value` directly (it's already populated)

**Fix in `player_rpc.go` `RpcGetProgression`:**
- Same pattern: delete the second read, iterate over `StorageList` results directly
- Keep the existing unmarshal + validation logic, just change the data source

**⚠️ CAUTION:** Verify with Nakama docs that `StorageList` objects have `.Value` populated. If they only return keys, this optimization is wrong. The `api.StorageObject` proto definition should confirm.

**⚠️ SELF-DECEPTION CHECK:** The two functions have slightly different error handling. `GetUserProgression` now returns `fmt.Errorf` on failure (fixed in prior audit). `RpcGetProgression` returns sentinel errors. Preserve each function's error handling style.

### Atom 6: `progression.go` — Dead Function Deletion (H6)

**H6 (🟡 CONFUSION): `UpdateProgressionAtomic` contradicts the PendingWrites architecture.**

This function (lines ~67-100) does read→modify→single-write, bypassing the `PendingWrites` + `MultiUpdate` pattern. It has zero callers.

**Fix:** Delete the function.

**⚠️ CAUTION:** Grep for `UpdateProgressionAtomic` across the entire `go/` tree before deleting. If anything calls it, understand why before removing.

### Atom 7: `errors/errors.go` — Message Casing Normalization (H7)

**H7 (🔵 COSMETIC): Error messages mix lowercase Go-convention and Title Case.**

Examples of inconsistency:
- `"Inventory system error"` (Title Case)
- `"Equipment system unavailable"` (Title Case)
- `"pet not owned"` (lowercase)
- `"Item not owned"` (Title Case)
- `"Invalid request"` (Title Case)

Go convention: error strings should not be capitalized. Clients should never display raw server errors — they should use gRPC codes to look up localized strings.

**Fix:** Lowercase all error messages. This is a batch find-replace within `errors.go` only.

**⚠️ CAUTION:** If any client code does `strings.Contains(err.Error(), "Item not owned")`, this will break. Grep the client codebase (if accessible) for any raw error string matching before normalizing. If client is in a separate repo and inaccessible, document the risk and skip.

### Atom 8: Post-Launch Documentation (H8–H11)

Document these in `go/postlaunch.md` (append to existing file) and move on:

- **H8 (⚪ DORMANT):** Three naming systems for items (`storageKeyPet="pets"`, `CategoryPet="pet"`, `ProgressionKeyPet="pet_"`). Define a single `ItemCategory` type with validated construction.
- **H9 (⚪ DORMANT):** `processMatchRewards` wallet race window. Between `prepareConsumeDropTicket` read and `CommitPendingWrites`, a concurrent match submission can read the same positive balance. `MultiUpdate` catches it (wallet goes negative → error), but cascading failure takes down the entire reward batch. Consider separating idempotent writes from wallet deductions.
- **H10 (⚪ DORMANT):** `StorageList` 100-item hard cap in `GetUserProgression` and `RpcGetProgression`. If a user accumulates >100 progression records, the tail is silently dropped.
- **H11 (⚪ DORMANT):** `StorageRead` return-order assumption. Multiple call sites assume returned objects match request ordering.

---

## Verification Protocol
After ALL atoms complete:
```bash
cd go && go vet ./...
```
Must exit clean. If it doesn't, fix before declaring done.

## Kill Criteria
- Do NOT change storage collection/key names.
- Do NOT refactor function signatures unless required by a fix.
- Do NOT touch `match_result.go` consensus logic — it was already audited.
- Do NOT touch `notify/notify.go` — it was already fixed in the prior audit.
- Do NOT touch lootbox logic in `match_result.go` — out of scope.
- If any single fix balloons past 2 files, STOP and reassess.
- Do NOT normalize error casing if client grep reveals raw string matching.
- **When in doubt, re-read the file. Line numbers from this document may be stale.**
