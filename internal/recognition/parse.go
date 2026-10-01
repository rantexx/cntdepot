package recognition

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var fence = regexp.MustCompile("(?i)```(?:json)?")

// decodeJSON recovers one JSON object from engine text into v. Structured
// output makes clean JSON the norm, but a good read should not be thrown away
// over a code fence or a stray word around it.
func decodeJSON(text string, v any) error {
	cleaned := strings.TrimSpace(fence.ReplaceAllString(text, ""))
	if err := json.Unmarshal([]byte(cleaned), v); err == nil {
		return nil
	}
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), v); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %.80q", ErrUnparseable, text)
}
