# Block Server Deep Audit — Kickoff Prompt

## Mission
Fix 23 pre-launch defects across 19 Go files in `block-server/go/`. Prioritized by blast radius. No feature work. No refactors beyond what each fix requires. Ship clean, verify with `go vet ./...`.

## Cognitive Framework
```
LOCATE → READ → FIX → VERIFY (per atom)
```
- Each fix is an **atom**. Complete it fully before starting the next.
- After every 3-4 atoms, run `go vet ./...` as a checkpoint.
- If a fix touches more than 2 files, pause and reassess scope.

## AI Toolkit
Use `@[/ai-toolkit]` for code navigation. Batch calls with `-Ops` to save engine tax.

## Style Guide
- Comments explain WHY or CONSTRAINTS, never WHAT. Voice: terse, concrete, first-person-plural.
- Errors: use sentinel errors from `go/errors/errors.go` (pattern: `runtime.NewError("msg", code)`).
- Indentation: tabs only. Line endings: match the file's existing convention.

---

## Context Targets (read these files first to prime your window)

| Priority | File | Lines of Interest | Why |
|----------|------|-------------------|-----|
| 🔴 1st | `go/items/daily_drops.go` | 85-99, 104, 173-174, 265 | 4 defects in one file — error swallows, dead code, lying log |
| 🔴 2nd | `go/items/storage_operations.go` | 84-87 | Silent error return poisons downstream verification |
| 🔴 3rd | `go/items/inventory.go` | 124, 128, 139, 164, 167 | Raw `runtime.NewError` calls need sentinel conversion |
| 🟠 4th | `go/notify/notify.go` | 132-167 | `SendReward` hand-copies struct fields into map — maintenance trap |
| 🟠 5th | `go/items/player_rpc.go` | 365, 376, 384, 398, 412, 473, 484, 492 | More raw `runtime.NewError` — batch with inventory.go |
| 🟠 6th | `go/items/rewards.go` | 149-162, 451-467 | String literal mismatch ("pet" vs storageKeyPet) + non-atomic commit |
| 🔵 7th | `go/main.go` | 15 | Dead constant |
| 🔵 8th | `go/items/types.go` | 91-98 | Dead struct |

---

## Execution Plan — Atomic Steps

### Atom 1: `daily_drops.go` — The Scariest File (D1, D2, D5, D8, D14, D15)

**D1 (🔴 MUST-FIX): Error swallowed after `grantCappedDrops`, claim timestamp still written.**
- Line 96-99: If `grantCappedDrops` returns error, the function MUST return early. Do NOT continue to line 104.
- Fix: Add `return err` after the error log on line 99.

**D5 (🔴 MUST-FIX): `json.Unmarshal` error silently ignored in `incrementDailyMatchCount`.**
- Line 265: `json.Unmarshal([]byte(objects[0].Value), &data)` — error not checked.
- Fix: Check err, log + return on failure.

**D14 (🟣 DISHONESTY): Error log message lies about cause.**
- Line 98: The message says "wallet JSON is valid but missing required key" but fires on `grantCappedDrops` failure (which could be anything — account fetch, wallet update, etc.).
- Fix: Change message to `"failed to grant daily drops: %v"`.

**D8 (🟠 RED FLAG): Dead `resp` struct built but never returned.**
- Lines 59-62 and 93, 102: `TryClaimDailyDrops` builds a `resp` struct with `DropsLeft` but returns `error` only.
- Fix: Delete the dead `resp` struct and all assignments to it.

**D15 (🟣 DISHONESTY): Duplicate consecutive comments.**
- Lines 173-174: `// get the current number of drops from the wallet` followed by `// get drops total before`. Delete one.

**D2 (🔴 MUST-FIX): Missing wallet key treated as non-fatal.**
- Lines 85-88: If `walletKeyDropsLeft` is missing, log but continue with `drops = 0`. This is actually correct behavior for new users. Downgrade log from `Error` to `Warn` — new users legitimately won't have this key yet.

### Atom 2: `storage_operations.go` — Silent Wipe Risk (D4)

**D4 (🔴 MUST-FIX): `GetUserProgression` returns empty on StorageRead failure.**
- Line 84-87: Returns `progression, nil` when `StorageRead` fails.
- Fix: Return `nil, fmt.Errorf("progression read failed: %w", err)` and log the error.
- ⚠️ SECOND-ORDER: Callers of `GetUserProgression` (specifically `VerifyAndFixUserProgression` in `progression.go:264`) already handle the error case. This fix is safe.

