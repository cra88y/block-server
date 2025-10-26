package items

type GameDataStuct struct {
	Pets        map[uint32]Pet        `json:"pets"`
	Classes     map[uint32]Class      `json:"classes"`
	Backgrounds map[uint32]Background `json:"backgrounds"`
	PieceStyles map[uint32]PieceStyle `json:"piece_styles"`
	LevelTrees  map[string]LevelTree  `json:"level_trees"`
}

type Pet struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
	BackgroundIDs []uint32 `json:"backgroundIds"`
	StyleIDs      []uint32 `json:"styleIds"`
	LevelTreeName string   `json:"levelTreeName"`
}

type Class struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
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

	storageCollectionEquipment = "equipment"
	// storageKeyPet              = "pet"         // 0
	// storageKeyClass            = "class"       // 2
	// storageKeyBackground       = "background"  // 2
	// storageKeyPieceStyle       = "piece_style" // 0
	storageCollectionProgression = "progression"
)

const (
	ProgressionKeyPet   = "pet_"
	ProgressionKeyClass = "class_"
)

type ItemProgression struct {
	Level int `json:"l"`
	Exp   int `json:"e"`

	EquippedAbility int `json:"ea"`
	EquippedSprite  int `json:"es"`

	AbilitiesUnlocked   int `json:"au"`
	SpritesUnlocked     int `json:"su"`
	BackgroundsUnlocked int `json:"bu"`
	PieceStylesUnlocked int `json:"pu"`
}

type EquipRequest struct {
	ID uint32 `json:"id"`
}

type AbilityEquipRequest struct {
	ItemID    uint32 `json:"id"`
	AbilityID uint32 `json:"ability_id"`
}

type LevelReward struct {
	Gold        uint32   `json:"gold,omitempty"`
	Gems        uint32   `json:"gems,omitempty"`
	Abilities   []uint32 `json:"abilities,omitempty"`
	Backgrounds []uint32 `json:"backgrounds,omitempty"`
	PieceStyles []uint32 `json:"piece_styles,omitempty"`
	Sprites     []uint32 `json:"sprites,omitempty"`
}
