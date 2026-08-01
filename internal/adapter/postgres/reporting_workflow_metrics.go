package postgres

import (
	"context"
	"fmt"
)

// WorkflowStateCounts exposes only the two durable workflow aggregates. It is
// deliberately separate from scheduler internals so metrics cannot recreate a
// generic work-item model.
type WorkflowStateCounts struct {
	Generation map[string]int64
	Taxonomy   map[string]int64
}

func (d *ReportingRepository) WorkflowStateCounts(ctx context.Context) (WorkflowStateCounts, error) {
	result := WorkflowStateCounts{Generation: map[string]int64{}, Taxonomy: map[string]int64{}}
	for _, target := range []struct {
		statement string
		values    map[string]int64
	}{
		{`SELECT state, COUNT(*) FROM generation_workflows GROUP BY state`, result.Generation},
		{`SELECT state, COUNT(*) FROM taxonomy_workflows GROUP BY state`, result.Taxonomy},
	} {
		rows, err := d.conn.QueryContext(ctx, target.statement)
		if err != nil {
			return WorkflowStateCounts{}, fmt.Errorf("query workflow state counts: %w", err)
		}
		for rows.Next() {
			var state string
			var count int64
			if err := rows.Scan(&state, &count); err != nil {
				_ = rows.Close()
				return WorkflowStateCounts{}, fmt.Errorf("scan workflow state count: %w", err)
			}
			target.values[state] = count
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return WorkflowStateCounts{}, fmt.Errorf("iterate workflow state counts: %w", err)
		}
		if err := rows.Close(); err != nil {
			return WorkflowStateCounts{}, fmt.Errorf("close workflow state counts: %w", err)
		}
	}
	return result, nil
}
