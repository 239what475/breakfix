package assistant

import "context"

func testRequest(environmentUID string) Request {
	return Request{
		UserID:           "user-a",
		EnvironmentUID:   environmentUID,
		EnvironmentName:  "environment-a",
		Runtime:          "container",
		ChallengeID:      "cleanup-logs",
		ChallengeTitle:   "Cleanup logs",
		Problem:          "Repair the cleanup script.",
		CurrentWindow:    "shell-1",
		OpenWindows:      []string{"shell-1"},
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

func (r *fakeReader) TerminalScrollback(_ context.Context, window string, offset, _ int) (Scrollback, error) {
	r.scrollbackCalls++
	return Scrollback{Window: window, Offset: offset, Lines: []string{"$ ls", "broken"}, TotalLines: 2}, nil
}

func (r *fakeReader) CheckpointStatus(context.Context) (CheckpointSnapshot, error) {
	r.checkpointCalls++
	return CheckpointSnapshot{}, nil
}

func (r *fakeReader) ListEnvironmentFiles(_ context.Context, path string, offset, _ int) (EnvironmentFiles, error) {
	r.listCalls++
	return EnvironmentFiles{Path: path, Offset: offset}, nil
}

func (r *fakeReader) ReadEnvironmentFile(_ context.Context, path string, offset int64, _ int) (EnvironmentFile, error) {
	r.readCalls++
	return EnvironmentFile{Path: path, Offset: offset, Content: "kind: ConfigMap"}, nil
}

func (*fakeReader) Solution(context.Context) (string, error) { return "secret solution", nil }
