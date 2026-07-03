package challenge

import (
	"fmt"
	"strings"
)

func DeriveID(seed string) string {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(seed)))
	var id string
	for _, w := range words {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			return -1
		}, w)
		if clean == "" {
			continue
		}
		if id != "" {
			id += "-"
		}
		id += clean
		if strings.Count(id, "-") >= 3 {
			break
		}
	}
	if id == "" {
		h := seedHash(seed)
		id = "challenge-" + h[:8]
	}
	if len(id) > 50 {
		id = id[:50]
	}
	return strings.Trim(id, "-")
}

func seedHash(seed string) string {
	var h [16]byte
	for i, c := range []byte(seed) {
		h[i%16] ^= c + byte(i)
	}
	return fmt.Sprintf("%x", h)
}
