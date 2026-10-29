package domain

import (
	"regexp"
	"strings"
)

const technicalZone = `(?:Africa|America|Antarctica|Arctic|Asia|Atlantic|Australia|Europe|Indian|Pacific|Etc)/[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)*`

var parenthesizedZone = regexp.MustCompile(`\s*\(\s*` + technicalZone + `\s*\)`)
var labeledZone = regexp.MustCompile(`(?i)\s*(?:no )?fuso(?: hor[aá]rio)?\s+` + technicalZone)
var clockZoneLabel = regexp.MustCompile(`(?i)\s*hor[aá]rio de\s+` + technicalZone + `[.]?`)
var bareZone = regexp.MustCompile(`(?:^|[ \t])` + technicalZone + `\b`)

// Presentation only: dates were converted with Policy.Zone before reaching
// here. Keep the stored timezone and preserve URL paths containing zone names.
func CustomerMessage(text string) string {
	text = parenthesizedZone.ReplaceAllString(text, "")
	text = labeledZone.ReplaceAllString(text, "")
	text = clockZoneLabel.ReplaceAllString(text, "")
	text = bareZone.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}
