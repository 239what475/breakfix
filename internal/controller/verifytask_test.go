package controller

import (
	"context"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/verification"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValidateVerifyTaskSpec(t *testing.T) {
	valid := func() *breakfixv1.VerifyTask {
		return &breakfixv1.VerifyTask{Spec: breakfixv1.VerifyTaskSpec{
			Source:      breakfixv1.VerifyTaskSource{Ref: "run-abc123"},
			ChallengeID: "chal-abc123def456",
			Submission:  breakfixv1.VerifyTaskSubmission{ID: "sub-abc123"},
		}}
	}

	cases := []struct {
		name    string
		mutate  func(*breakfixv1.VerifyTask)
		wantErr bool
	}{
		{name: "agent artifact", mutate: func(*breakfixv1.VerifyTask) {}},
		{name: "missing challenge id", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.ChallengeID = "" }, wantErr: true},
		{name: "missing submission id", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Submission.ID = "" }, wantErr: true},
		{name: "missing source reference", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Source.Ref = "" }, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := valid()
			tc.mutate(task)
			err := validateVerifyTaskSpec(task)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVerifyTaskSpec() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestVerifierJobNameIsDeterministicAndDNSLengthBounded(t *testing.T) {
	name := "verify-task-with-a-name-that-is-longer-than-a-kubernetes-job-name-can-accept-without-truncation"
	first := verification.JobName(name)
	second := verification.JobName(name)
	if first != second {
		t.Fatalf("job name must be deterministic: %q != %q", first, second)
	}
	if len(first) > 63 {
		t.Fatalf("job name length = %d, want <= 63: %q", len(first), first)
	}
}

func TestVerifyTaskGetsCleanupFinalizerBeforeCreatingJob(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	task := &breakfixv1.VerifyTask{ObjectMeta: metav1.ObjectMeta{Name: "vt-finalizer", Namespace: "breakfix-system"}}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).Build()
	reconciler := &VerifyTaskReconciler{Client: fakeClient}
	key := types.NamespacedName{Name: task.Name, Namespace: task.Namespace}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var stored breakfixv1.VerifyTask
	if err := fakeClient.Get(context.Background(), key, &stored); err != nil {
		t.Fatal(err)
	}
	for _, finalizer := range stored.Finalizers {
		if finalizer == verifyTaskFinalizer {
			return
		}
	}
	t.Fatalf("finalizers = %v, want %q", stored.Finalizers, verifyTaskFinalizer)
}
