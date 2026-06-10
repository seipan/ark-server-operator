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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

var _ = Describe("ArkCluster Controller", func() {
	Context("ConfigMap reconciliation", func() {
		const (
			resourceName = "test"
			namespace    = "default"
		)

		ctx := context.Background()

		namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

		BeforeEach(func() {
			By("creating an ArkCluster with both inline ini sources and player lists")
			existing := &arkv1.ArkCluster{}
			err := k8sClient.Get(ctx, namespacedName, existing)
			if err != nil && errors.IsNotFound(err) {
				cluster := &arkv1.ArkCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: arkv1.ArkClusterSpec{
						ClusterName: "test-cluster",
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
							Inline: "[/script/shootergame.shootergamemode]\nKillXPMultiplier=8\n",
						},
						GameUserSettings: arkv1.IniSource{
							Inline: "[ServerSettings]\nServerPVE=true\n",
						},
						ArkManager: arkv1.ArkManagerSpec{
							BanListURL: "http://playark.com/banlist.txt",
							Flags: arkv1.ArkFlags{
								Crossplay: boolPtr(true),
							},
							Options: arkv1.ArkOptions{
								ActiveEvent: arkv1.ArkEventSummer,
							},
						},
						PlayerLists: arkv1.PlayerListsSpec{
							AllowedCheaters: []string{"76561198030942091"},
						},
						Passwords: arkv1.PasswordsSpec{
							SecretRef: arkv1.PasswordSecretRef{
								Name: "ark-server-secrets",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			}
		})

		AfterEach(func() {
			cluster := &arkv1.ArkCluster{}
			if err := k8sClient.Get(ctx, namespacedName, cluster); err == nil {
				Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			}
		})

		It("creates all four operator-managed ConfigMaps with owner references", func() {
			By("reconciling the ArkCluster")
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			expected := []struct {
				name        string
				expectedKey string
			}{
				{name: resourceName + "-arkmanager-cfg", expectedKey: arkManagerCfgKey},
				{name: resourceName + "-game-ini", expectedKey: globalGameIniKey},
				{name: resourceName + "-game-user-settings-ini", expectedKey: globalGameUserSettingsIniKey},
				{name: resourceName + "-player-lists", expectedKey: allowedCheatersKey},
			}

			for _, want := range expected {
				cm := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: want.name, Namespace: namespace}, cm)).
					To(Succeed(), "ConfigMap %s should exist", want.name)
				Expect(cm.Data).To(HaveKey(want.expectedKey), "ConfigMap %s should have data key %s", want.name, want.expectedKey)
				Expect(cm.OwnerReferences).To(HaveLen(1), "ConfigMap %s should have one owner reference", want.name)
				Expect(cm.OwnerReferences[0].Kind).To(Equal("ArkCluster"))
				Expect(cm.OwnerReferences[0].Name).To(Equal(resourceName))
				Expect(cm.OwnerReferences[0].Controller).NotTo(BeNil())
				Expect(*cm.OwnerReferences[0].Controller).To(BeTrue())
			}

			By("checking the rendered arkmanager.cfg contents")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-arkmanager-cfg", Namespace: namespace,
			}, cm)).To(Succeed())

			Expect(cm.Data[arkManagerCfgKey]).To(ContainSubstring("arkopt_clusterid=test-cluster"))
			Expect(cm.Data[arkManagerCfgKey]).To(ContainSubstring("arkopt_ActiveEvent=Summer"))
			Expect(cm.Data[arkManagerCfgKey]).To(ContainSubstring("arkflag_crossplay=true"))
		})

		It("writes status conditions reflecting missing dependencies", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred(),
				"missing external resources should not surface as controller errors")
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"missing references should schedule a follow-up reconcile")

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, namespacedName, cluster)).To(Succeed())

			Expect(cluster.Status.LastReconcileTime).NotTo(BeNil())
			Expect(cluster.Status.ImageDigest).To(HavePrefix("sha256:"))
			Expect(cluster.Status.GlobalConfigMapsReady).To(BeTrue(),
				"operator-managed CMs were applied")
			Expect(cluster.Status.SharedStorageReady).To(BeFalse(),
				"existingClaimName points at a PVC that does not exist in envtest")

			cfgs := meta.FindStatusCondition(cluster.Status.Conditions, ConditionConfigsApplied)
			Expect(cfgs).NotTo(BeNil())
			Expect(cfgs.Status).To(Equal(metav1.ConditionTrue))
			Expect(cfgs.Reason).To(Equal(ReasonReconciled))

			storage := meta.FindStatusCondition(cluster.Status.Conditions, ConditionSharedStorageBound)
			Expect(storage).NotTo(BeNil())
			Expect(storage.Status).To(Equal(metav1.ConditionFalse))
			Expect(storage.Reason).To(Equal(ReasonSharedStorageNotFound))

			ready := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Message).To(ContainSubstring("passwords Secret"))
		})

		It("clears the passwords Secret reason once the Secret is applied", func() {
			By("creating the passwords Secret out-of-band")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ark-server-secrets",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"serverPass": "s3cret",
					"adminPass":  "adminS3cret",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, namespacedName, cluster)).To(Succeed())

			ready := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Message).NotTo(ContainSubstring("passwords Secret"),
				"Ready message should no longer call out the Secret once it exists")
		})

		It("does not create a shared PVC when existingClaimName is set", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-shared", Namespace: namespace,
			}, pvc)
			Expect(errors.IsNotFound(err)).To(BeTrue(),
				"shared PVC should not be created when existingClaimName is set; got err=%v", err)
		})

		It("is idempotent — a second reconcile is a no-op", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Sanity: arkmanager.cfg CM still has expected data after the second reconcile.
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-arkmanager-cfg", Namespace: namespace,
			}, cm)).To(Succeed())
			Expect(cm.Data[arkManagerCfgKey]).To(ContainSubstring("arkopt_clusterid=test-cluster"))
		})
	})

	Context("Shared PVC reconciliation (operator-managed)", func() {
		const (
			resourceName = "pvc-test"
			namespace    = "default"
		)

		ctx := context.Background()
		namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

		BeforeEach(func() {
			existing := &arkv1.ArkCluster{}
			err := k8sClient.Get(ctx, namespacedName, existing)
			if err != nil && errors.IsNotFound(err) {
				size := resource.MustParse("5Gi")
				cluster := &arkv1.ArkCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: arkv1.ArkClusterSpec{
						ClusterName: "pvc-test-cluster",
						Image: arkv1.ImageSpec{
							Repository: "nightdragon1/ark-docker",
							Tag:        "v1",
							Digest:     "sha256:" + strings.Repeat("0", 64),
						},
						SharedStorage: arkv1.SharedStorageSpec{
							StorageClassName: "nfs",
							Size:             &size,
							AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
							MountPath:        "/ark-shared",
						},
						Game: arkv1.IniSource{
							Inline: "[/script/shootergame.shootergamemode]\n",
						},
						GameUserSettings: arkv1.IniSource{
							Inline: "[ServerSettings]\n",
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
		})

		AfterEach(func() {
			cluster := &arkv1.ArkCluster{}
			if err := k8sClient.Get(ctx, namespacedName, cluster); err == nil {
				Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			}
		})

		It("reports SharedStoragePending when the operator-managed PVC has not yet bound", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, namespacedName, cluster)).To(Succeed())

			storage := meta.FindStatusCondition(cluster.Status.Conditions, ConditionSharedStorageBound)
			Expect(storage).NotTo(BeNil())
			Expect(storage.Status).To(Equal(metav1.ConditionFalse))
			Expect(storage.Reason).To(Equal(ReasonSharedStoragePending),
				"envtest does not bind PVCs so phase stays empty")
		})

		It("creates a shared PVC from spec.sharedStorage", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-shared", Namespace: namespace,
			}, pvc)).To(Succeed())

			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("nfs"))
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))

			got, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(ok).To(BeTrue())
			want := resource.MustParse("5Gi")
			Expect(got.Cmp(want)).To(Equal(0), "PVC size = %s, want %s", got.String(), want.String())

			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Kind).To(Equal("ArkCluster"))
			Expect(pvc.OwnerReferences[0].Name).To(Equal(resourceName))

			Expect(pvc.Labels[LabelComponent]).To(Equal(ComponentSharedStorage))
			Expect(pvc.Labels[LabelInstance]).To(Equal(resourceName))
		})
	})

	Context("configMapRef validation", func() {
		const (
			resourceName = "ref-test"
			namespace    = "default"
		)

		ctx := context.Background()
		namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

		BeforeEach(func() {
			existing := &arkv1.ArkCluster{}
			err := k8sClient.Get(ctx, namespacedName, existing)
			if err != nil && errors.IsNotFound(err) {
				cluster := &arkv1.ArkCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: arkv1.ArkClusterSpec{
						ClusterName: "ref-test-cluster",
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
							ConfigMapRef: &arkv1.IniConfigMapRef{
								Name: "missing-user-game-ini",
								Key:  "Game.ini",
							},
						},
						GameUserSettings: arkv1.IniSource{
							Inline: "[ServerSettings]\n",
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
		})

		AfterEach(func() {
			cluster := &arkv1.ArkCluster{}
			if err := k8sClient.Get(ctx, namespacedName, cluster); err == nil {
				Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			}
		})

		It("flips ConfigsApplied to False when the referenced ConfigMap is missing", func() {
			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, namespacedName, cluster)).To(Succeed())

			cfgs := meta.FindStatusCondition(cluster.Status.Conditions, ConditionConfigsApplied)
			Expect(cfgs).NotTo(BeNil())
			Expect(cfgs.Status).To(Equal(metav1.ConditionFalse))
			Expect(cfgs.Reason).To(Equal(ReasonReferencedCMNotFound))
		})

		It("flips ConfigsApplied to ReferencedCMKeyMissing when the key is absent", func() {
			By("creating the referenced ConfigMap without the expected key")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-user-game-ini",
					Namespace: namespace,
				},
				Data: map[string]string{
					"some-other-key.ini": "[/script/shootergame.shootergamemode]\n",
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, cm)
			}()

			r := &ArkClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cluster := &arkv1.ArkCluster{}
			Expect(k8sClient.Get(ctx, namespacedName, cluster)).To(Succeed())

			cfgs := meta.FindStatusCondition(cluster.Status.Conditions, ConditionConfigsApplied)
			Expect(cfgs).NotTo(BeNil())
			Expect(cfgs.Status).To(Equal(metav1.ConditionFalse))
			Expect(cfgs.Reason).To(Equal(ReasonReferencedCMKeyMissing))
		})
	})
})
