package main

import (
	"context"
	"database/sql"
	"time"

	"block-server/items"
	"block-server/session"

	"github.com/heroiclabs/nakama-common/runtime"
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
	if err := initializer.RegisterRpc("get_game_config", items.RpcGetGameConfig); err != nil {
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
	if err := initializer.RegisterRpc("use_gold_for_class_xp", items.RpcUseGoldForClassXP); err != nil {
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
	if err := initializer.RegisterRpc("notify_match_start", items.RpcNotifyMatchStart); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("submit_match_result", items.RpcSubmitMatchResult); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_lootboxes", items.RpcGetLootboxes); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("open_lootbox", items.RpcOpenLootbox); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	// Shop RPCs
	if err := items.LoadShopData(); err != nil {
		logger.Warn("Failed to load shop data (shop disabled): %v", err)
	} else {
		logger.Info("Loaded shop data: %d items, %d IAP products",
			len(items.GetShopConfig().ShopItems),
			len(items.GetShopConfig().IAPProducts))
	}
	if err := initializer.RegisterRpc("get_shop_catalog", items.RpcGetShopCatalog); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("purchase_shop_item", items.RpcPurchaseShopItem); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("purchase_lootbox", items.RpcPurchaseLootbox); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("validate_iap_receipt", items.RpcValidateIAPReceipt); err != nil {
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
