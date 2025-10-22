# Nakama Go Modules
Built automatically in Dockerfile

Use `go mod vendor` if dependencies change.

Built with `go build --trimpath --mod=vendor --buildmode=plugin -o ./backend.so`
