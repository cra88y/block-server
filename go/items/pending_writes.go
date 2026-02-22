package items

import (
	"encoding/json"
	"strconv"

	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

// PendingWrites batches storage + wallet writes for a single atomic MultiUpdate commit.
type PendingWrites struct {
	StorageWrites []*runtime.StorageWrite
	WalletUpdates []*runtime.WalletUpdate
	Payload       *notify.RewardPayload
}

// NewPendingWrites creates a new PendingWrites collector
func NewPendingWrites() *PendingWrites {
	return &PendingWrites{
		StorageWrites: make([]*runtime.StorageWrite, 0),
		WalletUpdates: make([]*runtime.WalletUpdate, 0),
	}
}

// AddStorageWrite adds a storage write to the pending batch
func (pw *PendingWrites) AddStorageWrite(write *runtime.StorageWrite) {
	pw.StorageWrites = append(pw.StorageWrites, write)
}

// AddWalletUpdate adds a wallet update to the pending batch
func (pw *PendingWrites) AddWalletUpdate(userID string, changeset map[string]int64) {
	pw.WalletUpdates = append(pw.WalletUpdates, &runtime.WalletUpdate{
		UserID:    userID,
		Changeset: changeset,
	})
}

// AddWalletDeduction is a convenience method for deducting currency
func (pw *PendingWrites) AddWalletDeduction(userID string, currency string, amount int64) {
	pw.AddWalletUpdate(userID, map[string]int64{currency: -amount})
}

// Merge combines another PendingWrites into this one
func (pw *PendingWrites) Merge(other *PendingWrites) {
	if other == nil {
		return
	}
	pw.StorageWrites = append(pw.StorageWrites, other.StorageWrites...)
	pw.WalletUpdates = append(pw.WalletUpdates, other.WalletUpdates...)
	
	// Merge payloads
	if other.Payload != nil {
		if pw.Payload == nil {
			pw.Payload = other.Payload
		} else {
			pw.MergePayload(other.Payload)
		}
	}
}

// MergePayload additively combines other into pw.Payload. Wallet is summed, items appended; levels/XP are not touched.
func (pw *PendingWrites) MergePayload(other *notify.RewardPayload) {
	if other == nil {
		return
	}
	if pw.Payload == nil {
		pw.Payload = notify.NewRewardPayload("")
	}

	if other.Wallet != nil {
		if pw.Payload.Wallet == nil {
			pw.Payload.Wallet = &notify.WalletDelta{}
		}
		pw.Payload.Wallet.Gold += other.Wallet.Gold
		pw.Payload.Wallet.Gems += other.Wallet.Gems
		pw.Payload.Wallet.Treats += other.Wallet.Treats
	}

	if other.Inventory != nil {
		if pw.Payload.Inventory == nil {
			pw.Payload.Inventory = &notify.InventoryDelta{Items: []notify.ItemGrant{}}
		}
		pw.Payload.Inventory.Items = append(pw.Payload.Inventory.Items, other.Inventory.Items...)
	}

	if other.Progression != nil && len(other.Progression.Unlocks) > 0 {
		if pw.Payload.Progression == nil {
			pw.Payload.Progression = &notify.ProgressionDelta{}
		}
		pw.Payload.Progression.Unlocks = append(pw.Payload.Progression.Unlocks, other.Progression.Unlocks...)
	}
}

// IsEmpty returns true if no writes are pending
func (pw *PendingWrites) IsEmpty() bool {
	return len(pw.StorageWrites) == 0 && len(pw.WalletUpdates) == 0
}

// BuildProgressionWrite creates a storage write for progression data
func BuildProgressionWrite(userID string, progressionKey string, itemID uint32, prog *ItemProgression) (*runtime.StorageWrite, error) {
	value, err := json.Marshal(prog)
	if err != nil {
		return nil, err
	}
	
	return &runtime.StorageWrite{
		Collection:      storageCollectionProgression,
		Key:             progressionKey + itoa(itemID),
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  2,
		PermissionWrite: 0,
		Version:         prog.Version, // OCC version for atomic update
	}, nil
}

// BuildInventoryWrite creates a storage write for inventory data
func BuildInventoryWrite(userID string, storageKey string, items []uint32, version string) (*runtime.StorageWrite, error) {
	data := InventoryData{Items: items}
	value, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	
	return &runtime.StorageWrite{
		Collection:      storageCollectionInventory,
		Key:             storageKey,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  2,
		PermissionWrite: 0,
		Version:         version,
	}, nil
}

// Helper for uint32 to string conversion
func itoa(n uint32) string {
	return strconv.FormatUint(uint64(n), 10)
}
