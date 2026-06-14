/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

const (
	ArkServerConditionReady            = "Ready"
	ArkServerConditionClusterRefValid  = "ClusterRefValid"
	ArkServerConditionStatefulSetReady = "StatefulSetReady"
	ArkServerConditionPVCReady         = "PVCReady"
)

const (
	ArkServerReasonReconciled                = "Reconciled"
	ArkServerReasonReconcileFailed           = "ReconcileFailed"
	ArkServerReasonAllSubresourcesReady      = "AllSubresourcesReady"
	ArkServerReasonSubresourcesNotReady      = "SubresourcesNotReady"
	ArkServerReasonClusterRefResolved        = "ClusterRefResolved"
	ArkServerReasonClusterRefNotFound        = "ClusterRefNotFound"
	ArkServerReasonClusterRefChangeForbidden = "ClusterRefChangeForbidden"
	ArkServerReasonPodRunning                = "PodRunning"
	ArkServerReasonPodPending                = "PodPending"
	ArkServerReasonScaledToZero              = "ScaledToZero"
	ArkServerReasonStatefulSetMissing        = "StatefulSetMissing"
	ArkServerReasonPVCBound                  = "PVCBound"
	ArkServerReasonPVCPending                = "PVCPending"
	ArkServerReasonPVCMissing                = "PVCMissing"
)

// arkServerObservation aggregates the per-Reconcile substep results that feed
// into status. clusterRefForbidden is a soft-immutability marker derived from
// status.clusterName cached on the first successful reconcile.
type arkServerObservation struct {
	clusterRefErr       error
	clusterRefForbidden bool
	forbiddenMsg        string

	configsErr  error
	pvcApplyErr error
	svcApplyErr error
	stsApplyErr error

	desiredReplicas int32
	podName         string

	stsReady  bool
	stsGetErr error

	pvcBound  bool
	pvcGetErr error
}

func setArkServerCondition(s *arkv1.ArkServer, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: s.Generation,
	})
}

// resolveArkServerPhase maps the observation into a single Phase token for
// kubectl-friendly visibility. desiredState is consulted first: Stopped /
// Hibernated produce Stopped regardless of Pod readiness, since the absence
// of a running Pod is the intent rather than an error.
func resolveArkServerPhase(s *arkv1.ArkServer, obs arkServerObservation) arkv1.ArkServerPhase {
	if obs.clusterRefForbidden || obs.clusterRefErr != nil {
		return arkv1.PhaseFailed
	}
	if obs.configsErr != nil || obs.pvcApplyErr != nil || obs.svcApplyErr != nil || obs.stsApplyErr != nil {
		return arkv1.PhaseFailed
	}
	switch s.Spec.DesiredState {
	case arkv1.StateStopped, arkv1.StateHibernated:
		return arkv1.PhaseStopped
	}
	if obs.stsReady && obs.pvcBound {
		return arkv1.PhaseRunning
	}
	return arkv1.PhaseProvisioning
}

