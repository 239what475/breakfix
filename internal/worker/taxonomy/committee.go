package taxonomy

import (
	"context"
	"fmt"

	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
)

// einoCommittee is the production committee implementation. Workflow state,
// lease fencing, and retry decisions remain in Worker; this type only maps
// durable Server context to the model calls for the current state.
type einoCommittee struct {
	agent config.AgentConfig
}

func newEinoCommittee(agent config.AgentConfig) Committee {
	return einoCommittee{agent: agent}
}

func (c einoCommittee) Map(ctx context.Context, input taxonomyapp.Context) (taxonomy.ChangeSet, error) {
	if input.MapperValidation == nil {
		return taxonomy.ChangeSet{}, fmt.Errorf("taxonomy mapper has no deterministic validation context")
	}
	return runMapper(ctx, c.agent, input.Mapper, *input.MapperValidation)
}

func (c einoCommittee) Review(ctx context.Context, input taxonomyapp.Context) (taxonomy.Review, taxonomy.Review, error) {
	return runReviewPair(ctx, c.agent, input.CurriculumReview, input.SREReview)
}
