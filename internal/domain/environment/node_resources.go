package environment

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var byteSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)$`)

// ValidatePositiveByteSize accepts the byte-size units used by immutable Node
// runtime snapshots without making the environment domain depend on Incus.
func ValidatePositiveByteSize(value string) error {
	match := byteSizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return fmt.Errorf("must be a positive byte size")
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return fmt.Errorf("must be a positive byte size")
	}
	return nil
}
