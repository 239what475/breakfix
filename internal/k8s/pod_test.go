package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCreatePodAppliesImagePullSecrets(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}
	if err := client.CreatePod("challenge-user", "challenge-one", CreatePodOpts{
		Image:            "registry.breakfix.internal/breakfix/challenge:latest",
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "breakfix-registry-pull"}},
	}); err != nil {
		t.Fatal(err)
	}

	pod, err := client.clientset.CoreV1().Pods("challenge-user").Get(context.Background(), "challenge-one", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pod.Spec.ImagePullSecrets; len(got) != 1 || got[0].Name != "breakfix-registry-pull" {
		t.Fatalf("image pull secrets = %#v", got)
	}
}

func TestWaitForPodClassifiesCrashLoopAsTerminal(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "challenge-user"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "challenge",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CrashLoopBackOff",
					Message: "container keeps exiting",
				}},
			}},
		},
	})}

	err := client.WaitForPod("challenge-user", "broken", "challenge")
	if err == nil {
		t.Fatal("WaitForPod() succeeded for a CrashLoopBackOff pod")
	}
	if !IsTerminalPodReadinessError(err) {
		t.Fatalf("WaitForPod() error is not terminal: %v", err)
	}
}
