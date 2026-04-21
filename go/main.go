package main

import (
	"context"
	"database/sql"
	"time"

	"block-server/items"
	"block-server/session"

	"github.com/heroiclabs/nakama-common/runtime"
)

// startTelemetryCleanup starts a goroutine that periodically cleans up old telemetry data
// HAZARD: Run during off-peak hours to avoid impacting active users
func startTelemetryCleanup(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) {
	go func() {
		// Run cleanup every 24 hours
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.Info("Starting scheduled telemetry cleanup...")
				if err := items.CleanupOldTelemetry(ctx, logger, nk); err != nil {
					logger.Error("Telemetry cleanup failed: %v", err)
				} else {
					logger.Info("Telemetry cleanup completed successfully")
				}
			}
		}
	}()
}

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

	for _, lb := range []struct {
		id, sortOrder, operator, reset string
	}{
		{items.LeaderboardSoloSeason, "desc", "best", ""},
		{items.LeaderboardSoloWeekly, "desc", "best", "0 0 * * 1"},
		{items.Leaderboard1v1Season, "desc", "incr", ""},
		{items.Leaderboard1v1Weekly, "desc", "incr", "0 0 * * 1"},
	} {
		if err := nk.LeaderboardCreate(ctx, lb.id, true, lb.sortOrder, lb.operator, lb.reset, nil, true); err != nil {
			logger.Error("Failed to create leaderboard %s: %v", lb.id, err)
			// Non-fatal: boards may already exist from a previous startup.
		}
	}
	logger.Info("Leaderboards bootstrapped: %s, %s, %s, %s",
		items.LeaderboardSoloSeason, items.LeaderboardSoloWeekly,
		items.Leaderboard1v1Season, items.Leaderboard1v1Weekly)

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
	if err := initializer.RegisterRpc("claim_progression_reward", items.RpcClaimProgressionReward); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("claim_all_progression_rewards", items.RpcClaimAllProgressionRewards); err != nil {
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
	if err := initializer.RegisterRpc("report_round_result", items.RpcReportRoundResult); err != nil {
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
	if err := initializer.RegisterRpc("revoke_iap_purchase", items.RpcRevokeIAPPurchase); err != nil {
		logger.Error("Unable to register revoke_iap_purchase: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("submit_telemetry", items.RpcSubmitTelemetry); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	// Competitive / Leaderboard RPCs
	if err := initializer.RegisterRpc("get_leaderboard", items.RpcGetLeaderboard); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_friends_leaderboard", items.RpcGetFriendsLeaderboard); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_player_stats", items.RpcGetPlayerStats); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("get_match_history", items.RpcGetMatchHistory); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	if err := initializer.RegisterRpc("get_users_loadouts", items.RpcGetUsersLoadouts); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	if err := initializer.RegisterRpc("delete_account", items.RpcDeleteAccount); err != nil {
		logger.Error("Unable to register delete_account: %v", err)
		return err
	}

	// Social RPCs
	if err := initializer.RegisterRpc("send_game_invite", items.RpcSendGameInvite); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("cancel_game_invite", items.RpcCancelGameInvite); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}
	if err := initializer.RegisterRpc("decline_game_invite", items.RpcDeclineGameInvite); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	if err := session.RegisterSessionEvents(db, nk, initializer); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	// Start telemetry cleanup goroutine
	startTelemetryCleanup(ctx, logger, nk)

	logger.Info("Plugin loaded in '%d' msec.", time.Since(initStart).Milliseconds())
	return nil
}

