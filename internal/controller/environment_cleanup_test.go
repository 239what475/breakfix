package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCleanupStaleEnvironmentsRetainsNamespacesForUnprovisionedEnvironments(t *testing.T) {
	const runtimeNamespace = "breakfix"
	const crdNamespace = "breakfix-system"
	const userRef = "u-pending"
	containerNamespace := k8s.EnvironmentNamespace(runtimeNamespace, userRef, "pending-container")
	vclusterNamespace := k8s.EnvironmentNamespace(runtimeNamespace, userRef, "pending-vcluster")
	deleted := map[string]bool{}

	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/breakfix.dev/v1/namespaces/"+crdNamespace+"/containerenvironments":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&breakfixv1.ContainerEnvironmentList{
				TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "ContainerEnvironmentList"},
				Items: []breakfixv1.ContainerEnvironment{{
					ObjectMeta: metav1.ObjectMeta{Name: "pending-container", Namespace: crdNamespace},
					Spec:       breakfixv1.CommonEnvironmentSpec{UserRef: userRef},
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/breakfix.dev/v1/namespaces/"+crdNamespace+"/vclusterenvironments":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&breakfixv1.VClusterEnvironmentList{
				TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VClusterEnvironmentList"},
				Items: []breakfixv1.VClusterEnvironment{{
					ObjectMeta: metav1.ObjectMeta{Name: "pending-vcluster", Namespace: crdNamespace},
					Spec:       breakfixv1.VClusterEnvironmentSpec{CommonEnvironmentSpec: breakfixv1.CommonEnvironmentSpec{UserRef: userRef}},
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&corev1.NamespaceList{
				Items: []corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: containerNamespace}}, {ObjectMeta: metav1.ObjectMeta{Name: vclusterNamespace}}},
			})
		case r.Method == http.MethodDelete && (r.URL.Path == "/api/v1/namespaces/"+containerNamespace || r.URL.Path == "/api/v1/namespaces/"+vclusterNamespace):
			deleted[r.URL.Path] = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	if err := cleanupStaleEnvironments(context.Background(), client, runtimeNamespace, crdNamespace); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("cleanup deleted namespaces owned by unprovisioned environments: %#v", deleted)
	}
}

func TestStaleCommonEnvironmentDoesNotTreatPendingPodAsStale(t *testing.T) {
	t.Helper()

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workspace",
			Namespace: "demo",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "challenge",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "ContainerCreating",
						},
					},
				},
			},
		},
	}

	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/demo/pods/workspace" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(pod); err != nil {
			t.Fatalf("encode pod: %v", err)
		}
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentProvisioning,
		Namespace:        "demo",
		WorkspacePodName: "workspace",
	}

	if staleCommonEnvironment(client, status) {
		t.Fatalf("expected provisioning pod in ContainerCreating to remain active, but it was marked stale")
	}
}

func TestStaleCommonEnvironmentDoesNotTreatProvisioningVClusterWithoutWorkspacePodAsStale(t *testing.T) {
	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentProvisioning,
		Namespace:        "demo",
		WorkspacePodName: "",
	}

	if staleCommonEnvironment(client, status) {
		t.Fatalf("expected provisioning vcluster environment without workspace pod to remain non-stale")
	}
}

func TestStaleCommonEnvironmentDoesNotTreatCompletedEnvironmentAsStale(t *testing.T) {
	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentCompleted,
		Namespace:        "demo",
		WorkspacePodName: "workspace",
	}

	if staleCommonEnvironment(client, status) {
		t.Fatalf("expected completed environment to remain non-stale")
	}
}

func TestStaleCommonEnvironmentTreatsMissingPodAsStale(t *testing.T) {
	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentProvisioning,
		Namespace:        "demo",
		WorkspacePodName: "workspace",
	}

	if !staleCommonEnvironment(client, status) {
		t.Fatalf("expected missing pod to be marked stale")
	}
}

func TestStaleCommonEnvironmentDoesNotTreatTransientPodReadErrorAsStale(t *testing.T) {
	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary apiserver failure", http.StatusInternalServerError)
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentProvisioning,
		Namespace:        "demo",
		WorkspacePodName: "workspace",
	}

	if staleCommonEnvironment(client, status) {
		t.Fatalf("expected transient pod read failure to remain non-stale")
	}
}

func TestStaleCommonEnvironmentTreatsCrashLoopBackOffAsStale(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workspace",
			Namespace: "demo",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "challenge",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}

	client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/demo/pods/workspace" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(pod); err != nil {
			t.Fatalf("encode pod: %v", err)
		}
	})

	status := &breakfixv1.CommonEnvironmentStatus{
		Phase:            breakfixv1.EnvironmentProvisioning,
		Namespace:        "demo",
		WorkspacePodName: "workspace",
	}

	if !staleCommonEnvironment(client, status) {
		t.Fatalf("expected CrashLoopBackOff pod to be marked stale")
	}
}

func newTestK8sClient(t *testing.T, handler http.HandlerFunc) *k8s.Client {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	kubeconfigPath := filepath.Join(dir, "config")
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`, server.URL)
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0644); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	client, err := k8s.New(kubeconfigPath)
	if err != nil {
		t.Fatalf("new k8s client: %v", err)
	}
	return client
}
