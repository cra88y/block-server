package items

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"block-server/errors"
)

//go:embed gamedata/items.json
var gamedata []byte

var (
	GameData         *GameDataStruct
	GameDataOnce     sync.Once
	starterPack      *StarterPack
	configVersion    string
	minClientVersion string
)

// LoadGameData loads and parses game data from embedded JSON
func LoadGameData() error {
	var initErr error
	var parseErrors []error
	GameDataOnce.Do(func() {
		var raw struct {
			Items struct {
				Pets        map[string]Pet        `json:"pets"`
				Classes     map[string]Class      `json:"classes"`
				Backgrounds map[string]Background `json:"backgrounds"`
				PieceStyles map[string]PieceStyle `json:"piece_styles"`
				LevelTrees  map[string]LevelTree  `json:"level_trees"`
			} `json:"items"`
			Economy          EconomyConfig `json:"economy"`
			StarterPack      StarterPack   `json:"starter_pack"`
			ConfigVersion    string        `json:"config_version"`
			VersionRequirements struct {
				MinClientVersion string `json:"min_client_version"`
			} `json:"version_requirements"`
		}

		if err := json.Unmarshal(gamedata, &raw); err != nil {
			initErr = err
			return
		}

		economyConfig = &raw.Economy
		starterPack = &raw.StarterPack
		configVersion = raw.ConfigVersion
		minClientVersion = raw.VersionRequirements.MinClientVersion
		GameData = &GameDataStruct{
			Pets:        make(map[uint32]*Pet, len(raw.Items.Pets)),
			Classes:     make(map[uint32]*Class, len(raw.Items.Classes)),
			Backgrounds: make(map[uint32]Background, len(raw.Items.Backgrounds)),
			PieceStyles: make(map[uint32]PieceStyle, len(raw.Items.PieceStyles)),
			LevelTrees:  make(map[string]LevelTree, len(raw.Items.LevelTrees)),
		}

		for name, tree := range raw.Items.LevelTrees {
			t := tree
			t.LevelThresholds = make([]int, t.MaxLevel+1)
			cumulative := 0
			for level := 1; level <= t.MaxLevel; level++ {
				cumulative += t.BaseXP * level * level
				t.LevelThresholds[level] = cumulative
			}
			GameData.LevelTrees[name] = t
		}

		for k, v := range raw.Items.Pets {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid pet ID %q: %w", k, err))
				continue
			}
			GameData.Pets[uint32(id)] = &Pet{
				Name:               v.Name,
				SpriteCount:        v.SpriteCount,
				AbilityIDs:         v.AbilityIDs,
				AbilitySet:         createAbilitySet(v.AbilityIDs),
				BackgroundIDs:      v.BackgroundIDs,
				StyleIDs:           v.StyleIDs,
				LevelTreeName:      v.LevelTreeName,
				BaseAttack:         v.BaseAttack,
				AttackScalePercent: v.AttackScalePercent,
				BaseHealth:         v.BaseHealth,
				HealthScalePercent: v.HealthScalePercent,
			}
		}

		for k, v := range raw.Items.Classes {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid class ID %q: %w", k, err))
				continue
			}
			GameData.Classes[uint32(id)] = &Class{
				Name:               v.Name,
				SpriteCount:        v.SpriteCount,
				AbilityIDs:         v.AbilityIDs,
				AbilitySet:         createAbilitySet(v.AbilityIDs),
				BackgroundIDs:      v.BackgroundIDs,
				StyleIDs:           v.StyleIDs,
				LevelTreeName:      v.LevelTreeName,
				BaseAttack:         v.BaseAttack,
				AttackScalePercent: v.AttackScalePercent,
				BaseHealth:         v.BaseHealth,
				HealthScalePercent: v.HealthScalePercent,
			}
		}

		for k, v := range raw.Items.Backgrounds {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid background ID %q: %w", k, err))
				continue
			}
			GameData.Backgrounds[uint32(id)] = v
		}

		for k, v := range raw.Items.PieceStyles {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid piece style ID %q: %w", k, err))
				continue
			}
			GameData.PieceStyles[uint32(id)] = v
		}
	})

	if len(parseErrors) > 0 {
		initErr = fmt.Errorf("%d parse errors: %+v", len(parseErrors), parseErrors)
	}
	return initErr
}

