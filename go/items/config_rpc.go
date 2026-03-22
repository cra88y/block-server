package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RpcGetGameConfig returns a unified JSON containing both the item manifest
// and the match/economy economy rules.
func RpcGetGameConfig(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	cfg := GetEconomyConfig()
	cfgJson, err := json.Marshal(cfg)
	if err != nil {
		return "", runtime.NewError("failed to marshal economy config", 13)
	}

	// Result is a composite of the raw gamedata and the dynamic economy config
	response := fmt.Sprintf("{\"items\": %s, \"economy\": %s}", string(gamedata), string(cfgJson))
	return response, nil
}
