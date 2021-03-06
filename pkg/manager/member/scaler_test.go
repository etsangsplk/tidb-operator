// Copyright 2018 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package member

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/gomega"
	"github.com/pingcap/advanced-statefulset/pkg/apis/apps/v1/helper"
	"github.com/pingcap/tidb-operator/pkg/apis/pingcap/v1alpha1"
	"github.com/pingcap/tidb-operator/pkg/controller"
	"github.com/pingcap/tidb-operator/pkg/features"
	"github.com/pingcap/tidb-operator/pkg/label"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	kubeinformers "k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestGeneralScalerDeleteAllDeferDeletingPVC(t *testing.T) {
	type testcase struct {
		name         string
		memberType   v1alpha1.MemberType
		ordinal      int32
		pvc          *corev1.PersistentVolumeClaim
		deleteFailed bool
		expectFn     func(*GomegaWithT, map[int32]string, error)
	}
	tc := newTidbClusterForPD()
	setName := controller.PDMemberName(tc.GetName())
	testFn := func(test *testcase, t *testing.T) {
		t.Logf(test.name)
		g := NewGomegaWithT(t)

		gs, pvcIndexer, pvcControl := newFakeGeneralScaler()
		if test.pvc != nil {
			pvcIndexer.Add(test.pvc)
		}
		if test.deleteFailed {
			pvcControl.SetDeletePVCError(fmt.Errorf("delete pvc failed"), 0)
		}

		skipReason, err := gs.deleteDeferDeletingPVC(tc, setName, test.memberType, test.ordinal)
		test.expectFn(g, skipReason, err)
	}
	tests := []testcase{
		{
			name:       "normal",
			memberType: v1alpha1.PDMemberType,
			ordinal:    3,
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ordinalPVCName(v1alpha1.PDMemberType, setName, 3),
					Namespace: corev1.NamespaceDefault,
					Annotations: map[string]string{
						label.AnnPVCDeferDeleting: "deleting-3",
					},
				},
			},
			deleteFailed: false,
			expectFn: func(g *GomegaWithT, skipReason map[int32]string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(skipReason)).To(Equal(0))
			},
		},
		{
			name:         "pvc is not found",
			memberType:   v1alpha1.PDMemberType,
			ordinal:      3,
			pvc:          nil,
			deleteFailed: false,
			expectFn: func(g *GomegaWithT, skipReason map[int32]string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(skipReason)).To(Equal(1))
				g.Expect(skipReason[3]).To(Equal(skipReasonScalerPVCNotFound))
			},
		},
		{
			name:       "pvc annotations is nil",
			memberType: v1alpha1.PDMemberType,
			ordinal:    3,
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ordinalPVCName(v1alpha1.PDMemberType, setName, 3),
					Namespace: corev1.NamespaceDefault,
				},
			},
			deleteFailed: false,
			expectFn: func(g *GomegaWithT, skipReason map[int32]string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(skipReason)).To(Equal(1))
				g.Expect(skipReason[3]).To(Equal(skipReasonScalerAnnIsNil))
			},
		},
		{
			name:       "pvc annotations defer deleting is empty",
			memberType: v1alpha1.PDMemberType,
			ordinal:    3,
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        ordinalPVCName(v1alpha1.PDMemberType, setName, 3),
					Namespace:   corev1.NamespaceDefault,
					Annotations: map[string]string{},
				},
			},
			deleteFailed: false,
			expectFn: func(g *GomegaWithT, skipReason map[int32]string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(skipReason)).To(Equal(1))
				g.Expect(skipReason[3]).To(Equal(skipReasonScalerAnnDeferDeletingIsEmpty))
			},
		},
		{
			name:       "pvc delete failed",
			memberType: v1alpha1.PDMemberType,
			ordinal:    3,
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ordinalPVCName(v1alpha1.PDMemberType, setName, 3),
					Namespace: corev1.NamespaceDefault,
					Annotations: map[string]string{
						label.AnnPVCDeferDeleting: "deleting-3",
					},
				},
			},
			deleteFailed: true,
			expectFn: func(g *GomegaWithT, skipReason map[int32]string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "delete pvc failed")).To(BeTrue())
				g.Expect(len(skipReason)).To(Equal(0))
			},
		},
	}

	for i := range tests {
		testFn(&tests[i], t)
	}
}

func newFakeGeneralScaler() (*generalScaler, cache.Indexer, *controller.FakePVCControl) {
	kubeCli := kubefake.NewSimpleClientset()
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeCli, 0)
	pvcInformer := kubeInformerFactory.Core().V1().PersistentVolumeClaims()
	pvcControl := controller.NewFakePVCControl(pvcInformer)

	return &generalScaler{pvcLister: pvcInformer.Lister(), pvcControl: pvcControl},
		pvcInformer.Informer().GetIndexer(), pvcControl
}

