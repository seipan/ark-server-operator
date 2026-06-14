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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

// resolvedParent is the per-Reconcile snapshot of everything the ArkServer
// needs from its parent ArkCluster. Centralising the resolution in one
// struct keeps the builders pure (string in, K8s object out) and lets us
// unit-test render logic without a fake client.
type resolvedParent struct {
	cluster *arkv1.ArkCluster

	// sharedPVCName is the PVC the Pod mounts at sharedMountPath. Either
	// the operator-managed PVC (<cluster>-shared) or, when
	// spec.sharedStorage.existingClaimName is set on the cluster, that.
	sharedPVCName   string
	sharedMountPath string

	// arkManagerCfg is the rendered plain-text arkmanager.cfg. Per-Pod
	// values are still ${VAR} placeholders that bash expands when arkmanager
	// sources the file at container start.
	arkManagerCfg string

	// globalGame / globalGameUserSettings are the resolved ini contents. For
	// inline sources this is just the spec value; for configMapRef sources
	// the named ConfigMap is fetched and its key extracted here so the
	// builders never have to touch the API server.
	globalGame             string
	globalGameUserSettings string

	allowedCheaters   string
	joinNoCheck       string
	exclusiveJoinList string
}

// resolveParent reads the parent ArkCluster and any inputs it references
// (configMapRef-backed ini sources), and assembles a resolvedParent.
//
// Returns an error if the parent is missing, the parent's referenced
// ConfigMap is missing, or any required key is absent. Callers should treat
// these as "external resource not yet present" and requeue periodically
// rather than triggering exponential backoff.
func (r *ArkServerReconciler) resolveParent(ctx context.Context, server *arkv1.ArkServer) (*resolvedParent, error) {
	cluster := &arkv1.ArkCluster{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: server.Namespace,
		Name:      server.Spec.ClusterRef.Name,
	}, cluster); err != nil {
		return nil, fmt.Errorf("get parent ArkCluster %q: %w", server.Spec.ClusterRef.Name, err)
	}

	p := &resolvedParent{cluster: cluster}

	p.sharedPVCName = SharedStoragePVCName(cluster)
	if cn := cluster.Spec.SharedStorage.ExistingClaimName; cn != "" {
		p.sharedPVCName = cn
	}
	p.sharedMountPath = cluster.Spec.SharedStorage.MountPath
	if p.sharedMountPath == "" {
		p.sharedMountPath = defaultSharedMountPath
	}

	p.arkManagerCfg = renderArkManagerCfg(cluster)

	game, err := r.resolveIniSource(ctx, server.Namespace, cluster.Spec.Game, "spec.game")
	if err != nil {
		return nil, err
	}
	p.globalGame = game

	gus, err := r.resolveIniSource(ctx, server.Namespace, cluster.Spec.GameUserSettings, "spec.gameUserSettings")
	if err != nil {
		return nil, err
	}
	p.globalGameUserSettings = gus

	pl := cluster.Spec.PlayerLists
	p.allowedCheaters = strings.Join(pl.AllowedCheaters, "\n")
	p.joinNoCheck = strings.Join(pl.JoinNoCheck, "\n")
	p.exclusiveJoinList = strings.Join(pl.ExclusiveJoinList, "\n")
	return p, nil
}

func (r *ArkServerReconciler) resolveIniSource(ctx context.Context, namespace string, src arkv1.IniSource, label string) (string, error) {
	if src.Inline != "" {
		return src.Inline, nil
	}
	if src.ConfigMapRef != nil {
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: src.ConfigMapRef.Name}, cm); err != nil {
			return "", fmt.Errorf("%s: get ConfigMap %s: %w", label, src.ConfigMapRef.Name, err)
		}
		content, ok := cm.Data[src.ConfigMapRef.Key]
		if !ok {
			return "", fmt.Errorf("%s: ConfigMap %s missing data key %q", label, src.ConfigMapRef.Name, src.ConfigMapRef.Key)
		}
		return content, nil
	}
	return "", fmt.Errorf("%s: neither inline nor configMapRef is set", label)
}
