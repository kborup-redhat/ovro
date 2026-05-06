export type RecommendationDirection = 'downsize' | 'upsize';

export type RecommendationState =
  | 'pending'
  | 'approved'
  | 'applied-pending-restart'
  | 'applied'
  | 'reverted'
  | 'failed';

export interface CPUSpec {
  cores: number;
  sockets: number;
  threads: number;
}

export interface ResourceSpec {
  cpu: CPUSpec;
  memory: string;
}

export interface MetricsSnapshot {
  lookbackDays: number;
  cpuP95Percent: number;
  memoryP95Percent: number;
  cpuMaxPercent: number;
  memoryMaxPercent: number;
}

export interface RightsizingRecommendation {
  metadata: {
    name: string;
    namespace: string;
    creationTimestamp: string;
  };
  spec: {
    virtualMachineRef: { name: string; namespace: string };
    direction: RecommendationDirection;
    current: ResourceSpec;
    recommended: ResourceSpec;
    savings: { cpu: number; memory: string };
    metrics: MetricsSnapshot;
    hotplugCapable: boolean;
  };
  status: {
    state: RecommendationState;
    lastCalculated: string | null;
    appliedAt: string | null;
    scheduledRestartAt: string | null;
    revertBefore: string | null;
    revertConfig: ResourceSpec | null;
    message: string;
  };
}

export interface OverviewData {
  totalVMs: number;
  downsizeCandidates: number;
  upsizeNeeded: number;
  appliedToday: number;
  totalCpuSavings: number;
  totalMemorySavings: number;
}

export interface RightsizingPolicySpec {
  lookbackDays: number;
  algorithm: { percentile: number; headroomPercent: number };
  thresholds: {
    minCpuSavings: number;
    minMemorySavings: string;
    upsizeUtilizationPercent: number;
  };
  revertRetentionDays: number;
  autoMode: {
    enabled: boolean;
    schedule: string;
    requireApproval: boolean;
  };
  reconcileIntervalMinutes: number;
}

export interface RightsizingPolicy {
  metadata: { name: string };
  spec: RightsizingPolicySpec;
}

export type RestartOption = 'now' | 'schedule' | 'later';
