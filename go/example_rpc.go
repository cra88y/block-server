//////////////// Generic RPC
//////////////// http://127.0.0.1:7350/v2/rpc/authoritative_write_rpc
//////////////// Hook
// if err = initializer.RegisterRpc("authoritative_write_rpc", AuthoritativeWriteRPC); err != nil {
//   logger.Error("Unable to register: %v", err)
// 	return err
// }
//
//////////////// Callback
// func AuthoritativeWriteRPC(ctx context.Context,
// 							logger runtime.Logger,
// 							db *sql.DB,
// 							nk runtime.NakamaModule,
// 							payload string)
// (string, error) {
// 	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

// 	data := map[string]interface{}{
// 		"achievementPoints": 100,
// 		"unlockedAchievements": []string{"max-level", "defeat-boss-2", "equip-rare-gear"},
// 	}
//
// 	bytes, err := json.Marshal(data)
// 	if err != nil {
// 		return "", runtime.NewError("error marshaling data", 13)
// 	}
//
// 	write := &runtime.StorageWrite{
// 		Collection:      "Unlocks",
// 		Key:             "Achievements",
// 		UserID:          userID,
// 		Value:           string(bytes),
// 		PermissionRead:  1, // Only the server and owner can read
// 		PermissionWrite: 0, // Only the server can write
// 	}
//
// 	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{write})
// 	if err != nil {
// 		return "", runtime.NewError("error saving data", 13)
// 	}
//
// 	return "<JsonResponse>", nil
// }
