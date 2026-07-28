package builder

import (
	"reflect"
	"testing"
)

func TestBuildArgsUseTaskScopedOCILayoutNamedContexts(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := buildArgs("/work/challenge", "/work/base", "breakfix-base", digest, "/work/image.oci.tar")
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/work/challenge",
		"--local", "dockerfile=/work/challenge",
		"--oci-layout", "trusted-base=/work/base",
		"--opt", "build-arg:BREAKFIX_BASE_IMAGE=breakfix-base",
		"--output", "type=oci,dest=/work/image.oci.tar",
		"--progress", "plain",
		"--opt", "context:breakfix-base=oci-layout://trusted-base@" + digest,
		"--opt", "context:breakfix-base:latest=oci-layout://trusted-base@" + digest,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("build args = %#v, want %#v", got, want)
	}
}
