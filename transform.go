package main

import "regexp"

var slashRe = regexp.MustCompile(`^[Ss]lash\s+`)

// transformText applies output transformations, e.g. "slash help" -> "/help".
func transformText(text string) string {
	return slashRe.ReplaceAllString(text, "/")
}
