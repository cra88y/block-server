// Package errors defines sentinel errors for all RPCs. Return these unwrapped — wrapping changes the gRPC code on the wire.
package errors

import "github.com/heroiclabs/nakama-common/runtime"

// gRPC status codes.
const (
	CodeInternal   = 13 // codes.Internal
	CodeInvalidArg = 3  // codes.InvalidArgument
	CodeForbidden  = 7  // codes.PermissionDenied
)

// Unified error definitions
var (
	// Internal errors (code 13)
	ErrInternalError          = runtime.NewError("internal server error", CodeInternal)
	ErrMarshal                = runtime.NewError("cannot marshal type", CodeInternal)
	ErrUnmarshal              = runtime.NewError("cannot unmarshal type", CodeInternal)
	ErrNoCategory             = runtime.NewError("invalid category", CodeInternal)
	ErrInvalidItem            = runtime.NewError("invalid item", CodeInternal)
	ErrInvalidLevelTree       = runtime.NewError("level tree doesnt exist", CodeInternal)
	ErrParse                  = runtime.NewError("error parsing value", CodeInternal)
	ErrInventoryFailure       = runtime.NewError("inventory system error", CodeInternal)
	ErrInvalidConfig          = runtime.NewError("invalid item configuration", CodeInternal)
	ErrFailedGrantPetXP       = runtime.NewError("failed to grant pet XP", CodeInternal)
	ErrFailedCheckOwnership   = runtime.NewError("failed to check pet ownership", CodeInternal)
	ErrCouldNotGetAccount     = runtime.NewError("could not get user account", CodeInternal)
	ErrCouldNotReadStorage    = runtime.NewError("could not read storage", CodeInternal)
	ErrCouldNotWriteStorage   = runtime.NewError("could not write storage", CodeInternal)
	ErrCouldNotUnmarshal      = runtime.NewError("could not unmarshal storage data", CodeInternal)
	ErrCouldNotUpdateWallet   = runtime.NewError("could not update wallet", CodeInternal)
	ErrDropsAlreadyClaimed    = runtime.NewError("drops already claimed for user", CodeInternal)
	ErrEquipmentUnavailable   = runtime.NewError("equipment system unavailable", CodeInternal)
	ErrInventoryUnavailable   = runtime.NewError("inventory system unavailable", CodeInternal)
	ErrProgressionUnavailable = runtime.NewError("progression unavailable", CodeInternal)

	// Invalid argument errors (code 3)
	ErrNoInputAllowed          = runtime.NewError("no input allowed", CodeInvalidArg)
	ErrNoUserIdFound           = runtime.NewError("no user ID in context", CodeInvalidArg)
	ErrInvalidInput            = runtime.NewError("invalid request", CodeInvalidArg)
	ErrNotOwned                = runtime.NewError("item not owned", CodeInvalidArg)
	ErrInvalidItemID           = runtime.NewError("invalid item ID", CodeInvalidArg)
	ErrItemNotFound            = runtime.NewError("item not found", CodeInvalidArg)
	ErrInvalidAbility          = runtime.NewError("invalid ability for item", CodeInvalidArg)
	ErrInvalidAbilityPet       = runtime.NewError("invalid ability for pet", CodeInvalidArg)
	ErrInvalidAbilityClass     = runtime.NewError("invalid ability for class", CodeInvalidArg)
	ErrNoAbilitiesAvailable    = runtime.NewError("no abilities available", CodeInvalidArg)
	ErrAbilityNotFound         = runtime.NewError("ability not found", CodeInvalidArg)
	ErrAbilityNotUnlocked      = runtime.NewError("ability not unlocked", CodeInvalidArg)
	ErrInsufficientPetTreats   = runtime.NewError("insufficient pet treats", CodeInvalidArg)
	ErrInvalidExperience       = runtime.NewError("invalid experience amount", CodeInvalidArg)
	ErrInvalidItemType         = runtime.NewError("invalid item type for experience", CodeInvalidArg)
	ErrCouldNotEquipAbility    = runtime.NewError("couldn't equip ability", CodeInvalidArg)
	ErrCouldNotEquipItem       = runtime.NewError("couldn't equip item", CodeInvalidArg)
	ErrCouldNotEquipClass      = runtime.NewError("couldn't equip class", CodeInvalidArg)
	ErrCouldNotEquipBackground = runtime.NewError("couldn't equip background", CodeInvalidArg)
	ErrCouldNotEquipStyle      = runtime.NewError("couldn't equip style", CodeInvalidArg)
	ErrInvalidPetID            = runtime.NewError("invalid pet ID", CodeInvalidArg)
	ErrInvalidLevelThresholds  = runtime.NewError("invalid level thresholds", CodeInvalidArg)
	ErrLootboxAlreadyOpened    = runtime.NewError("lootbox already opened", CodeInvalidArg)

	// Forbidden errors (code 7)
	ErrItemNotOwnedForbidden = runtime.NewError("item not owned", CodeForbidden)
	ErrPetNotOwned           = runtime.NewError("pet not owned", CodeForbidden)
	ErrClassNotOwned         = runtime.NewError("class not owned", CodeForbidden)

	// Transaction / commit errors (code 13)
	ErrTransactionFailed = runtime.NewError("transaction failed", CodeInternal)
	ErrPrepareFailed     = runtime.NewError("prepare failed", CodeInternal)
)
