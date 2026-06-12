package items

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// InventoryMutator is a centralized fulfillment engine for inventory operations.
// It resolves the "Read-Modify-Overwrite" race condition by aggregating all item
// grants and revokes in memory, then issuing exactly ONE database write per storage key.
// This guarantees strict OCC transaction safety during complex bundle grants.
type InventoryMutator struct {
	adds    map[string][]uint32 // e.g. "pet" -> [1, 2]
	removes map[string][]uint32 // e.g. "class" -> [3]
	
	// Track progression init requirements for new items
	progressionInits map[string][]uint32 
}

func NewInventoryMutator() *InventoryMutator {
	return &InventoryMutator{
		adds:             make(map[string][]uint32),
		removes:          make(map[string][]uint32),
		progressionInits: make(map[string][]uint32),
	}
}

// AddItem queues an item to be granted.
func (m *InventoryMutator) AddItem(itemType string, itemID uint32) {
	key := m.resolveStorageKey(itemType)
	if key == "" {
		return
	}
	m.adds[key] = append(m.adds[key], itemID)
}

// RemoveItem queues an item for surgical revocation.
func (m *InventoryMutator) RemoveItem(itemType string, itemID uint32) {
	key := m.resolveStorageKey(itemType)
	if key == "" {
		return
	}
	m.removes[key] = append(m.removes[key], itemID)
}

func (m *InventoryMutator) resolveStorageKey(itemType string) string {
	switch itemType {
	case "pet", storageKeyPet:
		return storageKeyPet
	case "class", storageKeyClass:
		return storageKeyClass
	case "background", storageKeyBackground:
		return storageKeyBackground
	case "piece_style", storageKeyPieceStyle:
		return storageKeyPieceStyle
	}
	return ""
}

// CompileWrites executes a single database read batch, applies in-memory mutations,
// and returns a PendingWrites object containing OCC-locked StorageWrites.
func (m *InventoryMutator) CompileWrites(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (*PendingWrites, error) {
	pending := NewPendingWrites()

	// 1. Gather all unique storage keys we need to read/write
	keysToRead := make(map[string]bool)
	for k := range m.adds {
		keysToRead[k] = true
	}
	for k := range m.removes {
		keysToRead[k] = true
	}

	if len(keysToRead) == 0 {
		return pending, nil
	}

	// 2. Execute exactly ONE read batch
	var reads []*runtime.StorageRead
	for k := range keysToRead {
		reads = append(reads, &runtime.StorageRead{
			Collection: storageCollectionInventory,
			Key:        k,
			UserID:     userID,
		})
	}

	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return nil, err
	}

	// Map existing data by key
	existingData := make(map[string]InventoryData)
	versions := make(map[string]string)
	for _, obj := range objs {
		var data InventoryData
		if err := json.Unmarshal([]byte(obj.Value), &data); err != nil {
			return nil, fmt.Errorf("CRITICAL: failed to unmarshal inventory %s: %w", obj.Key, err)
		}
		existingData[obj.Key] = data
		versions[obj.Key] = obj.Version
	}

	// 3. Apply mutations in-memory per key
	for k := range keysToRead {
		data := existingData[k] // Value semantic is fine here
		if data.Items == nil {
			data.Items = make([]uint32, 0)
		}

		changed := false

		// Apply Adds
		for _, addID := range m.adds[k] {
			if !contains(data.Items, addID) {
				data.Items = append(data.Items, addID)
				changed = true

				// Only queue progression init if the item was truly newly added
				if k == storageKeyPet || k == storageKeyClass {
					progKey := ProgressionKeyClass
					if k == storageKeyPet {
						progKey = ProgressionKeyPet
					}
					m.progressionInits[progKey] = append(m.progressionInits[progKey], addID)
				}
			}
		}

		// Apply Removes
		for _, remID := range m.removes[k] {
			newItems := make([]uint32, 0)
			removedLocally := false
			for _, id := range data.Items {
				if id == remID && !removedLocally {
					removedLocally = true // Remove only one instance
					changed = true
				} else {
					newItems = append(newItems, id)
				}
			}
			data.Items = newItems
		}

		// Queue exactly ONE write per key if changes occurred
		if changed {
			// OCC Constraint: Empty string means unconditional overwrite.
			// Coerce to "*" to enforce Insert-Only if the row does not exist.
			v := versions[k]
			if v == "" {
				v = "*" 
			}

			valueBytes, _ := json.Marshal(data)
			pending.AddStorageWrite(&runtime.StorageWrite{
				Collection:      storageCollectionInventory,
				Key:             k,
				UserID:          userID,
				Value:           string(valueBytes),
				PermissionRead:  2,
				PermissionWrite: 0,
				Version:         v, // OCC lock
			})
		}
	}

	// 4. Apply progression initializations for new pets/classes
	for progKey, itemIDs := range m.progressionInits {
		for _, id := range itemIDs {
			category := storageKeyClass
			if progKey == ProgressionKeyPet {
				category = storageKeyPet
			}
			
			treeName, _ := GetLevelTreeName(category, id)
			prog := DefaultProgression(treeName)
			value, err := json.Marshal(prog)
			if err != nil {
				return nil, fmt.Errorf("CRITICAL: failed to marshal progression init for %s %d: %w", progKey, id, err)
			}
			
			key := progKey + fmt.Sprintf("%d", id)
			pending.AddStorageWrite(&runtime.StorageWrite{
					Collection:      storageCollectionProgression,
					Key:             key,
					UserID:          userID,
					Value:           string(value),
					PermissionRead:  2,
					PermissionWrite: 0,
					Version:         "*", // Enforce Insert-Only to protect existing progression
				})
		}
	}

	return pending, nil
}

func contains(arr []uint32, val uint32) bool {
	for _, v := range arr {
		if v == val {
			return true
		}
	}
	return false
}