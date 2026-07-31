package assistant

import "context"

func testRequest(environmentUID string) Request {
	return Request{
		UserID:           "user-a",
		EnvironmentUID:   environmentUID,
		EnvironmentName:  "environment-a",
		Runtime:          "node",
		ChallengeID:      "cleanup-logs",
		ChallengeTitle:   "Cleanup logs",
		Problem:          "Repair the cleanup script.",
		Nodes:            []string{"host"},
		CurrentNode:      "host",
		CurrentWindow:    "shell-1",
		Terminals:        []TerminalContext{{Node: "host", Windows: []string{"shell-1"}}},
		EnvironmentPhase: "Ready",
		Checkpoints:      CheckpointSnapshot{Results: []CheckpointResult{{ID: "logs", Passed: false, Summary: "not complete"}}},
		Reader:           &fakeReader{},
	}
}

type fakeReader struct {
	scrollbackCalls int
	checkpointCalls int
	listCalls       int
	readCalls       int
}

func (r *fakeReader) TerminalScrollback(_ context.Context, node, window string, offset, _ int) (Scrollback, error) {
	r.scrollbackCalls++
	return Scrollback{Node: node, Window: window, Offset: offset, Lines: []string{"$ ls", "broken"}, TotalLines: 2}, nil
}

func (r *fakeReader) CheckpointStatus(context.Context) (CheckpointSnapshot, error) {
	r.checkpointCalls++
	return CheckpointSnapshot{}, nil
}

func (r *fakeReader) ListEnvironmentFiles(_ context.Context, node, path string, offset, _ int) (EnvironmentFiles, error) {
	r.listCalls++
	return EnvironmentFiles{Node: node, Path: path, Offset: offset}, nil
}

func (r *fakeReader) ReadEnvironmentFile(_ context.Context, node, path string, offset int64, _ int) (EnvironmentFile, error) {
	r.readCalls++
	return EnvironmentFile{Node: node, Path: path, Offset: offset, Content: "kind: ConfigMap"}, nil
}

func (*fakeReader) Solution(context.Context) (string, error) { return "secret solution", nil }
