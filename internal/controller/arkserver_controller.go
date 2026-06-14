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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

// ArkServerReconciler reconciles a ArkServer object.
//
// PR1 scope (Phase 1, Running path only):
//   - Resolves the parent ArkCluster (and any referenced ini ConfigMaps)
//   - Generates the rendered-config CM (arkmanager.cfg + merged inis + player lists)
//   - Creates / updates the per-map PVC, NodePort Service, and StatefulSet
//
// Deferred to later PRs: desiredState state machine (PR2), Wiped finalizer
// (PR3), status conditions and clusterRef immutability check (PR4),
// ArkCluster.status.managedServers count (PR5).
type ArkServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=ark.yadon3141.com,resources=arkclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one pass of the ArkServer control loop.
func (r *ArkServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	var server arkv1.ArkServer
	if err := r.Get(ctx, req.NamespacedName, &server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	parent, err := r.resolveParent(ctx, &server)
	if err != nil {
		// Missing parent or unreadable user-supplied CM: poll periodically
		// without exponential backoff so creation of the missing resource
		// is picked up within at most missingRefRequeueAfter.
		log.Info("resolve parent", "error", err.Error())
		return ctrl.Result{RequeueAfter: missingRefRequeueAfter}, nil
	}

	if err := r.applyRenderedConfigCM(ctx, &server, parent); err != nil {
		log.Error(err, "apply rendered-config CM")
		return ctrl.Result{}, err
	}
	if err := r.applyDataPVC(ctx, &server, parent); err != nil {
		log.Error(err, "apply data PVC")
		return ctrl.Result{}, err
	}
	if err := r.applyService(ctx, &server, parent); err != nil {
		log.Error(err, "apply Service")
		return ctrl.Result{}, err
	}
	if err := r.applyStatefulSet(ctx, &server, parent); err != nil {
		log.Error(err, "apply StatefulSet")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ArkServerReconciler) applyRenderedConfigCM(ctx context.Context, server *arkv1.ArkServer, parent *resolvedParent) error {
	desired := buildRenderedConfigCM(server, parent)
	target := &corev1.ConfigMap{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		target.Data = desired.Data
		if err := controllerutil.SetControllerReference(server, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
	return err
}

// applyDataPVC creates the per-map PVC or, for an existing one, reconciles
// only the size request (the only PVC spec field K8s allows to mutate post-bind).
func (r *ArkServerReconciler) applyDataPVC(ctx context.Context, server *arkv1.ArkServer, parent *resolvedParent) error {
	desired := buildDataPVC(server, parent)
	target := &corev1.PersistentVolumeClaim{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		if target.CreationTimestamp.IsZero() {
			target.Spec = desired.Spec
		} else {
			target.Spec.Resources = desired.Spec.Resources
		}
		if err := controllerutil.SetControllerReference(server, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
	return err
}

// applyService creates the Service or refreshes mutable fields. ClusterIP is
// immutable post-creation so it is preserved across updates.
func (r *ArkServerReconciler) applyService(ctx context.Context, server *arkv1.ArkServer, parent *resolvedParent) error {
	desired := buildService(server, parent)
	target := &corev1.Service{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		if target.CreationTimestamp.IsZero() {
			target.Spec = desired.Spec
		} else {
			clusterIP := target.Spec.ClusterIP
			clusterIPs := target.Spec.ClusterIPs
			target.Spec = desired.Spec
			target.Spec.ClusterIP = clusterIP
			target.Spec.ClusterIPs = clusterIPs
		}
		if err := controllerutil.SetControllerReference(server, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
	return err
}

// applyStatefulSet creates the StatefulSet or, for an existing one, refreshes
// the fields K8s allows to mutate post-creation (replicas, template, update
// strategy). selector and serviceName are immutable.
func (r *ArkServerReconciler) applyStatefulSet(ctx context.Context, server *arkv1.ArkServer, parent *resolvedParent) error {
	desired := buildStatefulSet(server, parent)
	target := &appsv1.StatefulSet{}
	target.Name = desired.Name
	target.Namespace = desired.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = desired.Labels
		if target.CreationTimestamp.IsZero() {
			target.Spec = desired.Spec
		} else {
			target.Spec.Replicas = desired.Spec.Replicas
			target.Spec.Template = desired.Spec.Template
			target.Spec.UpdateStrategy = desired.Spec.UpdateStrategy
			target.Spec.MinReadySeconds = desired.Spec.MinReadySeconds
		}
		if err := controllerutil.SetControllerReference(server, target, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on %s: %w", desired.Name, err)
		}
		return nil
	})
	return err
}

func (r *ArkServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&arkv1.ArkServer{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Named("arkserver").
		Complete(r)
}
