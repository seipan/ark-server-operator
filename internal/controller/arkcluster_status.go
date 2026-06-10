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

// Condition types stamped on ArkCluster.Status.Conditions.
// The set matches the design doc (§3 "status shape"):
//   Ready              — overall readiness
//   ConfigsApplied     — operator-managed ConfigMaps reconciled to desired state
//   SharedStorageBound — cluster-travel PVC is Bound
const (
	ConditionReady              = "Ready"
	ConditionConfigsApplied     = "ConfigsApplied"
	ConditionSharedStorageBound = "SharedStorageBound"
)

// Condition reasons surfaced in kubectl describe and events.
const (
	ReasonReconciled                = "Reconciled"
	ReasonReconcileFailed           = "ReconcileFailed"
	ReasonReferencedCMNotFound      = "ReferencedConfigMapNotFound"
	ReasonReferencedCMKeyMissing    = "ReferencedConfigMapKeyMissing"
	ReasonPasswordsSecretNotFound   = "PasswordsSecretNotFound"
	ReasonPasswordsSecretKeyMissing = "PasswordsSecretKeyMissing"
	ReasonSharedStorageNotFound     = "SharedStorageNotFound"
	ReasonSharedStoragePending      = "SharedStoragePending"
	ReasonAllSubresourcesReady      = "AllSubresourcesReady"
	ReasonSubresourcesNotReady      = "SubresourcesNotReady"
)

// reconcileObservation aggregates the outcome of each reconcile substep so the
// final status update has a single source of truth.
type reconcileObservation struct {
	// configsErr is non-nil if any operator-managed ConfigMap failed to apply.
	configsErr error
	// pvcApplyErr is non-nil if the shared PVC create/update failed.
	pvcApplyErr error
	// pvcBound reports whether the cluster-travel PVC (operator-managed or
	// existing) is in Bound phase.
	pvcBound bool
	// pvcGetErr is non-nil if the bound-status probe failed (e.g. an
	// existingClaimName points at a missing PVC).
	pvcGetErr error
	// secretErr is non-nil when the passwords Secret is missing or lacks a
	// required key. Not fatal; surfaced in conditions for operator follow-up.
	secretErr error
	// secretKeyMissing distinguishes "secret not found" from "secret found,
	// key inside it missing" for a more precise condition reason.
	secretKeyMissing bool
	// refErr is non-nil when an inline-replacing configMapRef is missing or
	// lacks the requested key.
	refErr error
	// refKeyMissing distinguishes "ConfigMap not found" from "ConfigMap found,
	// key missing".
	refKeyMissing bool
}

// setCondition is a thin wrapper around meta.SetStatusCondition that stamps the
// generation observed at write time so consumers can tell whether the status
// reflects the current spec.
func setCondition(c *arkv1.ArkCluster, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: c.Generation,
	})
}

// writeStatus computes Conditions + scalar status fields from the substep
// observation and persists the result via the /status subresource.
func (r *ArkClusterReconciler) writeStatus(ctx context.Context, c *arkv1.ArkCluster, obs reconcileObservation) error {
	now := metav1.Now()
	c.Status.LastReconcileTime = &now
	c.Status.ImageDigest = c.Spec.Image.Digest

	// ConfigsApplied: True only if both the apply step and the referenced
	// configMapRef validation succeeded.
	switch {
	case obs.configsErr != nil:
		setCondition(c, ConditionConfigsApplied, metav1.ConditionFalse,
			ReasonReconcileFailed, obs.configsErr.Error())
		c.Status.GlobalConfigMapsReady = false
	case obs.refErr != nil:
		reason := ReasonReferencedCMNotFound
		if obs.refKeyMissing {
			reason = ReasonReferencedCMKeyMissing
		}
		setCondition(c, ConditionConfigsApplied, metav1.ConditionFalse,
			reason, obs.refErr.Error())
		c.Status.GlobalConfigMapsReady = false
	default:
		setCondition(c, ConditionConfigsApplied, metav1.ConditionTrue,
			ReasonReconciled, "All operator-managed ConfigMaps applied and references resolved")
		c.Status.GlobalConfigMapsReady = true
	}

	// SharedStorageBound: True only when the PVC is observable AND Bound.
	switch {
	case obs.pvcApplyErr != nil:
		setCondition(c, ConditionSharedStorageBound, metav1.ConditionFalse,
			ReasonReconcileFailed, obs.pvcApplyErr.Error())
		c.Status.SharedStorageReady = false
	case obs.pvcGetErr != nil:
		setCondition(c, ConditionSharedStorageBound, metav1.ConditionFalse,
			ReasonSharedStorageNotFound, obs.pvcGetErr.Error())
		c.Status.SharedStorageReady = false
	case !obs.pvcBound:
		setCondition(c, ConditionSharedStorageBound, metav1.ConditionFalse,
			ReasonSharedStoragePending, "PVC is not yet Bound")
		c.Status.SharedStorageReady = false
	default:
		setCondition(c, ConditionSharedStorageBound, metav1.ConditionTrue,
			ReasonReconciled, "Shared PVC is Bound")
		c.Status.SharedStorageReady = true
	}

	// Ready: aggregate. False if any substep is not happy.
	ready := obs.configsErr == nil &&
		obs.pvcApplyErr == nil && obs.pvcGetErr == nil && obs.pvcBound &&
		obs.secretErr == nil &&
		obs.refErr == nil
	if ready {
		setCondition(c, ConditionReady, metav1.ConditionTrue,
			ReasonAllSubresourcesReady, "All subresources reconciled successfully")
	} else {
		setCondition(c, ConditionReady, metav1.ConditionFalse,
			ReasonSubresourcesNotReady, buildNotReadyMessage(obs))
	}

	return r.Status().Update(ctx, c)
}

// buildNotReadyMessage assembles a human-readable explanation listing every
// substep that is not yet satisfied. The Ready condition surfaces it.
func buildNotReadyMessage(obs reconcileObservation) string {
	var msg string
	add := func(s string) {
		if msg == "" {
			msg = s
			return
		}
		msg += "; " + s
	}
	if obs.configsErr != nil {
		add(fmt.Sprintf("ConfigMaps: %s", obs.configsErr))
	}
	if obs.pvcApplyErr != nil {
		add(fmt.Sprintf("shared PVC apply: %s", obs.pvcApplyErr))
	}
	if obs.pvcGetErr != nil {
		add(fmt.Sprintf("shared PVC lookup: %s", obs.pvcGetErr))
	} else if !obs.pvcBound {
		add("shared PVC pending bind")
	}
	if obs.secretErr != nil {
		add(fmt.Sprintf("passwords Secret: %s", obs.secretErr))
	}
	if obs.refErr != nil {
		add(fmt.Sprintf("ini configMapRef: %s", obs.refErr))
	}
	if msg == "" {
		return "not ready"
	}
	return msg
}
