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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

// ArkClusterReconciler reconciles a ArkCluster object.
//
// Current scope (Phase 1 MVP, ConfigMap-only step):
//   - Renders arkmanager.cfg into a ConfigMap
//   - Materializes spec.game / spec.gameUserSettings inline contents into ConfigMaps
//     (configMapRef inputs are left for downstream consumers to reference directly)
//   - Renders spec.playerLists into a single ConfigMap with three text-file keys
//
// Out of scope for this step (tracked separately): shared PVC reconciliation,
// passwords Secret validation, status conditions, backup CronJob.
type ArkClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile renders the operator-managed ConfigMaps for the named ArkCluster.
func (r *ArkClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster arkv1.ArkCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	desired := []*corev1.ConfigMap{
		buildArkManagerCfgConfigMap(&cluster),
		buildGlobalGameIniConfigMap(&cluster),
		buildGlobalGameUserSettingsIniConfigMap(&cluster),
		buildPlayerListsConfigMap(&cluster),
	}
	for _, cm := range desired {
		if cm == nil {
			continue
		}
		op, err := r.applyConfigMap(ctx, &cluster, cm)
		if err != nil {
			log.Error(err, "reconcile ConfigMap", "name", cm.Name)
			return ctrl.Result{}, err
		}
		if op != controllerutil.OperationResultNone {
			log.Info("reconciled ConfigMap", "name", cm.Name, "operation", op)
		}
	}
	return ctrl.Result{}, nil
}

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

func (r *ArkClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&arkv1.ArkCluster{}).
		Owns(&corev1.ConfigMap{}).
		Named("arkcluster").
		Complete(r)
}
