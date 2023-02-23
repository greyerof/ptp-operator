package test

import (
	"strings"
	"time"
)

type CpuUsageNodeCfg struct {
	CpuUsageThreshold int `yaml:"cpu_threshold_mcores"`
}

type CpuUsagePodCfg struct {
	PodType           string `yaml:"pod_type"`
	ContainerName     string `yaml:"container,omitempty"`
	CpuUsageThreshold int    `yaml:"cpu_threshold_mcores"`
}

type CpuUsageCfg struct {
	PromTimeWindow string            `yaml:"prometheus_rate_time_window"`
	NodeCfg        *CpuUsageNodeCfg  `yaml:"node,omitempty"`
	PodCfg         *[]CpuUsagePodCfg `yaml:"pod,omitempty"`
}

type CpuUtilizationTestConfig struct {
	TestSpec struct {
		TestSpec
		CustomParams CpuUsageCfg `yaml:"custom_params"`
	} `yaml:"spec"`
	Description string `yaml:"desc"`
}

func (config *CpuUtilizationTestConfig) PromRateTimeWindow() (time.Duration, error) {
	return time.ParseDuration(config.TestSpec.CustomParams.PromTimeWindow)
}

func (config *CpuUtilizationTestConfig) ShouldCheckNodeTotalCpuUsage() (bool, float64) {
	if config.TestSpec.CustomParams.NodeCfg == nil {
		return false, 0
	}

	return true, float64(config.TestSpec.CustomParams.NodeCfg.CpuUsageThreshold) / 1000
}

func (config *CpuUtilizationTestConfig) ShouldCheckContainerCpuUsage(podName, containerName string) (bool, float64) {
	if config.TestSpec.CustomParams.PodCfg == nil {
		return false, 0
	}

	for _, pod := range *config.TestSpec.CustomParams.PodCfg {
		if pod.ContainerName == containerName && strings.Contains(podName, pod.PodType) {
			return true, float64(pod.CpuUsageThreshold) / 1000
		}
	}

	// Pod type not found.
	return false, 0
}

func (config *CpuUtilizationTestConfig) ShouldCheckPodCpuUsage(podName string) (bool, float64) {
	return config.ShouldCheckContainerCpuUsage(podName, "")
}
