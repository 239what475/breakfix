package controller

const (
	// Container environments are lightweight enough to reconcile in parallel.
	containerEnvironmentMaxConcurrentReconciles = 4
	// VCluster reconciles do blocking waits on pod/file readiness, so they must not be fully serialized.
	vclusterEnvironmentMaxConcurrentReconciles = 2
)
