package items

type GameDataStruct struct {
	Pets        map[uint32]*Pet       `json:"pets"`
	Classes     map[uint32]*Class     `json:"classes"`
	Backgrounds map[uint32]Background `json:"backgrounds"`
	PieceStyles map[uint32]PieceStyle `json:"piece_styles"`
	LevelTrees  map[string]LevelTree  `json:"level_trees"`
}

type Pet struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
	AbilitySet    map[uint32]struct{}
	BackgroundIDs []uint32 `json:"backgroundIds"`
	StyleIDs      []uint32 `json:"styleIds"`
	LevelTreeName string   `json:"levelTreeName"`
}

type Class struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
	AbilitySet    map[uint32]struct{}
	BackgroundIDs []uint32 `json:"backgroundIds"`
	StyleIDs      []uint32 `json:"styleIds"`
	LevelTreeName string   `json:"levelTreeName"`
}

type Background struct {
	Name string `json:"name"`
}

type PieceStyle struct {
	Name string `json:"name"`
}

type LevelTree struct {
	MaxLevel        int   `json:"max_level"`
	BaseXP          int   `json:"base_xp"`
	LevelThresholds []int `json:"level_thresholds"`
	RewardedLevels  []int `json:"rewarded_levels"`
	Rewards         map[string]struct {
		Gold        string `json:"gold,omitempty"`
		Gems        string `json:"gems,omitempty"`
		Abilities   string `json:"abilities,omitempty"`
		Backgrounds string `json:"backgrounds,omitempty"`
		PieceStyles string `json:"piece_styles,omitempty"`
		Sprites     string `json:"sprites,omitempty"`
	} `json:"rewards"`
}

const (
	storageCollectionInventory = "inventory"
	storageKeyPet              = "pets"         // [0,1,2]
	storageKeyClass            = "classes"      // [0,1,2]
	storageKeyBackground       = "backgrounds"  // [0,1,2,3]
	storageKeyPieceStyle       = "piece_styles" //[0]

	storageCollectionEquipment   = "equipment"
	storageCollectionProgression = "progression"
)

const (
	ProgressionKeyPet    = "pet_"
	ProgressionKeyClass  = "class_"
	ProgressionKeyPlayer = "player_"
)

type ItemProgression struct {
	Level int `json:"level"`
	Exp   int `json:"xp"`

	EquippedAbility int `json:"ea"`
	EquippedSprite  int `json:"es"`

	AbilitiesUnlocked   int `json:"au"`
	SpritesUnlocked     int `json:"su"`
	BackgroundsUnlocked int `json:"bu"`
	PieceStylesUnlocked int `json:"pu"`

	Version string `json:"-"`
}

type AbilityEquipRequest struct {
	ItemID    uint32 `json:"id"`
	AbilityID uint32 `json:"ability_id"`
}




type EquipmentResponse struct {
	Pet        uint32 `json:"pet"`
	Class      uint32 `json:"class"`
	Background uint32 `json:"background"`
	PieceStyle uint32 `json:"piece_style"`
}

type InventoryResponse struct {
	Pets        []uint32 `json:"pets"`
	Classes     []uint32 `json:"classes"`
	Backgrounds []uint32 `json:"backgrounds"`
	PieceStyles []uint32 `json:"piece_styles"`
}

type ProgressionResponse struct {
	Pets    map[uint32]ItemProgression `json:"pets"`
	Classes map[uint32]ItemProgression `json:"classes"`
}

type InventoryData struct {
	Items []uint32 `json:"items"`
}

type EquipmentData struct {
	ID uint32 `json:"id"`
}

type PetTreatRequest struct {
	PetID uint32 `json:"pet_id"`
}

// Match Result Types
type MatchResultRequest struct {
	MatchID          string `json:"match_id"`
	Won              bool   `json:"won"`
	FinalScore       int    `json:"final_score"`
	OpponentScore    int    `json:"opponent_score"`
	MatchDurationSec int    `json:"match_duration_sec"`
	EquippedPetID    uint32 `json:"equipped_pet_id"`
	EquippedClassID  uint32 `json:"equipped_class_id"`
}
