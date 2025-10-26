package errors

import (
	"github.com/heroiclabs/nakama-common/runtime"
)

// common Error defs 13 = internal, 3 = invalid argument
var (
	ErrInternalError  = runtime.NewError("internal server error", 13) // INTERNAL
	ErrMarshal        = runtime.NewError("cannot marshal type", 13)
	ErrNoInputAllowed = runtime.NewError("no input allowed", 3)
	ErrNoUserIdFound  = runtime.NewError("no user ID in context", 3)
	ErrUnmarshal      = runtime.NewError("cannot unmarshal type", 13)

	ErrNoCategory       = runtime.NewError("invalid category", 13)
	ErrInvalidItem      = runtime.NewError("invalid item", 13)
	ErrInvalidLevelTree = runtime.NewError("level tree doesnt exist", 13)
)
