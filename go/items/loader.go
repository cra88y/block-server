package items

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

//go:embed gamedata/items.json
var gamedata []byte

var (
	GameData     *GameDataStuct
	GameDataOnce sync.Once
)

func LoadGameData() error {
	var initErr error
	var parseErrors []error
	GameDataOnce.Do(func() {
		var raw struct {
			Pets        map[string]Pet        `json:"pets"`
			Classes     map[string]Class      `json:"classes"`
			Backgrounds map[string]Background `json:"backgrounds"`
			PieceStyles map[string]PieceStyle `json:"piece_styles"`
			LevelTrees  map[string]LevelTree  `json:"level_trees"`
		}

		if err := json.Unmarshal(gamedata, &raw); err != nil {
			initErr = err
			return
		}

		GameData = &GameDataStuct{
			Pets:        make(map[uint32]*Pet, len(raw.Pets)),
			Classes:     make(map[uint32]*Class, len(raw.Classes)),
			Backgrounds: make(map[uint32]Background, len(raw.Backgrounds)),
			PieceStyles: make(map[uint32]PieceStyle, len(raw.PieceStyles)),
			LevelTrees:  make(map[string]LevelTree, len(raw.LevelTrees)),
		}

		// Level trees
		for name, tree := range raw.LevelTrees {
			t := tree
			t.LevelThresholds = make([]int, t.MaxLevel+1)
			cumulative := 0
			for level := 1; level <= t.MaxLevel; level++ {
				cumulative += t.BaseXP * level * level
				t.LevelThresholds[level] = cumulative
			}
			GameData.LevelTrees[name] = t
		}

		// Pets
		for k, v := range raw.Pets {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid pet ID %q: %w", k, err))
				continue
			}
			GameData.Pets[uint32(id)] = &Pet{
				Name:          v.Name,
				SpriteCount:   v.SpriteCount,
				AbilityIDs:    v.AbilityIDs,
				AbilitySet:    createAbilitySet(v.AbilityIDs),
				BackgroundIDs: v.BackgroundIDs,
				StyleIDs:      v.StyleIDs,
				LevelTreeName: v.LevelTreeName,
			}
		}

		// Classes
		for k, v := range raw.Classes {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid class ID %q: %w", k, err))
				continue
			}
			GameData.Classes[uint32(id)] = &Class{
				Name:          v.Name,
				SpriteCount:   v.SpriteCount,
				AbilityIDs:    v.AbilityIDs,
				AbilitySet:    createAbilitySet(v.AbilityIDs),
				BackgroundIDs: v.BackgroundIDs,
				StyleIDs:      v.StyleIDs,
				LevelTreeName: v.LevelTreeName,
			}
		}

		// Backgrounds
		for k, v := range raw.Backgrounds {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("invalid background ID %q: %w", k, err))
				continue
			}
			GameData.Backgrounds[uint32(id)] = v
		}

		// Piece Styles
		for k, v := range raw.PieceStyles {
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

func createAbilitySet(ids []uint32) map[uint32]struct{} {
	set := make(map[uint32]struct{})
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}
