package tray

import "encoding/base64"

// iconData is a 22x22 black+alpha PNG key glyph, embedded so the binary stays
// self-contained. Black-with-alpha makes it a valid macOS template image, which
// is what lets it invert correctly in dark mode.
var iconData = mustDecode(iconB64)

const iconB64 = "iVBORw0KGgoAAAANSUhEUgAAABYAAAAWCAYAAADEtGw7AAAAQklEQVR42mNgGAWjgBTwHwlT3cD/" +
	"1LLgPxGYIoNJlaMoTMn2zdAzmJww/o+DTbGriDaY1HRMksGk5DyyDB4FgxwAAHiqWaeeKpsQAAAA" +
	"AElFTkSuQmCC"

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