func TestScaleOne(t *testing.T) {
	type scaleOp struct {
		scaling     int
		ordinal     int32
		replicas    int32
		deleteSlots sets.Int32
	}
	tests := []struct {
		name    string
		actual  *apps.StatefulSet
		desired *apps.StatefulSet
		wantOps []scaleOp
	}{
		{
			"no scaling required",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(1),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(1),
				},
			},
			[]scaleOp{
				{
					0,
					-1,
					1,
					sets.Int32{},
				},
			},
		},
		{
			"scale in without delete slots",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(1),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			[]scaleOp{
				{
					-1,
					0,
					0,
					sets.Int32{},
				},
			},
		},
		{
			"scale in without delete slots (diff > 1)",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(8),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(6),
				},
			},
			[]scaleOp{
				{
					-1,
					7,
					7,
					sets.Int32{},
				},
				{
					-1,
					6,
					6,
					sets.Int32{},
				},
			},
		},
		{
			"scale in from 2 to 0 without delete slots",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(2),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			[]scaleOp{
				{
					-1,
					1,
					1,
					sets.NewInt32(),
				},
				{
					-1,
					0,
					0,
					sets.NewInt32(),
				},
			},
		},
		{
			"scale out from 0 to 1 without delete slots",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(1),
				},
			},
			[]scaleOp{
				{
					1,
					0,
					1,
					sets.Int32{},
				},
			},
		},
		{
			"scale out from 3 to 5 without delete slots (diff > 1)",
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(3),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(5),
				},
			},
			[]scaleOp{
				{
					1,
					3,
					4,
					sets.Int32{},
				},
				{
					1,
					4,
					5,
					sets.Int32{},
				},
			},
		},
		{
			"scale in from 5 to 3 with delete slots",
			// 0, 2, 3, 4, 5
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[1]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(5),
				},
			},
			// 0, 4, 5
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[1,2,3]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(3),
				},
			},
			[]scaleOp{
				{
					-1,
					3,
					4,
					sets.NewInt32(1, 3),
				},
				{
					-1,
					2,
					3,
					sets.NewInt32(1, 2, 3),
				},
			},
		},
		{
			"scale in from 2 to 0 with delete slots",
			// 1,2
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[0]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(2),
				},
			},
			// <none>
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			[]scaleOp{
				{
					-1,
					2,
					1,
					sets.NewInt32(0),
				},
				{
					-1,
					1,
					0,
					sets.NewInt32(),
				},
			},
		},
		{
			"scale in from 2 to 0 with delete slots which contains redundant data",
			// 1,2
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[0]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(2),
				},
			},
			// <none>
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[0,1,3]", // this is redundant
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			[]scaleOp{
				{
					-1,
					2,
					1,
					sets.NewInt32(0, 3),
				},
				{
					-1,
					1,
					0,
					sets.NewInt32(0, 1, 3),
				},
			},
		},
		{
			"scale out from 0 to 2 with delete slots",
			// <none>
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			// 1,2
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[0]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(2),
				},
			},
			[]scaleOp{
				{
					1,
					1,
					1,
					sets.NewInt32(0),
				},
				{
					1,
					2,
					2,
					sets.NewInt32(0),
				},
			},
		},
		{
			"scale out from 3 to 5 with delete slots",
			// 0, 4, 5
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[1, 2, 3]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(3),
				},
			},
			// 0, 2, 3, 4, 5
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[1]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(5),
				},
			},
			[]scaleOp{
				{
					1,
					2,
					4,
					sets.NewInt32(1, 3),
				},
				{
					1,
					3,
					5,
					sets.NewInt32(1),
				},
			},
		},
		{
			"scale out from 0 to 2 with delete slots with redundant data",
			// <none>
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(0),
				},
			},
			// 1,2
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[0,4,5]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(2),
				},
			},
			[]scaleOp{
				{
					1,
					1,
					1,
					sets.NewInt32(0, 4, 5),
				},
				{
					1,
					2,
					2,
					sets.NewInt32(0, 4, 5),
				},
			},
		},
		{
			"scale in and out with delete slots by adding 1 then deleting 2",
			// 0, 2, 3, 4
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[1]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(4),
				},
			},
			&apps.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						helper.DeleteSlotsAnn: "[2]",
					},
				},
				Spec: apps.StatefulSetSpec{
					Replicas: controller.Int32Ptr(4),
				},
			},
			[]scaleOp{
				{
					// 0, 1, 2, 3, 4
					1,
					1,
					5,
					sets.NewInt32(),
				},
				{
					// 0, 1, 3, 4
					-1,
					2,
					4,
					sets.NewInt32(2),
				},
			},
		},
	}

	features.DefaultFeatureGate.Set("AdvancedStatefulSet=true")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := tt.actual.DeepCopy()
			for i, op := range tt.wantOps {
				t.Logf("scaleOne %d", i)
				scaling, ordinal, replicas, deleteSlots := scaleOne(target, tt.desired)
				if diff := cmp.Diff(op.scaling, scaling); diff != "" {
					t.Errorf("unexpected (-want, +got): %s", diff)
				}
				if diff := cmp.Diff(op.ordinal, ordinal); diff != "" {
					t.Errorf("unexpected (-want, +got): %s", diff)
				}
				if diff := cmp.Diff(op.replicas, replicas); diff != "" {
					t.Errorf("unexpected (-want, +got): %s", diff)
				}
				if diff := cmp.Diff(op.deleteSlots, deleteSlots); diff != "" {
					t.Errorf("unexpected (-want, +got): %s", diff)
				}
				setReplicasAndDeleteSlots(target, replicas, deleteSlots)
			}
			if diff := cmp.Diff(tt.desired, target); diff != "" {
				t.Errorf("unexpected (-want, +got): %s", diff)
			}
		})
	}
}
