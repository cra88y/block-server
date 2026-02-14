package items

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RpcGetGameConfig returns the embedded game configuration JSON.
// Client can cache this for offline use and hot-update support.
func RpcGetGameConfig(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// gamedata is already embedded via //go:embed in game.go
	return string(gamedata), nil
}
