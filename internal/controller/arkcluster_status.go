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
	ConditionReady              = "Ready"
	ConditionConfigsApplied     = "ConfigsApplied"
	ConditionSharedStorageBound = "SharedStorageBound"
)

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

type reconcileObservation struct {
	pvcApplyErr      error
	pvcBound         bool
	pvcGetErr        error
	secretErr        error
	secretKeyMissing bool
	refErr           error
	refKeyMissing    bool
}

func setCondition(c *arkv1.ArkCluster, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: c.Generation,
	})
}

func (r *ArkClusterReconciler) writeStatus(ctx context.Context, c *arkv1.ArkCluster, obs reconcileObservation) error {
	now := metav1.Now()
	c.Status.LastReconcileTime = &now
	c.Status.ImageDigest = c.Spec.Image.Digest

	if obs.refErr != nil {
		reason := ReasonReferencedCMNotFound
		if obs.refKeyMissing {
			reason = ReasonReferencedCMKeyMissing
		}
		setCondition(c, ConditionConfigsApplied, metav1.ConditionFalse, reason, obs.refErr.Error())
		c.Status.GlobalConfigMapsReady = false
	} else {
		setCondition(c, ConditionConfigsApplied, metav1.ConditionTrue,
			ReasonReconciled, "Referenced ConfigMaps resolved")
		c.Status.GlobalConfigMapsReady = true
	}

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

	ready := obs.pvcApplyErr == nil && obs.pvcGetErr == nil && obs.pvcBound &&
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

func buildNotReadyMessage(obs reconcileObservation) string {
	var msg string
	add := func(s string) {
		if msg == "" {
			msg = s
			return
		}
		msg += "; " + s
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
