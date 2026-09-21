package postgres

const currentSchemaVersion = 52

// currentSchemaStatements is the only database schema accepted by this
// development-only, intentionally destructive runtime migration. Do not add
// ALTER/DROP compatibility statements here: a previous schema must be reset.
var currentSchemaStatements = schemaStatements(
	schemaIdentityStatements,
	schemaAuthoringCoreStatements,
	schemaEnvironmentLearningStatements,
	schemaAgentStatements,
	schemaAuthoringRuntimeStatements,
	schemaGenerationWorkspaceStatements,
	schemaEnvironmentTerminalStatements,
	schemaRunnableStatements,
	schemaCatalogStatements,
	schemaGenerationStatements,
	schemaAuditStatements,
)

func schemaStatements(groups ...[]string) []string {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	statements := make([]string, 0, count)
	for _, group := range groups {
		statements = append(statements, group...)
	}
	return statements
}
