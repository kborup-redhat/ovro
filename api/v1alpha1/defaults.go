package v1alpha1

import "k8s.io/apimachinery/pkg/api/resource"

const AnnotationExclude = "rightsizing.redhatconsulting.io/exclude"
const LabelOwner = "rightsizing.redhatconsulting.io/owner"

func DefaultPolicySpec() RightsizingPolicySpec {
	return RightsizingPolicySpec{
		LookbackDays: 14,
		Algorithm: AlgorithmSpec{
			Percentile:      95,
			HeadroomPercent: 20,
		},
		Thresholds: ThresholdsSpec{
			MinCPUSavings:            1,
			MinMemorySavings:         resource.MustParse("1Gi"),
			UpsizeUtilizationPercent: 90,
		},
		RevertRetentionDays:      30,
		ReconcileIntervalMinutes: 60,
		AutoMode: AutoModeSpec{
			Enabled:  false,
			Schedule: "0 2 * * *",
		},
	}
}