### Atom 3: `inventory.go` + `player_rpc.go` — Sentinel Error Batch (D3)

**D3 (🔴 MUST-FIX): Raw `runtime.NewError` calls throughout.**

First, add missing sentinels to `go/errors/errors.go`:
```go
ErrItemNotFound       = runtime.NewError("item not found", 3)
ErrNoAbilitiesAvail   = runtime.NewError("no abilities available", 3)
ErrAbilityNotUnlocked = runtime.NewError("ability not unlocked", 3)
ErrAbilityNotFound    = runtime.NewError("ability not found", 3)
ErrPetNotOwned        = runtime.NewError("pet not owned", 7)      // 7 = PERMISSION_DENIED
ErrClassNotOwned      = runtime.NewError("class not owned", 7)
ErrTransactionFailed  = runtime.NewError("transaction failed", 13) // 13 = INTERNAL
ErrOwnershipCheck     = runtime.NewError("ownership check failed", 13)
ErrPrepareFailed      = runtime.NewError("prepare failed", 13)
```

Then sweep `inventory.go` and `player_rpc.go` replacing each bare `runtime.NewError(...)` with the matching sentinel. 

⚠️ CAUTION: Some `runtime.NewError` calls use error code `403` (line 384, 492). HTTP 403 is not a valid gRPC code. These should be `7` (PERMISSION_DENIED). This is both a sentinel fix AND a correctness fix.

### Atom 4: `notify.go` — Maintenance Trap (D6)

**D6 (🟠 RED FLAG): `SendReward` manually reconstructs struct as map.**
- Lines 132-167: Hand-copies each field from `RewardPayload` into `map[string]interface{}`.
- Fix: Use `json.Marshal` → `json.Unmarshal` into `map[string]interface{}` to auto-include all tagged fields.
```go
payloadBytes, err := json.Marshal(payload)
if err != nil {
    return fmt.Errorf("reward marshal: %w", err)
}
var content map[string]interface{}
if err := json.Unmarshal(payloadBytes, &content); err != nil {
    return fmt.Errorf("reward unmarshal: %w", err)
}
return nk.NotificationSend(ctx, userID, "Reward!", content, CodeReward, "", true)
```
- ⚠️ TEST: Verify notification still deserializes correctly on client.

### Atom 5: `rewards.go` — String Mismatch + Dead Export (D23, D10)

**D23 (⚪ DORMANT): `"pet"` / `"class"` literals vs `storageKeyPet` / `storageKeyClass` constants.**
- Lines 150, 156, 171, 173, 472, 482: Use bare strings `"pet"`, `"class"`.
- These are INTENTIONALLY different from `storageKeyPet = "pets"`. They represent the *category* not the *storage key*. The real fix: define category constants.
```go
const (
    CategoryPet   = "pet"
    CategoryClass = "class"
)
```
- Then replace all bare `"pet"` / `"class"` in `rewards.go` and `GetRewardItemIDs`.

**D10 (🟠 RED FLAG): `GrantLevelRewards` commits independently.**
- Line 451-467: This function calls `CommitPendingWrites` internally. If a caller is building atomic writes, this breaks the guarantee.
- Fix: Check if anything actually calls `GrantLevelRewards`. If not → delete it. If yes → refactor callers to use `PrepareLevelRewards` + merge instead.

### Atom 6: Dead Code Cleanup (D9, D17, D18, D19)

**D9/D17:** Delete `rpcIdRewards` constant from `main.go:15`.
**D18:** Search for callers of `addToInventory` in `inventory.go:470`. If zero callers → delete.
**D19:** Search for usages of `LevelReward` struct in `types.go:91-98`. If zero → delete.

### Atom 7: Low-Priority Hardening (D7, D11, D12, D20, D21, D22)

These are post-launch quality items. Document them in `postlaunch.md` and move on:
- **D7**: Permissions inconsistency (drops `PermissionRead:1` vs others `PermissionRead:2`)
- **D11**: Triple-validation in BatchInitializeProgression
- **D12**: LogWithUser over-engineering
- **D20**: Mixed line endings
- **D21**: StorageList 100-item hard cap
- **D22**: StorageRead return-order assumption

---

## Verification Protocol
After ALL atoms complete:
```bash
cd go && go vet ./...
```
Must exit clean. If it doesn't, fix before declaring done.

## Kill Criteria
- Do NOT refactor function signatures unless required by a fix.
- Do NOT add new features.
- Do NOT change storage collection/key names.
- Do NOT touch `match_result.go` — it was already audited and fixed in a prior session.
- If any single fix balloons past 3 files, STOP and reassess.
