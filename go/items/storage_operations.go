package items

import (
	"context"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

func GetUserInventory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (*InventoryResponse, error) {
	inventory := &InventoryResponse{
		Pets:        make([]uint32, 0),
		Classes:     make([]uint32, 0),
		Backgrounds: make([]uint32, 0),
		PieceStyles: make([]uint32, 0),
	}

	reads := []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyPieceStyle, UserID: userID},
	}

	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		logger.WithField("error", err.Error()).Error("Failed to read user inventory")
		return nil, err
	}

	for _, obj := range objs {
		if obj == nil {
			continue
		}

		data, err := UnmarshalJSON[InventoryData](obj.Value)
		if err != nil {
			logger.WithField("error", err.Error()).WithField("obj_key", obj.Key).Warn("Failed to unmarshal inventory data")
			continue
		}

		switch obj.Key {
		case storageKeyPet:
			inventory.Pets = data.Items
		case storageKeyClass:
			inventory.Classes = data.Items
		case storageKeyBackground:
			inventory.Backgrounds = data.Items
		case storageKeyPieceStyle:
			inventory.PieceStyles = data.Items
		default:
			logger.WithField("obj_key", obj.Key).Warn("Unexpected inventory storage key")
		}
	}

	return inventory, nil
}

func GetUserProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (*ProgressionResponse, error) {
	progression := &ProgressionResponse{
		Pets:    make(map[uint32]ItemProgression),
		Classes: make(map[uint32]ItemProgression),
	}

	objects, _, err := nk.StorageList(ctx, "", userID, storageCollectionProgression, 100, "")
	if err != nil {
		logger.WithField("error", err.Error()).Error("Failed to list progression storage objects")
		return progression, nil
	}

	if len(objects) == 0 {
		return progression, nil
	}

	reads := make([]*runtime.StorageRead, 0, len(objects))
	for _, obj := range objects {
		reads = append(reads, &runtime.StorageRead{
			Collection: storageCollectionProgression,
			Key:        obj.Key,
			UserID:     userID,
		})
	}

	storageObjs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return progression, nil
	}

	for _, obj := range storageObjs {
		if obj == nil {
			continue
		}

		p, err := UnmarshalJSON[ItemProgression](obj.Value)
		if err != nil {
			continue
		}

		if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyPet); ok {
			if id, err := ParseUint32Safely(after, logger); err == nil {
				if _, exists := GetPet(id); exists {
					progression.Pets[id] = *p
				}
			}
		} else if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyClass); ok {
			if id, err := ParseUint32Safely(after, logger); err == nil {
				if _, exists := GetClass(id); exists {
					progression.Classes[id] = *p
				}
			}
		}
	}

	return progression, nil
}

// DefaultProgression creates a default progression record
func DefaultProgression() *ItemProgression {
	return &ItemProgression{
		Level:               1,
		Exp:                 0,
		EquippedAbility:     0,
		EquippedSprite:      0,
		AbilitiesUnlocked:   1,
		SpritesUnlocked:     1,
		BackgroundsUnlocked: 0,
		PieceStylesUnlocked: 0,
	}
}