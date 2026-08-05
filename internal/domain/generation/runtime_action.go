package generation

// PublicationFinalization is the Server-owned durable handoff after a Runtime
// Worker has promoted the final artifact. Its archive path remains private to
// Server and never crosses the Runtime Worker protocol.
type PublicationFinalization struct {
	Workflow  Workflow
	Candidate Revision
}
