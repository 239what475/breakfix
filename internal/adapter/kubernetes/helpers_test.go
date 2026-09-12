package kubernetes

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestEnvironmentNamespaceUsesCleanupPrefixAndStableBoundedName(t *testing.T) {
	if got, want := EnvironmentNamespace("breakfix", "user_one", "environment-one"), "breakfix-u-user-one-environment-one"; got != want {
		t.Fatalf("EnvironmentNamespace() = %q, want %q", got, want)
	}

	userID := strings.Repeat("author-", 16)
	environmentID := strings.Repeat("environment-", 16)
	first := EnvironmentNamespace("breakfix", userID, environmentID)
	second := EnvironmentNamespace("breakfix", userID, environmentID)
	changed := EnvironmentNamespace("breakfix", userID, environmentID+"next")
	if first != second {
		t.Fatalf("EnvironmentNamespace() is not deterministic: %q != %q", first, second)
	}
	if first == changed {
		t.Fatalf("EnvironmentNamespace() collided after input changed: %q", first)
	}
	if len(first) > 63 {
		t.Fatalf("EnvironmentNamespace() length = %d, want <= 63: %q", len(first), first)
	}
	if errs := validation.IsDNS1123Label(first); len(errs) != 0 {
		t.Fatalf("EnvironmentNamespace() returned invalid DNS label %q: %v", first, errs)
	}
	if !strings.HasPrefix(first, "breakfix-u-") {
		t.Fatalf("EnvironmentNamespace() = %q, want breakfix-u prefix", first)
	}
}

func TestDNSLabelNameBoundsResourceNames(t *testing.T) {
	name := DNSLabelName("scenario", strings.Repeat("verify-env-", 12))
	if len(name) > 63 {
		t.Fatalf("DNSLabelName() length = %d, want <= 63: %q", len(name), name)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) != 0 {
		t.Fatalf("DNSLabelName() returned invalid DNS label %q: %v", name, errs)
	}
}

func TestDNSLabelNameWithLimitKeepsLongNamesDistinct(t *testing.T) {
	first := DNSLabelNameWithLimit(52, "vc", strings.Repeat("verify-env-", 12))
	second := DNSLabelNameWithLimit(52, "vc", strings.Repeat("verify-env-", 11)+"other")
	if len(first) > 52 {
		t.Fatalf("DNSLabelNameWithLimit() length = %d, want <= 52: %q", len(first), first)
	}
	if first == second {
		t.Fatalf("DNSLabelNameWithLimit() collided: %q", first)
	}
	if errs := validation.IsDNS1123Label(first); len(errs) != 0 {
		t.Fatalf("DNSLabelNameWithLimit() returned invalid DNS label %q: %v", first, errs)
	}
}

func TestIsNotFoundRecognizesConcurrentDelete(t *testing.T) {
	err := apierrors.NewNotFound(schema.GroupResource{Group: "breakfix.dev", Resource: "nodeenvironments"}, "example")
	if !isNotFound(err) {
		t.Fatal("a resource removed by another reconciler must be treated as not found")
	}
}
