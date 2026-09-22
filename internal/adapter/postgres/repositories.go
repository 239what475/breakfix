package postgres

// Every repository is scoped to a durable domain. They intentionally share a
// PostgreSQL connection so cross-domain transitions can remain transactional
// without reintroducing a catch-all database API.
type AgentRepository struct{ conn *Conn }

type AuthoringRepository struct{ conn *Conn }

type CatalogRepository struct{ conn *Conn }

type DocumentationLinkRepository struct{ conn *Conn }

type ScenarioRepository struct{ conn *Conn }

type EnvironmentRepository struct{ conn *Conn }

type GenerationRepository struct{ conn *Conn }

type IdentityRepository struct{ conn *Conn }

type ReportingRepository struct{ conn *Conn }

type RunnableRepository struct{ conn *Conn }

type HumanActionRepository struct{ conn *Conn }
