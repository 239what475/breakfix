package runnable

import "time"

// RetryBackoffCap bounds the platform's shared patience curve: any retrying
// queue waits at most five minutes between attempts, so a stuck dependency
// costs roughly 288 attempts a day instead of a hot loop.
const RetryBackoffCap = 5 * time.Minute

// RetryBackoff returns the delay before the next attempt after failure number
// attempt: the base doubles every attempt and caps at RetryBackoffCap. Reap
// cleanup and runnable action retries share this shape — one curve, two
// consumers — so operator intuition transfers between the queue surfaces.
func RetryBackoff(base time.Duration, attempt int64) time.Duration {
	if base <= 0 {
		base = 5 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for range attempt - 1 {
		delay *= 2
		if delay >= RetryBackoffCap {
			return RetryBackoffCap
		}
	}
	return delay
}
