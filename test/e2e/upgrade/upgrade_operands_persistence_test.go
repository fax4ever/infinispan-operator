package upgrade

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	ispnv1 "github.com/infinispan/infinispan-operator/api/v1"
	tutils "github.com/infinispan/infinispan-operator/test/e2e/utils"
	"github.com/stretchr/testify/assert"
)

func TestOperandUpgrades_Persistence(t *testing.T) {
	olm := testKube.OLMTestEnv()
	// Only test Operands in the most recent CSV
	olm.SubStartingCSV = olm.TargetChannel.CurrentCSVName
	olm.SourceChannel = olm.TargetChannel
	olm.PrintManifest()

	testKube.NewNamespace(tutils.Namespace)
	sub := subscription(olm)

	defer testKube.CleanupOLMTest(t, tutils.TestName(t), olm.SubName, olm.SubNamespace, olm.SubPackage)
	testKube.CreateOperatorGroup(olm)
	testKube.CreateSubscriptionAndApproveInitialVersion(sub)

	versionManager := testKube.VersionManagerFromCSV(sub)
	assert.NotNil(t, versionManager)

	startingOperandIdx := 0
	replicas := 2

	assert.NotNil(t, startingOperandIdx)

	ispn := tutils.DefaultSpec(t, testKube, func(i *ispnv1.Infinispan) {
		i.Spec.Replicas = int32(replicas)
		i.Spec.Service.Container.EphemeralStorage = false
		i.Spec.Logging.Categories["org.infinispan.topology"] = ispnv1.LoggingLevelTrace
		i.Spec.Version = versionManager.Operands[startingOperandIdx].Ref()
	})

	fmt.Printf("Testing upgrades from Operand '%s' to '%s'\n", ispn.Spec.Version, versionManager.Latest().Ref())
	testKube.CreateInfinispan(ispn, tutils.Namespace)
	testKube.WaitForInfinispanPods(replicas, tutils.SinglePodTimeout, ispn.Name, tutils.Namespace)
	testKube.WaitForInfinispanConditionWithTimeout(ispn.Name, ispn.Namespace, ispnv1.ConditionWellFormed, conditionTimeout)

	numEntries := 100
	client := tutils.HTTPClientForClusterWithVersionManager(ispn, testKube, versionManager)

	// Add a persistent cache with data to ensure contents can be read after upgrade(s)
	createAndPopulatePersistentCache(persistentCacheName, numEntries, client)
	assertNoDegradedCaches()

	for _, operand := range versionManager.Operands[startingOperandIdx+1:] {
		fmt.Printf("Next Operand %s\n", operand.Ref())
		ispn = testKube.WaitForInfinispanConditionWithTimeout(ispn.Name, tutils.Namespace, ispnv1.ConditionWellFormed, conditionTimeout)
		tutils.ExpectNoError(
			testKube.UpdateInfinispan(ispn, func() {
				ispn.Spec.Version = operand.Ref()
				fmt.Printf("Upgrading Operand to %s\n", ispn.Spec.Version)
			}),
		)
		testKube.WaitForInfinispanPods(replicas, tutils.SinglePodTimeout, ispn.Name, tutils.Namespace)
		testKube.WaitForInfinispanState(ispn.Name, ispn.Namespace, func(i *ispnv1.Infinispan) bool {
			return i.IsConditionTrue(ispnv1.ConditionWellFormed) &&
				i.Status.Operand.Version == operand.Ref() &&
				i.Status.Operand.Image == operand.Image &&
				i.Status.Operand.Phase == ispnv1.OperandPhaseRunning
		})
		assertOperandImage(operand.Image, ispn)
		assertNoDegradedCaches()

		// Ensure that persistent cache entries have survived the upgrade(s)
		// Refresh the hostAddr and client as the url will change if NodePort is used.
		client = tutils.HTTPClientForClusterWithVersionManager(ispn, testKube, versionManager)
		tutils.NewCacheHelper(persistentCacheName, client).AssertSize(numEntries)

		// Kill the first pod to ensure that the cluster can recover from failover after upgrade
		err := testKube.Kubernetes.Client.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ispn.Name + "-0",
				Namespace: tutils.Namespace,
			},
		})
		tutils.ExpectNoError(err)
		testKube.WaitForInfinispanPods(replicas, tutils.SinglePodTimeout, ispn.Name, tutils.Namespace)
		testKube.WaitForInfinispanConditionWithTimeout(ispn.Name, tutils.Namespace, ispnv1.ConditionWellFormed, conditionTimeout)

		// Ensure that persistent cache entries still contain the expected numEntries
		versionManager = testKube.VersionManagerFromCSV(sub)
		client = tutils.HTTPClientForClusterWithVersionManager(ispn, testKube, versionManager)
		tutils.NewCacheHelper(persistentCacheName, client).AssertSize(numEntries)
	}
}
