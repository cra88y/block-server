package items

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
)

var (
	GameData     *GameDataStuct
	GameDataOnce sync.Once
)

func LoadGameData() error {
	var initErr error
	GameDataOnce.Do(func() {
		data, err := os.ReadFile("items.json")
		if err != nil {
			initErr = err
			return
		}

		var raw struct {
			Pets        map[string]Pet        `json:"pets"`
			Classes     map[string]Class      `json:"classes"`
			Backgrounds map[string]Background `json:"backgrounds"`
			PieceStyles map[string]PieceStyle `json:"piece_styles"`
			LevelTrees  map[string]LevelTree  `json:"level_trees"`
		}

		if err := json.Unmarshal(data, &raw); err != nil {
			initErr = err
			return
		}

		GameData = &GameDataStuct{
			Pets:        make(map[uint32]Pet, len(raw.Pets)),
			Classes:     make(map[uint32]Class, len(raw.Classes)),
			Backgrounds: make(map[uint32]Background, len(raw.Backgrounds)),
			PieceStyles: make(map[uint32]PieceStyle, len(raw.PieceStyles)),
			LevelTrees:  raw.LevelTrees,
		}

		// Pre-calculate level thresholds
		for name, tree := range GameData.LevelTrees {
			thresholds := make([]int, tree.MaxLevel+1)
			for level := 1; level <= tree.MaxLevel; level++ {
				thresholds[level] = tree.BaseXP * level * level
			}
			tree.LevelThresholds = thresholds
			GameData.LevelTrees[name] = tree
		}
		// Pets
		for k, v := range raw.Pets {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				continue
			}
			GameData.Pets[uint32(id)] = v
		}

		// Classes
		for k, v := range raw.Classes {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				continue
			}
			GameData.Classes[uint32(id)] = v
		}

		// Backgrounds
		for k, v := range raw.Backgrounds {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				continue
			}
			GameData.Backgrounds[uint32(id)] = v
		}

		// Piece Styles
		for k, v := range raw.PieceStyles {
			id, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				continue
			}
			GameData.PieceStyles[uint32(id)] = v
		}

	})
	return initErr
}
