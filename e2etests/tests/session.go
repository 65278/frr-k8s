// SPDX-License-Identifier:Apache-2.0

package tests

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"

	frrk8sv1beta1 "github.com/metallb/frr-k8s/api/v1beta1"
	"github.com/metallb/frrk8stests/pkg/config"
	"github.com/metallb/frrk8stests/pkg/dump"
	"github.com/metallb/frrk8stests/pkg/infra"
	"github.com/metallb/frrk8stests/pkg/k8s"
	. "github.com/onsi/gomega"
	"go.universe.tf/e2etest/pkg/executor"
	"go.universe.tf/e2etest/pkg/frr"
	frrconfig "go.universe.tf/e2etest/pkg/frr/config"
	frrcontainer "go.universe.tf/e2etest/pkg/frr/container"
	"go.universe.tf/e2etest/pkg/ipfamily"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"

	"k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

var _ = ginkgo.Describe("Advertisement", func() {
	var f *framework.Framework
	var cs clientset.Interface

	defer ginkgo.GinkgoRecover()
	clientconfig, err := framework.LoadConfig()
	framework.ExpectNoError(err)
	updater, err := config.NewUpdater(clientconfig)
	framework.ExpectNoError(err)
	reporter := dump.NewK8sReporter(framework.TestContext.KubeConfig, k8s.FRRK8sNamespace)

	f = framework.NewDefaultFramework("bgpfrr")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	ginkgo.AfterEach(func() {
		if ginkgo.CurrentSpecReport().Failed() {
			testName := ginkgo.CurrentSpecReport().LeafNodeText
			dump.K8sInfo(testName, reporter)
			dump.BGPInfo(testName, infra.FRRContainers, f.ClientSet, f)
		}
	})

	ginkgo.BeforeEach(func() {
		ginkgo.By("Clearing any previous configuration")

		for _, c := range infra.FRRContainers {
			err := c.UpdateBGPConfigFile(frrconfig.Empty)
			framework.ExpectNoError(err)
		}
		err := updater.Clean()
		framework.ExpectNoError(err)
		cs = f.ClientSet
	})

	ginkgo.Context("Session parameters", func() {
		ginkgo.It("are set correctly", func() {
			config := frrk8sv1beta1.FRRConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: k8s.FRRK8sNamespace,
				},
				Spec: frrk8sv1beta1.FRRConfigurationSpec{
					BGP: frrk8sv1beta1.BGPConfig{
						Routers: []frrk8sv1beta1.Router{
							{
								ASN: infra.FRRK8sASN,
								Neighbors: []frrk8sv1beta1.Neighbor{
									{
										ASN:     1234,
										Address: "192.168.1.1",
										HoldTime: &metav1.Duration{
											Duration: 100 * time.Second,
										},
										KeepaliveTime: &metav1.Duration{
											Duration: 20 * time.Second,
										},
									},
								},
							},
						},
					},
				},
			}
			err = updater.Update([]corev1.Secret{}, config)
			framework.ExpectNoError(err)

			pods, err := k8s.FRRK8sPods(cs)
			framework.ExpectNoError(err)

			for _, pod := range pods {
				podExec := executor.ForPod(pod.Namespace, pod.Name, "frr")
				Eventually(func() error {
					neighbors, err := frr.NeighborsInfo(podExec)
					if err != nil {
						return err
					}
					if len(neighbors) != 1 {
						return fmt.Errorf("expected 1 neighbor, got %d", len(neighbors))
					}
					if neighbors[0].ConfiguredHoldTime != 100000 {
						return fmt.Errorf("expected hold time to be 100000, got %d", neighbors[0].ConfiguredHoldTime)
					}
					if neighbors[0].ConfiguredKeepAliveTime != 20000 {
						return fmt.Errorf("expected hold time to be 20000, got %d", neighbors[0].ConfiguredKeepAliveTime)
					}
					return nil
				}, 2*time.Minute, time.Second).ShouldNot(HaveOccurred())
			}
		})

		ginkgo.DescribeTable("Establishes sessions with cleartext password", func(family ipfamily.Family) {
			frrs := config.ContainersForVRF(infra.FRRContainers, "")
			neighbors := []frrk8sv1beta1.Neighbor{}

			for _, f := range frrs {
				addresses := f.AddressesForFamily(family)
				ebgpMultihop := false
				if f.NeighborConfig.MultiHop && f.NeighborConfig.ASN != f.RouterConfig.ASN {
					ebgpMultihop = true
				}

				for _, address := range addresses {
					neighbors = append(neighbors, frrk8sv1beta1.Neighbor{
						ASN:          f.RouterConfig.ASN,
						Address:      address,
						Password:     f.RouterConfig.Password,
						Port:         &f.RouterConfig.BGPPort,
						EBGPMultiHop: ebgpMultihop,
					})
				}
			}

			config := frrk8sv1beta1.FRRConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: k8s.FRRK8sNamespace,
				},
				Spec: frrk8sv1beta1.FRRConfigurationSpec{
					BGP: frrk8sv1beta1.BGPConfig{
						Routers: []frrk8sv1beta1.Router{
							{
								ASN:       infra.FRRK8sASN,
								VRF:       "",
								Neighbors: neighbors,
							},
						},
					},
				},
			}

			ginkgo.By("pairing with nodes")
			for _, c := range frrs {
				err := frrcontainer.PairWithNodes(cs, c, family)
				framework.ExpectNoError(err)
			}

			err := updater.Update([]corev1.Secret{}, config)
			framework.ExpectNoError(err)

			nodes, err := k8s.Nodes(cs)
			framework.ExpectNoError(err)

			for _, c := range frrs {
				ValidateFRRPeeredWithNodes(nodes, c, family)
			}
		},
			ginkgo.Entry("IPV4", ipfamily.IPv4),
			ginkgo.Entry("IPV6", ipfamily.IPv6),
		)
	})
})