// writeArkServerStatus computes scalar status fields and Conditions from the
// observation and persists the result via the /status subresource.
//
// parent may be nil when resolution failed; the cached status.clusterName is
// preserved so the immutability check on the next reconcile still has the
// reference value.
func (r *ArkServerReconciler) writeArkServerStatus(ctx context.Context, server *arkv1.ArkServer, parent *resolvedParent, obs arkServerObservation) error {
	now := metav1.Now()
	server.Status.LastReconcileTime = &now
	server.Status.PodName = obs.podName
	server.Status.StatefulSetReady = obs.stsReady
	server.Status.PVCReady = obs.pvcBound

	if parent != nil {
		server.Status.ClusterName = parent.cluster.Name
	}

	server.Status.Phase = resolveArkServerPhase(server, obs)

	switch {
	case obs.clusterRefForbidden:
		setArkServerCondition(server, ArkServerConditionClusterRefValid, metav1.ConditionFalse,
			ArkServerReasonClusterRefChangeForbidden, obs.forbiddenMsg)
	case obs.clusterRefErr != nil:
		setArkServerCondition(server, ArkServerConditionClusterRefValid, metav1.ConditionFalse,
			ArkServerReasonClusterRefNotFound, obs.clusterRefErr.Error())
	default:
		setArkServerCondition(server, ArkServerConditionClusterRefValid, metav1.ConditionTrue,
			ArkServerReasonClusterRefResolved, "Parent ArkCluster is reachable and unchanged")
	}

	switch {
	case obs.stsApplyErr != nil:
		setArkServerCondition(server, ArkServerConditionStatefulSetReady, metav1.ConditionFalse,
			ArkServerReasonReconcileFailed, obs.stsApplyErr.Error())
	case obs.desiredReplicas == 0:
		setArkServerCondition(server, ArkServerConditionStatefulSetReady, metav1.ConditionFalse,
			ArkServerReasonScaledToZero, "StatefulSet intentionally scaled to 0 by desiredState")
	case obs.stsGetErr != nil:
		setArkServerCondition(server, ArkServerConditionStatefulSetReady, metav1.ConditionFalse,
			ArkServerReasonStatefulSetMissing, obs.stsGetErr.Error())
	case !obs.stsReady:
		setArkServerCondition(server, ArkServerConditionStatefulSetReady, metav1.ConditionFalse,
			ArkServerReasonPodPending, "Pod is not yet ready")
	default:
		setArkServerCondition(server, ArkServerConditionStatefulSetReady, metav1.ConditionTrue,
			ArkServerReasonPodRunning, "Pod is Running with all containers ready")
	}

	switch {
	case obs.pvcApplyErr != nil:
		setArkServerCondition(server, ArkServerConditionPVCReady, metav1.ConditionFalse,
			ArkServerReasonReconcileFailed, obs.pvcApplyErr.Error())
	case obs.pvcGetErr != nil:
		setArkServerCondition(server, ArkServerConditionPVCReady, metav1.ConditionFalse,
			ArkServerReasonPVCMissing, obs.pvcGetErr.Error())
	case !obs.pvcBound:
		setArkServerCondition(server, ArkServerConditionPVCReady, metav1.ConditionFalse,
			ArkServerReasonPVCPending, "PVC is not yet Bound")
	default:
		setArkServerCondition(server, ArkServerConditionPVCReady, metav1.ConditionTrue,
			ArkServerReasonPVCBound, "PVC is Bound")
	}

	ready := !obs.clusterRefForbidden &&
		obs.clusterRefErr == nil &&
		obs.configsErr == nil && obs.pvcApplyErr == nil &&
		obs.svcApplyErr == nil && obs.stsApplyErr == nil &&
		obs.stsReady && obs.pvcBound
	if ready {
		setArkServerCondition(server, ArkServerConditionReady, metav1.ConditionTrue,
			ArkServerReasonAllSubresourcesReady, "All subresources reconciled and ready")
	} else {
		setArkServerCondition(server, ArkServerConditionReady, metav1.ConditionFalse,
			ArkServerReasonSubresourcesNotReady, buildArkServerNotReadyMessage(obs))
	}

	return r.Status().Update(ctx, server)
}

func buildArkServerNotReadyMessage(obs arkServerObservation) string {
	var msg string
	add := func(s string) {
		if msg == "" {
			msg = s
			return
		}
		msg += "; " + s
	}
	if obs.clusterRefForbidden {
		add(obs.forbiddenMsg)
	}
	if obs.clusterRefErr != nil {
		add(fmt.Sprintf("clusterRef: %s", obs.clusterRefErr))
	}
	if obs.configsErr != nil {
		add(fmt.Sprintf("rendered-config: %s", obs.configsErr))
	}
	if obs.pvcApplyErr != nil {
		add(fmt.Sprintf("data PVC apply: %s", obs.pvcApplyErr))
	}
	if obs.svcApplyErr != nil {
		add(fmt.Sprintf("Service apply: %s", obs.svcApplyErr))
	}
	if obs.stsApplyErr != nil {
		add(fmt.Sprintf("StatefulSet apply: %s", obs.stsApplyErr))
	}
	if obs.desiredReplicas == 0 {
		add("StatefulSet scaled to zero by desiredState")
	} else if !obs.stsReady {
		if obs.stsGetErr != nil {
			add(fmt.Sprintf("StatefulSet: %s", obs.stsGetErr))
		} else {
			add("Pod not yet ready")
		}
	}
	if !obs.pvcBound {
		if obs.pvcGetErr != nil {
			add(fmt.Sprintf("data PVC: %s", obs.pvcGetErr))
		} else {
			add("data PVC pending bind")
		}
	}
	if msg == "" {
		return "not ready"
	}
	return msg
}
