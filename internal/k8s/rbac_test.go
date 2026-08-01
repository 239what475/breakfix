package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureVerificationWorkspaceExecAccessIsNamespaceLocalAndIdempotent(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}
	for range 2 {
		if err := client.EnsureVerificationWorkspaceExecAccess("breakfix-u-example", "breakfix-system", "breakfix-generate-worker"); err != nil {
			t.Fatal(err)
		}
	}

	role, err := client.clientset.RbacV1().Roles("breakfix-u-example").Get(context.Background(), verificationWorkspaceRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].Resources) != 1 || role.Rules[0].Resources[0] != "pods/exec" || len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "create" {
		t.Fatalf("workspace role rules = %#v", role.Rules)
	}
	binding, err := client.clientset.RbacV1().RoleBindings("breakfix-u-example").Get(context.Background(), verificationWorkspaceRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != verificationWorkspaceRoleName || len(binding.Subjects) != 1 {
		t.Fatalf("workspace role binding = %#v", binding)
	}
	subject := binding.Subjects[0]
	if subject.Kind != "ServiceAccount" || subject.Namespace != "breakfix-system" || subject.Name != "breakfix-generate-worker" {
		t.Fatalf("workspace role binding subject = %#v", subject)
	}
}
