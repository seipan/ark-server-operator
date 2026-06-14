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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

var _ = Describe("ArkServer Controller", func() {
	Context("Running path (PR1)", func() {
		const (
			parentName = "parent-cluster"
			serverName = "gen1-test"
			namespace  = "default"
		)
		ctx := context.Background()
		serverNN := types.NamespacedName{Name: serverName, Namespace: namespace}
		parentNN := types.NamespacedName{Name: parentName, Namespace: namespace}

		BeforeEach(func() {
			By("creating the parent ArkCluster")
			cluster := &arkv1.ArkCluster{}
			if err := k8sClient.Get(ctx, parentNN, cluster); err != nil && errors.IsNotFound(err) {
				cluster = &arkv1.ArkCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      parentName,
						Namespace: namespace,
					},
					Spec: arkv1.ArkClusterSpec{
						ClusterName: "parent-game-cluster",
						Image: arkv1.ImageSpec{
							Repository: "nightdragon1/ark-docker",
							Tag:        "v1",
							Digest:     "sha256:" + strings.Repeat("0", 64),
						},
						SharedStorage: arkv1.SharedStorageSpec{
							ExistingClaimName: "ark-shared",
							MountPath:         "/ark-shared",
						},
						Game: arkv1.IniSource{
							Inline: "[/script/shootergame.shootergamemode]\n",
						},
						GameUserSettings: arkv1.IniSource{
							Inline: "[ServerSettings]\nServerPVE=true\n",
						},
						ArkManager: arkv1.ArkManagerSpec{},
						Passwords: arkv1.PasswordsSpec{
							SecretRef: arkv1.PasswordSecretRef{
								Name: "ark-server-secrets",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			}

			By("creating the ArkServer")
			server := &arkv1.ArkServer{}
			if err := k8sClient.Get(ctx, serverNN, server); err != nil && errors.IsNotFound(err) {
				server = &arkv1.ArkServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serverName,
						Namespace: namespace,
					},
					Spec: arkv1.ArkServerSpec{
						ClusterRef:  arkv1.ClusterReference{Name: parentName},
						Map:         arkv1.MapTheIsland,
						SessionName: "PR1 - The Island",
						Ports: arkv1.PortsSpec{
							Client: 31001,
							Game:   31002,
							Query:  31003,
							RCON:   31004,
						},
						OverrideGameUserSettings: "[MessageOfTheDay]\nMessage=Test\n",
					},
				}
				Expect(k8sClient.Create(ctx, server)).To(Succeed())
			}
		})

		AfterEach(func() {
			server := &arkv1.ArkServer{}
			if err := k8sClient.Get(ctx, serverNN, server); err == nil {
				Expect(k8sClient.Delete(ctx, server)).To(Succeed())
			}
			cluster := &arkv1.ArkCluster{}
			if err := k8sClient.Get(ctx, parentNN, cluster); err == nil {
				Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			}
		})

		It("creates rendered-config CM, PVC, Service, and StatefulSet with owner refs", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())

			By("verifying the rendered-config ConfigMap")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName + "-rendered-config", Namespace: namespace,
			}, cm)).To(Succeed())
			Expect(cm.Data).To(HaveKey(arkManagerCfgKey))
			Expect(cm.Data).To(HaveKey(mergedGameIniKey))
			Expect(cm.Data).To(HaveKey(mergedGameUserSettingsIniKey))
			Expect(cm.Data[mergedGameUserSettingsIniKey]).To(ContainSubstring("Message=Test"))
			Expect(cm.OwnerReferences).To(HaveLen(1))
			Expect(cm.OwnerReferences[0].Kind).To(Equal("ArkServer"))

			By("verifying the data PVC")
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName + "-data", Namespace: namespace,
			}, pvc)).To(Succeed())
			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Kind).To(Equal("ArkServer"))

			By("verifying the NodePort Service")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, svc)).To(Succeed())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
			Expect(svc.Spec.Ports).To(HaveLen(4))
			Expect(svc.OwnerReferences).To(HaveLen(1))

			By("verifying the StatefulSet")
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			Expect(sts.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(sts.OwnerReferences).To(HaveLen(1))
		})

		It("scales the StatefulSet to zero replicas when desiredState is Stopped", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			By("flipping desiredState to Stopped")
			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.DesiredState = arkv1.StateStopped
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(0)))

			By("verifying the PVC is still present (saves retained)")
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName + "-data", Namespace: namespace,
			}, pvc)).To(Succeed())
		})

		It("scales the StatefulSet to zero replicas when desiredState is Hibernated", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.DesiredState = arkv1.StateHibernated
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(0)))
		})

		It("scales back up to one replica when desiredState returns to Running", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("starting from Stopped")
			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.DesiredState = arkv1.StateStopped
			Expect(k8sClient.Update(ctx, server)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			By("flipping back to Running")
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.DesiredState = arkv1.StateRunning
			Expect(k8sClient.Update(ctx, server)).To(Succeed())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
		})

		It("writes status conditions and caches clusterName on first reconcile", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())

			Expect(server.Status.LastReconcileTime).NotTo(BeNil())
			Expect(server.Status.ClusterName).To(Equal(parentName))
			Expect(server.Status.PodName).To(Equal(serverName + "-0"))
			Expect(server.Status.StatefulSetReady).To(BeFalse())
			Expect(server.Status.PVCReady).To(BeFalse())

			ref := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionClusterRefValid)
			Expect(ref).NotTo(BeNil())
			Expect(ref.Status).To(Equal(metav1.ConditionTrue))
			Expect(ref.Reason).To(Equal(ArkServerReasonClusterRefResolved))

			pvc := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionPVCReady)
			Expect(pvc).NotTo(BeNil())
			Expect(pvc.Status).To(Equal(metav1.ConditionFalse))
			Expect(pvc.Reason).To(Equal(ArkServerReasonPVCPending))

			ready := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(server.Status.Phase).To(Equal(arkv1.PhaseProvisioning))
		})

		It("flips ClusterRefValid to False when clusterRef is changed after creation", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			By("mutating spec.clusterRef.name to a different value")
			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.ClusterRef.Name = "different-cluster"
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())

			ref := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionClusterRefValid)
			Expect(ref).NotTo(BeNil())
			Expect(ref.Status).To(Equal(metav1.ConditionFalse))
			Expect(ref.Reason).To(Equal(ArkServerReasonClusterRefChangeForbidden))
			Expect(ref.Message).To(ContainSubstring("clusterRef cannot change after creation"))

			Expect(server.Status.Phase).To(Equal(arkv1.PhaseFailed))
		})

		It("reports ClusterRefNotFound when the parent ArkCluster disappears", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, parentNN, cluster)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())

			ref := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionClusterRefValid)
			Expect(ref).NotTo(BeNil())
			Expect(ref.Status).To(Equal(metav1.ConditionFalse))
			Expect(ref.Reason).To(Equal(ArkServerReasonClusterRefNotFound))
			Expect(server.Status.Phase).To(Equal(arkv1.PhaseFailed))
		})

		It("reports Phase=Stopped and StatefulSetReady=ScaledToZero when desiredState is Stopped", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			server.Spec.DesiredState = arkv1.StateStopped
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())
			Expect(server.Status.Phase).To(Equal(arkv1.PhaseStopped))

			sts := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionStatefulSetReady)
			Expect(sts).NotTo(BeNil())
			Expect(sts.Status).To(Equal(metav1.ConditionFalse))
			Expect(sts.Reason).To(Equal(ArkServerReasonScaledToZero))
		})

		It("reports Phase=Running and Ready=True when STS and PVC are fully bound", func() {
			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			By("manually marking the PVC Bound (envtest has no provisioner)")
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName + "-data", Namespace: namespace,
			}, pvc)).To(Succeed())
			pvc.Status.Phase = corev1.ClaimBound
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			By("manually marking the STS ready (envtest has no kubelet)")
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 1
			sts.Status.Replicas = 1
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred())

			server := &arkv1.ArkServer{}
			Expect(k8sClient.Get(ctx, serverNN, server)).To(Succeed())

			Expect(server.Status.Phase).To(Equal(arkv1.PhaseRunning))

			ready := meta.FindStatusCondition(server.Status.Conditions, ArkServerConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(ArkServerReasonAllSubresourcesReady))
		})

		It("returns RequeueAfter when the parent ArkCluster is missing", func() {
			By("deleting the parent ArkCluster")
			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, parentNN, cluster)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())

			r := &ArkServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: serverNN})
			Expect(err).NotTo(HaveOccurred(), "missing parent must not surface as controller error")
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"missing parent should schedule a periodic requeue")
		})
	})
})
