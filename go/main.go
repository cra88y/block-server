package main

import (
	"context"
	"database/sql"
	"time"

	"block-server/items"
	"block-server/session"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	rpcIdRewards   = "rewards"
	rpcIdFindMatch = "find_match"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	initStart := time.Now()
	if err := items.LoadGameData(); err != nil {
		logger.Error("Failed to load game data: %v", err)
		return err
	}
	logger.Info("Loaded game data: %d pets, %d classes, %d backgrounds, %d styles, %d level trees",
		len(items.GameData.Pets),
		len(items.GameData.Classes),
		len(items.GameData.Backgrounds),
		len(items.GameData.PieceStyles),
		len(items.GameData.LevelTrees))
	// after first time player init
	if err := initializer.RegisterAfterAuthenticateDevice(session.AfterAuthroizeUserDevice); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterAfterAuthenticateGameCenter(session.AfterAuthroizeUserGC); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	if err := session.RegisterSessionEvents(db, nk, initializer); err != nil {
		return err
	}
	logger.Info("Plugin loaded in '%d' msec.", time.Since(initStart).Milliseconds())
	return nil
}
