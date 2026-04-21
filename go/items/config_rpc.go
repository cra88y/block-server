package items

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type ServerMetaRequest struct {
	ClientVersion string `json:"client_version"`
}

type ServerMetaResponse struct {
	Status           string `json:"status"` // "ok" or "update_required"
	MinClientVersion string `json:"min_client_version"`
	ConfigVersion    string `json:"config_version"`
}

// RpcGetServerMeta validates the client version before allowing deeper network interaction.
func RpcGetServerMeta(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req ServerMetaRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Warn("Failed to unmarshal ServerMetaRequest: %v", err)
	}

	minClientVersion := GetMinClientVersion()
	status := "ok"

	if !IsVersionValid(req.ClientVersion, minClientVersion) {
		status = "update_required"
	}

	resp := ServerMetaResponse{
		Status:           status,
		MinClientVersion: minClientVersion,
		ConfigVersion:    GetConfigVersion(),
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}

	return string(out), nil
}

// RpcGetGameConfig returns a unified JSON containing both the item manifest
// and the match/economy economy rules.
func RpcGetGameConfig(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// gamedata (items.json) is now exported as a complete UnifiedConfig containing both items and economy.
	return string(gamedata), nil
}
