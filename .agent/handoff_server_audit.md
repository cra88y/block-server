# Server Audit Continuation — Handoff Prompt

Paste this into a fresh context window.

---

## Your Mission

You're continuing a pre-launch server audit on `block-server`, a Nakama game backend (Go). Two critical items were already fixed. You're picking up the remaining list.

## Cognitive Framework: Read → Verify → Fix → Verify

For each item below, follow this loop:

1. **READ** the relevant code using `Get-CodeItem` or `Get-FileOutline` from the ai-toolkit. Don't read whole files.
2. **VERIFY** your understanding — trace callers and callees with `Search-Code` to understand blast radius before touching anything.
3. **FIX** with the smallest correct change. Match the existing code style (short comments, no decorative banners, casual tone).
4. **VERIFY** the fix compiles: `cd go && go vet ./...`
5. **REALIGN** — after each fix, re-read the remaining list and ask: "Did this fix change the priority or applicability of any other item?" If yes, adjust order before continuing.

**Self-correction rule:** If you find yourself writing more than 20 lines of new code for any single item, stop. You're probably overengineering. These are all small targeted fixes.

## Remaining Items (priority order)

### 3. `math/rand` unseeded — 🟠 HIGH
- **File:** `go/items/lootbox.go`  
- **Issue:** Uses `math/rand` which may be unseeded depending on Go version. Go 1.20+ auto-seeds, but earlier versions default to seed(0) = deterministic = predictable lootbox contents.
- **Fix:** Either confirm Go version >= 1.20 in `go.mod` (then it's a no-op), or switch to `crypto/rand` for the shuffle/selection logic. Check `go.mod` first.
- **Toolkit:** `Get-CodeItem -Path go/items/lootbox.go -FunctionName <relevant_func>`, then check `go.mod`.

### 4. Silent error drop — 🟠 HIGH  
- **File:** `go/session/session_events.go` around line 68
- **Issue:** `TryClaimDailyDrops` error is silently swallowed. If daily drops fail, the player gets no drops and no indication why.
- **Fix:** Log the error at Warn level. One line.

### 5. Brittle key pluralization — 🟠 HIGH
- **File:** `go/items/shop.go` around line 262
- **Issue:** Storage collection keys are constructed by appending `+s` to item type strings (e.g., `"pet"` → `"pets"`). This breaks if any item type doesn't follow this pluralization pattern.
- **Fix:** Use an explicit map or switch instead of string concatenation. Check what item types actually flow through this path with `Search-Code`.
- **Toolkit:** `Search-Code -Term "+\"s\"" -Path go/items/shop.go` to find all instances.

### 6. Indentation — 🟡 MEDIUM
- **File:** `go/session/session_events.go` around line 65
- **Issue:** Non-gofmt indentation (spaces instead of tabs, or mixed).
- **Fix:** Run `gofmt -w go/session/session_events.go` or fix manually.

### 7. Raw fmt.Errorf — 🟡 MEDIUM
- **File:** `go/items/lootbox.go` around line 100
- **Issue:** Uses `fmt.Errorf("...")` with an inline string instead of a sentinel error from the `errors` package. Rest of codebase uses `errors.ErrXxx` pattern.
- **Fix:** Add a sentinel to the `errors` package and use it.
- **Toolkit:** `Get-FileOutline -Path go/errors/errors.go` to see existing sentinel pattern.

### 8. IAP stub — 🟢 LOW
- **File:** `go/items/shop.go` around line 341
- **Issue:** `validate_iap_receipt` RPC is registered but the handler is a stub. No abuse logging if someone calls it.
- **Fix:** Add a logger.Warn so you know if anyone's hitting it in production.

### 9. Lootbox ID collision — 🟢 LOW
- **File:** `go/items/match_result.go` around line 569
- **Issue:** Lootbox IDs use `userID[:8] + timestamp`. Two lootboxes created in the same millisecond = collision. Unlikely but possible during level-up reward chains.
- **Fix:** Append a short random suffix or use a counter.

## AI Toolkit

Load with: `/ai-toolkit`

Key tools for this work:
- **`Get-CodeItem`** — read a single function without loading the whole file
- **`Get-FileOutline`** — see file structure before diving in
- **`Search-Code`** — find all references to a symbol (blast radius)
- **`Verify-Build`** — confirm compilation (or just `cd go && go vet ./...`)
- **`Get-CodeHealth`** — spot complexity/quality issues you might have missed

Batch syntax (saves 700ms engine tax per call):
```powershell
pwsh -Command "& C:\Users\cra88y\Dev\Repos\blockjitsu\.agent\ai-tools\ai.ps1 -Ops '[[\""Get-CodeItem\"",\""-Path\"",\""go/items/lootbox.go\"",\""-FunctionName\"",\""generateLootboxContents\""]]'"
```

## Style Guide

- Comments: short, direct, no jargon. Write like the README author, not an AI.
- Errors: use existing sentinel pattern in `go/errors/`.
- No decorative banners or numbered steps in comments.
- `go vet ./...` must pass after every change.
