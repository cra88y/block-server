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
	rpcIdRewards = "rewards"
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
	if err := initializer.RegisterAfterAuthenticateDevice(items.AfterAuthorizeUserDevice); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterAfterAuthenticateGameCenter(items.AfterAuthorizeUserGC); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_inventory", items.RpcGetInventory); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_equipment", items.RpcGetEquipment); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_progression", items.RpcGetProgression); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("use_pet_treat", items.RpcUsePetTreat); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_class", items.RpcEquipClass); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_pet", items.RpcEquipPet); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_class_ability", items.RpcEquipClassAbility); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_pet_ability", items.RpcEquipPetAbility); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_background", items.RpcEquipBackground); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("equip_piece_style", items.RpcEquipPieceStyle); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := session.RegisterSessionEvents(db, nk, initializer); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	logger.Info("Plugin loaded in '%d' msec.", time.Since(initStart).Milliseconds())
	return nil
}
