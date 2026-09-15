package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestListRuntimeEnvironmentsConvertsV2Items(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/apis/breakfix.dev/v2/namespaces/breakfix-system/runtimeenvironments" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(runtimev2.RuntimeEnvironmentList{
			TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironmentList"},
			Items: []runtimev2.RuntimeEnvironment{{
				ObjectMeta: metav1.ObjectMeta{Name: "environment-01"},
				Spec: runtimev2.RuntimeEnvironmentSpec{
					RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
					Purpose:             runtimev2.PurposeLearning,
					Lease:               runtimev2.LeaseSpec{RenewedAt: metav1.Now()},
				},
			}},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	kubeconfig := filepath.Join(root, "kubeconfig")
	contents := "apiVersion: v1\nclusters:\n- cluster:\n    server: " + server.URL + "\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n"
	if err := os.WriteFile(kubeconfig, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	items, err := client.ListRuntimeEnvironments(context.Background(), "breakfix-system", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 1 || items.Items[0].Name != "environment-01" || items.Items[0].Spec.RunnableRevisionRef.ID != "revision-01" {
		t.Fatalf("converted runtime environment list = %#v", items)
	}
}
