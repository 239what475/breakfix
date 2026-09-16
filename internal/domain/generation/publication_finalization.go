package generation

// PublicationFinalization is the Server-owned durable handoff after a
// candidate has immutable public runnable and verification references. Its
// archive path remains private to Server and never crosses the public Worker
// protocol.
type PublicationFinalization struct {
	Workflow  Workflow
	Candidate Revision
}
