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
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

const missingRefRequeueAfter = 30 * time.Second

// ArkClusterReconciler reconciles a ArkCluster object.
type ArkClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile drives one pass of the ArkCluster control loop.
//
// The function never returns early on a substep failure: every substep records
// its outcome into a reconcileObservation, and the final status update reflects
// the aggregate state. Genuine controller errors (apply failures) are returned
// to trigger controller-runtime backoff; "missing referenced resource" cases
// are signalled via Result.RequeueAfter so the operator can recover quickly
// once the user applies the missing Secret or ConfigMap.
func (r *ArkClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster arkv1.ArkCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var obs reconcileObservation

	if err := r.reconcileConfigMaps(ctx, &cluster); err != nil {
		log.Error(err, "ConfigMap reconciliation")
		obs.configsErr = err
	}
	if err := r.reconcileSharedPVC(ctx, &cluster); err != nil {
		log.Error(err, "shared PVC reconciliation")
		obs.pvcApplyErr = err
	}
	obs.pvcBound, obs.pvcGetErr = r.probeSharedPVCBound(ctx, &cluster)
	if obs.pvcGetErr != nil {
		log.Info("shared PVC lookup", "error", obs.pvcGetErr.Error())
	}
	if err, keyMissing := r.validatePasswordsSecret(ctx, &cluster); err != nil {
		log.Info("passwords Secret validation", "error", err.Error())
		obs.secretErr = err
		obs.secretKeyMissing = keyMissing
	}
	if err, keyMissing := r.validateIniConfigMapRefs(ctx, &cluster); err != nil {
		log.Info("ini configMapRef validation", "error", err.Error())
		obs.refErr = err
		obs.refKeyMissing = keyMissing
	}
	if err := r.writeStatus(ctx, &cluster, obs); err != nil {
		log.Error(err, "status update")
		return ctrl.Result{}, err
	}

	if fatal := errors.Join(obs.configsErr, obs.pvcApplyErr); fatal != nil {
		return ctrl.Result{}, fatal
	}
	if obs.secretErr != nil || obs.refErr != nil || obs.pvcGetErr != nil || !obs.pvcBound {
		return ctrl.Result{RequeueAfter: missingRefRequeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileConfigMaps applies all four operator-managed ConfigMaps. Returns the
// first apply failure, or nil if every CM is present at the desired state.
func (r *ArkClusterReconciler) reconcileConfigMaps(ctx context.Context, cluster *arkv1.ArkCluster) error {
	log := logf.FromContext(ctx)
	desired := []*corev1.ConfigMap{
		buildArkManagerCfgConfigMap(cluster),
		buildGlobalGameIniConfigMap(cluster),
		buildGlobalGameUserSettingsIniConfigMap(cluster),
		buildPlayerListsConfigMap(cluster),
	}
	for _, cm := range desired {
		if cm == nil {
			continue
		}
		op, err := r.applyConfigMap(ctx, cluster, cm)
		if err != nil {
			return fmt.Errorf("apply ConfigMap %s: %w", cm.Name, err)
		}
		if op != controllerutil.OperationResultNone {
			log.Info("reconciled ConfigMap", "name", cm.Name, "operation", op)
		}
	}
	return nil
}

// reconcileSharedPVC creates / updates the operator-managed shared PVC. Returns
// nil immediately if spec.sharedStorage.existingClaimName is set (the operator
// does not touch user-owned PVCs).
func (r *ArkClusterReconciler) reconcileSharedPVC(ctx context.Context, cluster *arkv1.ArkCluster) error {
	log := logf.FromContext(ctx)
	pvc := buildSharedStoragePVC(cluster)
	if pvc == nil {
		return nil
	}
	op, err := r.applySharedPVC(ctx, cluster, pvc)
	if err != nil {
		return fmt.Errorf("apply shared PVC %s: %w", pvc.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("reconciled shared PVC", "name", pvc.Name, "operation", op)
	}
	return nil
}

// probeSharedPVCBound returns whether the cluster-travel PVC (operator-managed
// or user-supplied via existingClaimName) is in Bound phase.
func (r *ArkClusterReconciler) probeSharedPVCBound(ctx context.Context, cluster *arkv1.ArkCluster) (bool, error) {
	name := SharedStoragePVCName(cluster)
	if cn := cluster.Spec.SharedStorage.ExistingClaimName; cn != "" {
		name = cn
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, pvc); err != nil {
		return false, err
	}
	return pvc.Status.Phase == corev1.ClaimBound, nil
}

// validatePasswordsSecret checks that the Secret referenced by
// spec.passwords.secretRef exists and carries both expected data keys.
//
// Returns (nil, false) on success. On failure returns the error plus a flag
// distinguishing "secret not found" from "key inside secret missing" so the
// caller can pick a precise condition Reason.
func (r *ArkClusterReconciler) validatePasswordsSecret(ctx context.Context, cluster *arkv1.ArkCluster) (error, bool) {
	ref := cluster.Spec.Passwords.SecretRef
	if ref.Name == "" {
		return fmt.Errorf("spec.passwords.secretRef.name is empty"), false
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: cluster.Namespace}, secret); err != nil {
		return fmt.Errorf("get Secret %s: %w", ref.Name, err), false
	}

	serverKey := ref.Keys.ServerPassword
	if serverKey == "" {
		serverKey = "serverPass"
	}
	adminKey := ref.Keys.AdminPassword
	if adminKey == "" {
		adminKey = "adminPass"
	}
	for _, key := range []string{serverKey, adminKey} {
		if _, ok := secret.Data[key]; !ok {
			return fmt.Errorf("Secret %s is missing data key %q", ref.Name, key), true
		}
	}
	return nil, false
}

// validateIniConfigMapRefs confirms that every IniSource.ConfigMapRef on the
// spec resolves to a ConfigMap whose data carries the requested key.
//
// Returns (nil, false) on success. On failure the second return value flags
// whether the failure was a missing-key (true) versus a missing-ConfigMap (false).
func (r *ArkClusterReconciler) validateIniConfigMapRefs(ctx context.Context, cluster *arkv1.ArkCluster) (error, bool) {
	sources := []struct {
		field string
		ref   *arkv1.IniConfigMapRef
	}{
		{"spec.game.configMapRef", cluster.Spec.Game.ConfigMapRef},
		{"spec.gameUserSettings.configMapRef", cluster.Spec.GameUserSettings.ConfigMapRef},
	}
	for _, src := range sources {
		if src.ref == nil {
			continue
		}
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Name: src.ref.Name, Namespace: cluster.Namespace}, cm); err != nil {
			return fmt.Errorf("%s: get ConfigMap %s: %w", src.field, src.ref.Name, err), false
		}
		if _, ok := cm.Data[src.ref.Key]; !ok {
			return fmt.Errorf("%s: ConfigMap %s missing data key %q", src.field, src.ref.Name, src.ref.Key), true
		}
	}
	return nil, false
}

// applyConfigMap creates the ConfigMap or updates its data/labels to match the
// desired state.
func (r *ArkClusterReconciler) applyConfigMap(ctx context.Context, cluster *arkv1.ArkCluster, desired *corev1.ConfigMap) (controllerutil.OperationResult, error) {
	target := &corev1.ConfigMap{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	return controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		target.Data = desired.Data
		if err := controllerutil.SetControllerReference(cluster, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
}

// applySharedPVC creates the cluster-travel PVC or, for an existing one, reconciles
// only the fields K8s allows to mutate post-bind. AccessModes / StorageClassName /
// VolumeName are immutable after creation; Resources.Requests can be expanded if
// the bound StorageClass permits.
func (r *ArkClusterReconciler) applySharedPVC(ctx context.Context, cluster *arkv1.ArkCluster, desired *corev1.PersistentVolumeClaim) (controllerutil.OperationResult, error) {
	target := &corev1.PersistentVolumeClaim{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	return controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		if target.CreationTimestamp.IsZero() {
			target.Spec = desired.Spec
		} else {
			target.Spec.Resources = desired.Spec.Resources
		}
		if err := controllerutil.SetControllerReference(cluster, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
}

func (r *ArkClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&arkv1.ArkCluster{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Named("arkcluster").
		Complete(r)
}
