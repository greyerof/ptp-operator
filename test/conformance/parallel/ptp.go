//go:build !unittests
// +build !unittests

package test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"

	testclient "github.com/openshift/ptp-operator/test/pkg/client"
	"github.com/openshift/ptp-operator/test/pkg/event"
	"github.com/openshift/ptp-operator/test/pkg/execute"
	"github.com/openshift/ptp-operator/test/pkg/ptphelper"
	"github.com/openshift/ptp-operator/test/pkg/ptptesthelper"
	"github.com/openshift/ptp-operator/test/pkg/testconfig"
	v1core "k8s.io/api/core/v1"

	. "github.com/onsi/gomega"
	ptptestconfig "github.com/openshift/ptp-operator/test/conformance/config"
	exports "github.com/redhat-cne/ptp-listener-exports"
	lib "github.com/redhat-cne/ptp-listener-lib"
	ptpEvent "github.com/redhat-cne/sdk-go/pkg/event/ptp"
	"github.com/sirupsen/logrus"
)

const (
	clockSyncStateLocalForwardPort = 8901
	clockSyncStateLocalHttpPort    = 8902
)

// this full config is one per thread
var fullConfig = testconfig.TestConfig{}
var _ = Describe("[ptp-long-running]", func() {

	var testParameters ptptestconfig.PtpTestConfig

	execute.BeforeAll(func() {
		testParameters = ptptestconfig.GetPtpTestConfig()
		testclient.Client = testclient.New("")
		Expect(testclient.Client).NotTo(BeNil())
	})

	Context("Soak testing", func() {
		BeforeEach(func() {
			if fullConfig.Status == testconfig.DiscoveryFailureStatus {
				Skip("Failed to find a valid ptp slave configuration")
			}
		})
		FIt("PTP CPU Utilization", func() {
			testPtpCpuUtilization(fullConfig, testParameters)
		})
	})

	Context("Event based tests", func() {
		BeforeEach(func() {
			logrus.Debugf("fullConfig=%s", fullConfig.String())
			if fullConfig.Status == testconfig.DiscoveryFailureStatus {
				Skip("Failed to find a valid ptp slave configuration")
			}
		})

		It("PTP Slave Clock Sync", func() {
			useSideCar := false
			if event.IsDeployConsumerSidecar() {
				if fullConfig.PtpEventsIsSidecarReady {
					useSideCar = true
				}
			}

			event.InitEvents(&fullConfig, clockSyncStateLocalHttpPort, clockSyncStateLocalForwardPort, useSideCar)
			testPtpSlaveClockSync(fullConfig, testParameters) // Implementation of the test case

		})
		AfterEach(func() {
			// closing internal pubsub
			lib.Ps.Close()

			// unsubscribing all supported events
			lib.UnsubscribeAllEvents(
				fullConfig.DiscoveredClockUnderTestPod.Spec.NodeName, // this is the remote end of the port forwarding tunnel (pod's node name))
			)
		})
	})
})

// test case for continuous testing of clock synchronization of the clock under test
func testPtpSlaveClockSync(fullConfig testconfig.TestConfig, testParameters ptptestconfig.PtpTestConfig) {
	Expect(testclient.Client).NotTo(BeNil())
	logrus.Debugf("sync test fullConfig=%s", fullConfig.String())
	if fullConfig.Status == testconfig.DiscoveryFailureStatus {
		Fail("failed to find a valid ptp slave configuration")
	}

	if testParameters.SoakTestConfig.DisableSoakTest {
		Skip("skip the test as the entire suite is disabled")
	}

	soakTestConfig := testParameters.SoakTestConfig
	slaveClockSyncTestSpec := testParameters.SoakTestConfig.SlaveClockSyncConfig.TestSpec

	if !slaveClockSyncTestSpec.Enable {
		Skip("skip the test - the test is disabled")
	}

	logrus.Info("Test description ", soakTestConfig.SlaveClockSyncConfig.Description)

	// populate failure threshold
	failureThreshold := slaveClockSyncTestSpec.FailureThreshold
	if failureThreshold == 0 {
		failureThreshold = soakTestConfig.FailureThreshold
	}
	if failureThreshold == 0 {
		failureThreshold = 1
	}
	logrus.Info("Failure threshold = ", failureThreshold)
	// Actual implementation
	testSyncState(soakTestConfig)
}

