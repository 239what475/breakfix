package terminal

// Size is the provider-neutral terminal geometry carried from the browser to
// either Kubernetes exec or an Incus interactive exec control socket.
type Size struct {
	Width  uint16
	Height uint16
}
