package items

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RpcGetGameConfig returns a unified JSON containing both the item manifest
// and the match/economy economy rules.
func RpcGetGameConfig(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// gamedata (items.json) is now exported as a complete UnifiedConfig containing both items and economy.
	return string(gamedata), nil
}