// Game Data Access Functions

func GetPet(id uint32) (*Pet, bool) {
	pet, exists := GameData.Pets[id]
	return pet, exists
}

func GetClass(id uint32) (*Class, bool) {
	class, exists := GameData.Classes[id]
	return class, exists
}

func GetLevelTree(name string) (LevelTree, bool) {
	tree, exists := GameData.LevelTrees[name]
	return tree, exists
}

func GetPetLevelTree(petID uint32) (LevelTree, bool) {
	if pet, exists := GetPet(petID); exists {
		return GetLevelTree(pet.LevelTreeName)
	}
	return LevelTree{}, false
}

func GetClassLevelTree(classID uint32) (LevelTree, bool) {
	if class, exists := GetClass(classID); exists {
		return GetLevelTree(class.LevelTreeName)
	}
	return LevelTree{}, false
}

func GetLevelTreeName(category string, id uint32) (string, error) {
	switch category {
	case storageKeyPet:
		if pet, exists := GameData.Pets[id]; exists {
			return pet.LevelTreeName, nil
		}
	case storageKeyClass:
		if class, exists := GameData.Classes[id]; exists {
			return class.LevelTreeName, nil
		}
	default:
		return "", errors.ErrNoCategory
	}
	return "", errors.ErrInvalidItem
}

func ValidateItemExists(category string, id uint32) bool {
	switch category {
	case storageKeyPet:
		_, exists := GameData.Pets[id]
		return exists
	case storageKeyClass:
		_, exists := GameData.Classes[id]
		return exists
	case storageKeyBackground:
		_, exists := GameData.Backgrounds[id]
		return exists
	case storageKeyPieceStyle:
		_, exists := GameData.PieceStyles[id]
		return exists
	case storageKeyPlayer:
		return id == 0 // Player is always ID 0 (singleton); no game data map needed
	default:
		return false
	}
}

// Level System Functions

func CalculateLevel(treeName string, exp int) (int, error) {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return 0, errors.ErrInvalidLevelTree
	}

	if exp < 0 {
		return 1, nil
	}

	if exp >= tree.LevelThresholds[tree.MaxLevel] {
		return tree.MaxLevel, nil
	}

	thresholds := tree.LevelThresholds
	low, high := 1, tree.MaxLevel
	if len(thresholds) < high {
		return 0, errors.ErrInvalidLevelThresholds
	}
	for low <= high {
		mid := (low + high) / 2
		if exp >= thresholds[mid] {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	return high, nil
}

// Helper Functions

func createAbilitySet(ids []uint32) map[uint32]struct{} {
	set := make(map[uint32]struct{})
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// GetStarterPack returns the starter item pack configuration.
// Falls back to default IDs [0, 0, 0, 0] if not configured in items.json.
func GetStarterPack() *StarterPack {
	if starterPack != nil {
		return starterPack
	}
	// Fallback defaults
	return &StarterPack{
		Pets:        []uint32{0},
		Classes:     []uint32{0},
		Backgrounds: []uint32{0},
		PieceStyles: []uint32{0},
	}
}

// GetConfigVersion returns the data config version stamped at export time.
// Empty string means the embedded items.json predates this feature.
func GetConfigVersion() string {
	return configVersion
}

// GetMinClientVersion returns the minimum client version required for online play.
// Empty string means no gate is currently enforced.
func GetMinClientVersion() string {
	return minClientVersion
}

// IsVersionValid compares client version vs minimum required using simple semantic checking.
func IsVersionValid(clientVer, minReq string) bool {
	if minReq == "" {
		return true
	}
	if clientVer == "" {
		return false
	}
	parseParts := func(v string) (int, int) {
		var major, minor int
		fmt.Sscanf(v, "%d.%d", &major, &minor)
		return major, minor
	}
	cMaj, cMin := parseParts(clientVer)
	mMaj, mMin := parseParts(minReq)

	if cMaj < mMaj {
		return false
	} else if cMaj == mMaj {
		return cMin >= mMin
	}
	return true
}
