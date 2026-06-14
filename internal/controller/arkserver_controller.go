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

	appsv1 "k8s.io/api/apps/v1"
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

func (r *ArkServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var server arkv1.ArkServer
	if err := r.Get(ctx, req.NamespacedName, &server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	obs := arkServerObservation{desiredReplicas: resolveReplicas(&server)}

	if cached := server.Status.ClusterName; cached != "" && cached != server.Spec.ClusterRef.Name {
		obs.clusterRefForbidden = true
		obs.forbiddenMsg = fmt.Sprintf("clusterRef cannot change after creation (was %q, now %q)",
			cached, server.Spec.ClusterRef.Name)
		log.Info("clusterRef change rejected", "was", cached, "now", server.Spec.ClusterRef.Name)
		if err := r.writeArkServerStatus(ctx, &server, nil, obs); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: missingRefRequeueAfter}, nil
	}

	parent, err := r.resolveParent(ctx, &server)
	if err != nil {
		log.Info("resolve parent", "error", err.Error())
		obs.clusterRefErr = err
		if statusErr := r.writeArkServerStatus(ctx, &server, nil, obs); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: missingRefRequeueAfter}, nil
	}

	if err := r.applyRenderedConfigCM(ctx, &server, parent); err != nil {
		log.Error(err, "apply rendered-config CM")
		obs.configsErr = err
	}
	if err := r.applyDataPVC(ctx, &server, parent); err != nil {
		log.Error(err, "apply data PVC")
		obs.pvcApplyErr = err
	}
	if err := r.applyService(ctx, &server, parent); err != nil {
		log.Error(err, "apply Service")
		obs.svcApplyErr = err
	}
	if err := r.applyStatefulSet(ctx, &server, parent); err != nil {
		log.Error(err, "apply StatefulSet")
		obs.stsApplyErr = err
	}

	obs.podName, obs.stsReady, obs.stsGetErr = r.probeStatefulSetReady(ctx, &server)
	if obs.stsGetErr != nil {
		log.Info("StatefulSet lookup", "error", obs.stsGetErr.Error())
	}
	obs.pvcBound, obs.pvcGetErr = r.probeDataPVCBound(ctx, &server)
	if obs.pvcGetErr != nil {
		log.Info("data PVC lookup", "error", obs.pvcGetErr.Error())
	}

	if err := r.writeArkServerStatus(ctx, &server, parent, obs); err != nil {
		log.Error(err, "status update")
		return ctrl.Result{}, err
	}

	if fatal := errors.Join(obs.configsErr, obs.pvcApplyErr, obs.svcApplyErr, obs.stsApplyErr); fatal != nil {
		return ctrl.Result{}, fatal
	}
	if obs.desiredReplicas > 0 && (!obs.stsReady || !obs.pvcBound) {
		return ctrl.Result{RequeueAfter: missingRefRequeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// probeStatefulSetReady reports the Pod readiness for this ArkServer.
//
// "Ready" means at least the desired replica count is reflected in
// Status.ReadyReplicas. A missing StatefulSet (e.g. transient API race
// after a fresh apply) is reported via the err return so the status update
// can pick a distinct condition reason.
func (r *ArkServerReconciler) probeStatefulSetReady(ctx context.Context, server *arkv1.ArkServer) (string, bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      statefulSetName(server),
		Namespace: server.Namespace,
	}, sts); err != nil {
		return "", false, err
	}
	podName := sts.Name + "-0"
	var desired int32 = 1
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	if desired == 0 {
		return podName, false, nil
	}
	return podName, sts.Status.ReadyReplicas >= desired, nil
}

func (r *ArkServerReconciler) probeDataPVCBound(ctx context.Context, server *arkv1.ArkServer) (bool, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      dataPVCName(server),
		Namespace: server.Namespace,
	}, pvc); err != nil {
		return false, err
	}
	return pvc.Status.Phase == corev1.ClaimBound, nil
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