// This test will run for configured minutes or until failure_threshold reached,
// whatever comes first. A failure_threshold is reached each time the cpu usage
// of the sum of the cpu usage of all the ptp pods (daemonset & operator) deployed
// in the same node is higher than the expected one. The cpu usage check for each
// node is once per minute.
func testPtpCpuUtilization(fullConfig testconfig.TestConfig, testParameters ptptestconfig.PtpTestConfig) {
	const (
		minimumFailureThreshold  = 1
		cpuUsageCheckingInterval = 1 * time.Minute
		milliCoresThreshold      = 1 // ptptestconfig.PtpDefaultMilliCoresUsageThreshold
	)

	logrus.Debugf("CPU Utilization TC Config: %+v", testParameters.SoakTestConfig.CpuUtilization)

	if testParameters.SoakTestConfig.DisableSoakTest {
		Skip("skip the test as the entire suite is disabled")
	}

	params := testParameters.SoakTestConfig.CpuUtilization
	if !params.TestSpec.Enable {
		Skip("skip the test - the test is disabled")
		return
	}

	// Set failureThresold limit number.
	failureThreshold := minimumFailureThreshold
	if params.TestSpec.FailureThreshold > minimumFailureThreshold {
		failureThreshold = params.TestSpec.FailureThreshold
	}

	prometheusPod, err := ptptesthelper.GetPrometheusPod()
	Expect(err).To(BeNil(), "failed to get prometheus pod")

	ptpPodsPerNode, err := ptptesthelper.GetPtpPodsPerNode()
	Expect(err).To(BeNil(), "failed to get ptp pods per node")

	rateTimeWindow := time.Duration(60 * time.Second)
	cadvisorScrapeInterval, err := ptptesthelper.GetCadvisorScrapeInterval()
	Expect(err).To(BeNil(), "failed to get cadvisor's prometheus scrape interval")

	logrus.Infof("Configured rate timeWindow: %s, cadvisor scrape interval: %d secs.", rateTimeWindow, cadvisorScrapeInterval)
	// Make sure the configured time interval for prometheus's rate() func is at least twice
	// the current scrape interval for the kubelet's cadvisor endpoint. Otherwise, rate() will
	// never get the minimum samples number (2) to work.
	Expect(int(rateTimeWindow.Seconds())).To(BeNumerically(">=", cadvisorScrapeInterval*2),
		fmt.Sprintf("configured time window (%s) is lower than twice the cadvisor scraping interval (%d secs)",
			rateTimeWindow, cadvisorScrapeInterval))

	_, err = params.PromRateTimeWindow()
	Expect(err).To(BeNil(), "Invalid prometheus time window for prometheus' rate function.")

	// Warmup: waiting until prometheus can scrape a couple of cpu samples from ptp pods.
	warmupTime := time.Duration(2*cadvisorScrapeInterval) * time.Second
	By(fmt.Sprintf("Waiting %s so prometheus can get at least 2 metric samples from the ptp pods.", warmupTime))

	time.Sleep(warmupTime)

	// Create timer channel for test case timeout.
	testCaseDuration := time.Duration(params.TestSpec.Duration) * time.Minute
	tcEndChan := time.After(testCaseDuration)

	// Create ticker for cpu usage checker function.
	cpuUsageCheckTicker := time.NewTicker(5 * time.Second) // cpuUsageCheckingInterval)

	logrus.Infof("Running test for %s (failure threshold: %d)", testCaseDuration.String(), failureThreshold)

	failureCounter := 0
	for {
		select {
		case <-tcEndChan:
			// TC ended: report & return.
			logrus.Infof("CPU utilization threshold reached %d times.", failureCounter)
			return
		case <-cpuUsageCheckTicker.C:
			logrus.Infof("Retrieving cpu usage of the ptp pods.")

			thresholdReached, err := isCpuUsageThresholdReachedInPtpPods(prometheusPod, ptpPodsPerNode, &params)
			Expect(err).To(BeNil(), "failed to get cpu usage")

			if thresholdReached {
				failureCounter++
				Expect(failureCounter).To(BeNumerically("<", failureThreshold),
					fmt.Sprintf("Failure threshold (%d) reached", failureThreshold))
			}
		}
	}
}

// isCpuUsageThresholdReachedInPtpPods is a helper that checks whether the cpu usage of
// each node, pod and or container is below preconfigured (via yaml) threshold/s.
func isCpuUsageThresholdReachedInPtpPods(prometheusPod *v1core.Pod, ptpPodsPerNode map[string][]*v1core.Pod, targets *ptptestconfig.CpuUtilizationTestConfig) (bool, error) {
	thresholdReached := false

	// No need to check error for the rateTimeWindow: it was already checked.
	rateTimeWindow, _ := targets.PromRateTimeWindow()

	checkNodeTotalCpuUsage, nodeCpuUsageThreshold := targets.ShouldCheckNodeTotalCpuUsage()

	for nodeName, ptpPods := range ptpPodsPerNode {
		nodeTotalCpuUsage := float64(0)

		for i := range ptpPods {
			pod := ptpPods[i]

			cpuUsage, err := ptptesthelper.GetPodTotalCpuUsage(pod.Name, pod.Namespace, rateTimeWindow, prometheusPod)
			if err != nil {
				return false, fmt.Errorf("failed to get total cpu usage for ptp pods on node %s: %w", nodeName, err)
			}

			logrus.Infof("Node %s: pod: %s ns:%s cpu usage: %.5f", nodeName, pod.Name, pod.Namespace, cpuUsage)

			// Accumulate ptp pod cpu usage for this node.
			nodeTotalCpuUsage += cpuUsage

			// Should we check the total cpu usage for this pod?
			checkCpuUsage, cpuUsageThreshold := targets.ShouldCheckPodCpuUsage(pod.Name)
			if checkCpuUsage && cpuUsage > cpuUsageThreshold {
				logrus.Infof("Node %s: ptp pod %s cpu usage %.5f is higher than threshold %v", nodeName, pod.Name, cpuUsage, cpuUsageThreshold)
				thresholdReached = true
			}

			for i := range pod.Spec.Containers {
				container := &pod.Spec.Containers[i]
				cpuUsage, err := ptptesthelper.GetContainerCpuUsage(pod.Name, container.Name, pod.Namespace, rateTimeWindow, prometheusPod)
				if err != nil {
					return false, fmt.Errorf("failed to get total cpu usage for ptp pods on node %s: %w", nodeName, err)
				}

				// Should we check the total cpu usage for this container?
				checkCpuUsage, cpuUsageThreshold := targets.ShouldCheckContainerCpuUsage(pod.Name, container.Name)
				if checkCpuUsage && cpuUsage > cpuUsageThreshold {
					logrus.Infof("Node %s: ptp container %s (pod %s) cpu usage %.5f is higher than threshold %v",
						nodeName, container.Name, pod.Name, cpuUsage, cpuUsageThreshold)
					thresholdReached = true
				}
			}

			if checkNodeTotalCpuUsage {
				logrus.Infof("Node cpu usage check enabled. Node %s, cpu:%v, threshold:%v", nodeName, nodeTotalCpuUsage, nodeCpuUsageThreshold)
				if nodeTotalCpuUsage > nodeCpuUsageThreshold {
					logrus.Infof("Node %s: ptp pods cpu usage %.5f is higher than threshold %v",
						nodeName, nodeTotalCpuUsage, nodeCpuUsageThreshold)
					thresholdReached = true
				}
			}
		}
	}

	return thresholdReached, nil
}

