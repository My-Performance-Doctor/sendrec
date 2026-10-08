package video

import (
	"context"
	"strconv"
)

type editVersionKey struct{}

// The version is captured by UPDATE RETURNING when the mutation is accepted,
// before starting the goroutine. Recovery can release a hung claim, but cannot
// let its eventual completion or fallback overwrite a later accepted edit.
func editVersionFence(ctx context.Context) string {
	if version, ok := ctx.Value(editVersionKey{}).(int); ok {
		return " AND media_version = " + strconv.Itoa(version)
	}
	return ""
}
