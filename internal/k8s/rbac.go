package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const verificationWorkspaceRoleName = "breakfix-verification-workspace"

// EnsureVerificationWorkspaceExecAccess grants the Generate Worker only
// pods/exec access within one verification environment namespace. Verification
// environments are dynamically named, so this binding is created per namespace.
func (c *Client) EnsureVerificationWorkspaceExecAccess(namespace, serviceAccountNamespace, serviceAccountName string) error {
	namespace = strings.TrimSpace(namespace)
	serviceAccountNamespace = strings.TrimSpace(serviceAccountNamespace)
	serviceAccountName = strings.TrimSpace(serviceAccountName)
	if namespace == "" || serviceAccountNamespace == "" || serviceAccountName == "" {
		return fmt.Errorf("workspace namespace and verification service account are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	roles := c.clientset.RbacV1().Roles(namespace)
	if _, err := roles.Get(ctx, verificationWorkspaceRoleName, metav1.GetOptions{}); err != nil {
		if !k8sErrors.IsNotFound(err) {
			return fmt.Errorf("get verification workspace role: %w", err)
		}
		if _, err := roles.Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: verificationWorkspaceRoleName, Namespace: namespace},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create"},
			}},
		}, metav1.CreateOptions{}); err != nil && !k8sErrors.IsAlreadyExists(err) {
			return fmt.Errorf("create verification workspace role: %w", err)
		}
	}

	bindings := c.clientset.RbacV1().RoleBindings(namespace)
	if _, err := bindings.Get(ctx, verificationWorkspaceRoleName, metav1.GetOptions{}); err != nil {
		if !k8sErrors.IsNotFound(err) {
			return fmt.Errorf("get verification workspace role binding: %w", err)
		}
		if _, err := bindings.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: verificationWorkspaceRoleName, Namespace: namespace},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     verificationWorkspaceRoleName,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Namespace: serviceAccountNamespace,
				Name:      serviceAccountName,
			}},
		}, metav1.CreateOptions{}); err != nil && !k8sErrors.IsAlreadyExists(err) {
			return fmt.Errorf("create verification workspace role binding: %w", err)
		}
	}
	return nil
}
