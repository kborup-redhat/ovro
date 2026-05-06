---
title: "Chapter 10: Console Plugin"
order: 10
---

# Chapter 10: Console Plugin

## Introduction

The Console Plugin is the user-facing side of OVRO — a React/TypeScript application that runs inside the OpenShift Console as a dynamic plugin. It provides a tabbed interface for browsing recommendations, managing VMs, adjusting policy, and triggering rightsizing actions. Think of it as the dashboard on a car: all the instruments and controls the driver (cluster admin) needs, backed by the engine (API server) we built in previous chapters.

## OpenShift Dynamic Plugin Architecture

OpenShift Console dynamic plugins are self-contained React applications served from their own pod. The console loads them at runtime via the `ConsolePlugin` CRD. Key integration points:

- **`console-extensions.json`** — declares what the plugin adds to the console (nav items, pages).
- **`consoleFetch`** — an SDK function that makes API calls through the console's proxy, automatically including the user's bearer token.
- **Service proxy** — the console proxies requests to the plugin's backend service, configured in the `ConsolePlugin` resource.

## Project Structure

```
console-plugin/src/
  api/
    client.ts         # API client wrapping consoleFetch
  components/
    RightsizingNavPage.tsx    # Main page with tab navigation
    OverviewPage.tsx          # Dashboard cards
    RecommendationsPage.tsx   # Recommendation table
    RightsizeDialog.tsx       # Apply/approval dialog
    AllVMsPage.tsx            # VM list with exclude controls
    ExcludedVMsPage.tsx       # Excluded VM list
    PolicyPage.tsx            # Policy configuration form
  types.ts            # TypeScript interfaces matching Go CRD types
  index.ts            # Plugin entry point
```

## API Client

The API client wraps `consoleFetch` to provide typed access to the backend:

```typescript
// src/api/client.ts

const API_BASE = '/api/proxy/plugin/ovro-console-plugin/ovro-backend/api/v1';

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await consoleFetch(url, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...options?.headers },
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`API error ${response.status}: ${text}`);
  }
  return response.json();
}
```

The `API_BASE` path goes through the OpenShift Console proxy: `/api/proxy/plugin/<plugin-name>/<service-name>/...`. The console forwards the request to the plugin's backend service.

### Apply Result

The `applyRecommendation` function handles both direct-apply (200) and approval (202) responses:

```typescript
export interface ApplyResult {
  awaitingApproval: boolean;
  owner?: string;
  message?: string;
  approvalUrl?: string;
  recommendation?: RightsizingRecommendation;
}

export async function applyRecommendation(...): Promise<ApplyResult> {
  const response = await consoleFetch(...);
  const data = await response.json();
  if (response.status === 202) {
    return { awaitingApproval: true, owner: data.owner, approvalUrl: data.approvalUrl };
  }
  return { awaitingApproval: false, recommendation: data };
}
```

## Main Page: Tab Navigation

```typescript
// src/components/RightsizingNavPage.tsx

const RightsizingNavPage: React.FC = () => {
  const [activeTab, setActiveTab] = useState(0);
  const [dialogRec, setDialogRec] = useState<RightsizingRecommendation | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

  return (
    <Page>
      <Tabs activeKey={activeTab} onSelect={(_e, k) => setActiveTab(k as number)}>
        <Tab eventKey={0} title={<TabTitleText>Overview</TabTitleText>} />
        <Tab eventKey={1} title={<TabTitleText>Recommendations</TabTitleText>} />
        <Tab eventKey={2} title={<TabTitleText>Virtual Machines</TabTitleText>} />
        <Tab eventKey={3} title={<TabTitleText>Excluded VMs</TabTitleText>} />
        <Tab eventKey={4} title={<TabTitleText>Policy</TabTitleText>} />
      </Tabs>
      {/* Tab content and dialog */}
    </Page>
  );
};
```

Five tabs provide different views. The `refreshKey` counter forces the Recommendations tab to re-fetch data after an apply/revert operation.

## Recommendations Table

The Recommendations page shows a sortable, searchable table with action buttons:

- **Pending** — "Rightsize" button opens the dialog.
- **Awaiting Approval** — "Cancel Approval" button (admin can cancel).
- **Applied** — "Revert" button.
- **Applied Pending Restart** — disabled "Restart Pending" button with tooltip.

Colour-coded labels indicate direction (green for downsize, red for upsize) and state.

## Rightsize Dialog

The dialog is the most interactive component:

```typescript
// src/components/RightsizeDialog.tsx

export const RightsizeDialog: React.FC<Props> = ({ recommendation, isOpen, onClose, onApplied }) => {
  const [restartOption, setRestartOption] = useState<RestartOption>('now');
  const [awaitingApproval, setAwaitingApproval] = useState(false);
  const [approvalUrl, setApprovalUrl] = useState('');
  // ...
```

It shows:
1. **Hotplug status** — info/warning alert about whether a restart is needed.
2. **Resource comparison table** — current vs. recommended CPU and memory.
3. **Utilisation metrics** — P95 and lookback period.
4. **Restart options** — for non-hotplug VMs: now, schedule, or later.

After clicking Apply, if the backend returns 202 (awaiting approval), the dialog transitions to show the approval URL with a `ClipboardCopy` component and a link to open the approval page:

```tsx
{approvalUrl && (
  <StackItem>
    <strong>Approval URL:</strong>
    <ClipboardCopy isReadOnly variant={ClipboardCopyVariant.expansion}>
      {approvalUrl}
    </ClipboardCopy>
    <Button variant="link" component="a" href={approvalUrl} target="_blank">
      Open approval page
    </Button>
  </StackItem>
)}
```

## Overview Dashboard

Four PatternFly cards summarise the cluster state:
- **Total VMs Monitored** — count of all recommendations.
- **Downsize Candidates** — pending downsize recommendations with total savings.
- **Upsize Needed** — VMs above the utilisation threshold.
- **Applied Today** — changes applied in the last 24 hours.

## Policy Page

A form with PatternFly inputs for editing the `RightsizingPolicy`:
- Lookback days, percentile, headroom
- CPU/memory savings thresholds
- Upsize utilisation threshold
- Reconcile interval
- Revert retention
- Auto mode settings (enable, cron schedule, require approval)

Validation happens server-side in the API server.

## TypeScript Types

The `types.ts` file mirrors the Go CRD types:

```typescript
export interface RightsizingRecommendation {
  metadata: { name: string; namespace: string; creationTimestamp: string };
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
    owner?: string;
    reminderSentAt?: string | null;
    // ...
  };
}
```

## Key Takeaways

- The console plugin uses `consoleFetch` for authenticated API calls through the console proxy.
- Five tabs provide overview, recommendations, VM management, exclusions, and policy editing.
- The Rightsize dialog handles both direct-apply and approval workflow flows.
- PatternFly 5 components (Tables, Cards, Modals, ClipboardCopy) provide a consistent OpenShift look and feel.
- TypeScript types mirror Go CRD types for type safety across the stack.

## Summary

You've now walked through the entire OVRO architecture: from Prometheus metrics collection, through the rightsizing calculator, into the Kubernetes controller reconciliation loop, across the REST API with RBAC enforcement, through the multi-channel notification and JWT-based approval workflow, and finally into the OpenShift Console plugin that ties it all together. Each component has a focused responsibility, communicates through well-defined interfaces, and can be tested independently.