// Implementation for continuous testing of clock synchronization of the clock under test
func testSyncState(soakTestConfig ptptestconfig.SoakTestConfig) {

	slaveClockSyncTestSpec := soakTestConfig.SlaveClockSyncConfig.TestSpec
	logrus.Infof("%+v", slaveClockSyncTestSpec)
	syncEvents := ""
	// Create timer channel for test case timeout.
	testCaseDuration := time.Duration(slaveClockSyncTestSpec.Duration) * time.Minute
	tcEndChan := time.After(testCaseDuration)
	// registers channel to receive OsClockSyncStateChange events using the ptp-listener-lib
	tcEventChan, subscriberID := lib.Ps.Subscribe(string(ptpEvent.OsClockSyncStateChange))
	// unsubscribe event type when finished
	defer lib.Ps.Unsubscribe(string(ptpEvent.OsClockSyncStateChange), subscriberID)
	// creates and push an initial event indicating the initial state of the clock
	// otherwise no events would be received as long as the clock is not changing states
	lib.PushInitialEvent(string(ptpEvent.OsClockSyncState))
	// counts number of times the clock state looses LOCKED state
	failureCounter := 0
	wasLocked := false
	for {
		select {
		case <-tcEndChan:
			// The os clock never reach LOCKED status and the test has timed out
			if !wasLocked {
				Fail("OS Clock was never LOCKED and test timed out")
			}
			// Test case timeout, pushing metrics
			logrus.Infof("Clock Sync failed %d times.", failureCounter)
			logrus.Infof("%s", syncEvents)
			ptphelper.SaveStoreEventsToFile(syncEvents, soakTestConfig.EventOutputFile)
			return
		case singleEvent := <-tcEventChan:
			// New OsClockSyncStateChange event received
			logrus.Infof("Received a new OsClockSyncStateChange event")
			logrus.Infof("got %v\n", singleEvent)
			// get event values
			values, _ := singleEvent[exports.EventValues].(exports.StoredEventValues)
			state, _ := values["notification"].(string)
			clockOffset, _ := values["metric"].(float64)
			// create a pseudo value mapping a state to an integer (for vizualization)
			eventString := fmt.Sprintf("%s,%f,%s,%d\n", ptpEvent.OsClockSyncStateChange, clockOffset, state, exports.ToLockStateValue[state])
			// start counting loss of LOCK only after the clock was locked once
			logrus.Infof("clockOffset=%f", clockOffset)
			if state != "LOCKED" && wasLocked {
				failureCounter++
			}

			// Wait for the clock to be locked at least once before stating to count failures
			if !wasLocked && state == "LOCKED" {
				wasLocked = true
				logrus.Info("Clock is locked, starting to monitor status now")
			}

			// wait before the clock was locked once before starting to record metrics
			if wasLocked {
				syncEvents += eventString
			}

			// if the number of loss of lock events exceed test threshold, fail the test and end immediately
			if failureCounter >= slaveClockSyncTestSpec.FailureThreshold {
				// add the events to the junit report
				AddReportEntry(fmt.Sprintf("%v", syncEvents))
				// save events to file
				ptphelper.SaveStoreEventsToFile(syncEvents, soakTestConfig.EventOutputFile)
				// fail the test
				Expect(failureCounter).To(BeNumerically("<", slaveClockSyncTestSpec.FailureThreshold),
					fmt.Sprintf("Failure threshold (%d) reached", slaveClockSyncTestSpec.FailureThreshold))
			}
		}
	}
}
