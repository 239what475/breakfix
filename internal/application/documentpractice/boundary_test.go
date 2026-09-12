package documentpractice

import "testing"

func TestPageLocationRequiresAnUpstreamIdentity(t *testing.T) {
	valid := PageLocation{Source: "kubernetes/website", Revision: "v1.34.0", Path: "content/zh-cn/docs/concepts/workloads/pods/pod-lifecycle.md", Anchor: "pod-lifecycle"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid page location: %v", err)
	}
	for _, value := range []PageLocation{
		{Revision: valid.Revision, Path: valid.Path},
		{Source: valid.Source, Path: valid.Path},
		{Source: valid.Source, Revision: valid.Revision},
		{Source: valid.Source, Revision: valid.Revision, Path: "../outside.md"},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid page location was accepted: %#v", value)
		}
	}
}
