// Package sharelink builds a video's public link. It imports nothing from the app, so
// packages that video itself depends on, such as integration, can use it.
package sharelink

import "regexp"

// TokenPattern is the shape the short link serves: new 16-character tokens and the
// 22-character ones minted before them. The router matches the short path with it.
const TokenPattern = `(?:[a-z0-9]{16}|[A-Za-z0-9_-]{22})`

var shortToken = regexp.MustCompile(`^` + TokenPattern + `$`)

// Watch is a video's public link: the short one for every token the short path
// serves, and /watch/{token}, which serves the same page, for any other shape.
func Watch(baseURL, shareToken string) string {
	if !shortToken.MatchString(shareToken) {
		return baseURL + "/watch/" + shareToken
	}
	return baseURL + "/" + shareToken
}
