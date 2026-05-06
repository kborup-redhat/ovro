---
title: "Chapter 7: Console Plugin"
order: 7
---

# Chapter 7: Console Plugin

In the previous chapters we built a complete backend: CRDs to model recommendations, a Prometheus client to gather metrics, a calculator to turn those metrics into actionable resize proposals, controllers to drive the state machine, and a REST API secured by Kubernetes RBAC. All of that machinery works, but it is invisible. A cluster administrator who wants to review recommendations would need to run `oc get rightsizingrecommendations`, parse YAML output, and manually construct `curl` commands against the API to approve or revert changes.

That is not a great experience. What administrators need is a **patient portal** -- a visual interface where they can see the full picture of their VM fleet at a glance, review individual recommendations with the supporting metrics evidence, approve treatments with one click, and adjust the operational policy through a simple form. They need this interface to live inside the tool they already use every day: the OpenShift Console.

That is exactly what the OVRO Console Plugin provides.

## What Is an OpenShift Console Dynamic Plugin?

An OpenShift Console **dynamic plugin** is a mechanism for extending the OpenShift web console without modifying its source code. Your plugin ships as a separate container image with its own webpack-bundled JavaScript and CSS. At runtime, the Console discovers your plugin through a `ConsolePlugin` Custom Resource, loads your code on demand, and renders your components alongside the built-in console pages.

The key properties of a dynamic plugin:

- **Decoupled deployment** -- Your plugin has its own Pod, Service, and container image. You can update it independently of the Console itself.
- **Extension points** -- The Console SDK defines a set of extension types (navigation items, resource pages, dashboards, actions, etc.) that your plugin can register. You declare what you want to add, and the Console wires it in.
- **Proxy routing** -- The Console can proxy API requests from your plugin's frontend to your plugin's backend, so your JavaScript code never needs to know the cluster-internal Service URL. It just calls a well-known path prefix and the Console handles the rest.
- **PatternFly consistency** -- Because every plugin uses the same PatternFly design system as the Console itself, your pages look and feel native. Users cannot tell where the built-in Console ends and your plugin begins.

Think of it like browser extensions: Chrome provides the framework and extension APIs, and extensions add new functionality without modifying Chrome's source code. The OpenShift Console provides the framework, and dynamic plugins add new pages and capabilities without touching the Console's codebase.

## The ConsolePlugin CR and Proxy Routing

When OVRO is deployed, a `ConsolePlugin` CR named `ovro-console-plugin` tells the Console two things: where to fetch the plugin's static assets (JavaScript bundles), and how to proxy API requests to the operator's REST API.

The proxy configuration means that inside the plugin's frontend code, every API call uses a path prefix that the Console intercepts and forwards:

```
/api/plugins/ovro-console-plugin/api/v1/...
```

The Console reads the `ConsolePlugin` CR, sees that requests matching this prefix should be forwarded to the OVRO operator's Service, and proxies them transparently. The user's authentication token is forwarded along with the request, which is how the backend's RBAC middleware (covered in [Chapter 6](06-API-Server.md)) receives a valid bearer token for every call.

This design means the frontend code never needs to discover the operator's in-cluster address or manage authentication tokens directly. It just calls `fetch('/api/plugins/ovro-console-plugin/api/v1/overview')` and the infrastructure handles the rest.

## PatternFly -- Red Hat's Design System

Every component in the OVRO Console Plugin is built with [PatternFly](https://www.patternfly.org/), Red Hat's open-source design system. PatternFly provides a comprehensive library of React components -- buttons, tables, forms, cards, modals, alerts, toolbars, and more -- all following consistent visual and interaction patterns.

Using PatternFly is not optional for Console plugins. It is what makes your pages look native within the OpenShift Console. When you use a PatternFly `Table`, it renders with the same fonts, spacing, and color palette as the built-in Console tables. When you use a PatternFly `Modal`, it behaves identically to the modals in the Console's own resource editors.

Throughout this chapter, you will see imports from `@patternfly/react-core` (layout components, buttons, forms, alerts) and `@patternfly/react-table` (data tables). These are the two PatternFly packages the plugin uses most heavily.

## File Overview

The Console Plugin's source code is organized into three layers:

| Layer | Files | Responsibility |
|---|---|---|
| **Types** | `types.ts` | TypeScript interfaces mirroring the Go CRD types |
| **API client** | `api/client.ts` | HTTP functions wrapping every REST endpoint |
| **Components** | `components/*.tsx` | React components for each page and dialog |

Let us walk through each layer, starting at the bottom.

## TypeScript Types (`types.ts`)

**Source file:** `console-plugin/src/types.ts`

The frontend needs its own type definitions that mirror the Go structs from [Chapter 1](01-CRD-Types.md). TypeScript interfaces serve the same purpose as Go struct definitions -- they describe the shape of data -- but they are checked at compile time rather than at runtime.

```typescript
export type RecommendationDirection = 'downsize' | 'upsize';

export type RecommendationState =
  | 'pending'
  | 'approved'
  | 'applied-pending-restart'
  | 'applied'
  | 'reverted'
  | 'failed';
```

These union types are the TypeScript equivalent of Go's `const` block with string enumerations. The compiler enforces that any variable of type `RecommendationState` can only hold one of these six values -- a typo like `'appleid'` would be caught at build time.

The main data structure is `RightsizingRecommendation`, which mirrors the Go CRD type field by field:

```typescript
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
```

Notice how the Go types translate to TypeScript:

- Go's `metav1.Time` becomes `string | null` (JSON serializes timestamps as ISO 8601 strings)
- Go's `resource.Quantity` becomes `string` (the API transmits quantities like `"4Gi"` as strings)
- Go's `int32` becomes `number` (TypeScript has a single numeric type)
- Go's pointer fields (`*metav1.Time`, `*ResourceSpec`) become union types with `| null`

The file also defines `OverviewData`, `RightsizingPolicy`, `RightsizingPolicySpec`, and a `RestartOption` type (`'now' | 'schedule' | 'later'`). These types flow through the entire frontend -- every API response is typed, every component prop is typed, and every state variable is typed. This creates a chain of compile-time safety from the API boundary all the way to the rendered UI.

## API Client (`api/client.ts`)

**Source file:** `console-plugin/src/api/client.ts`

The API client is the single point of contact between the frontend and the backend REST API. Every HTTP request flows through this module, and every component imports its functions rather than calling `fetch` directly.

### The `fetchJSON` Wrapper

At the foundation is a generic wrapper around the browser's `fetch` API:

```typescript
const API_BASE = '/api/plugins/ovro-console-plugin/api/v1';

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`API error ${response.status}: ${text}`);
  }
  return response.json();
}
```

Several design decisions are worth noting here:

1. **`API_BASE` is a constant.** All endpoints are relative to this proxy path. If the plugin name changes, only this one line needs updating.

2. **Generic type parameter `<T>`.** The function returns `Promise<T>`, so callers get type-safe responses: `fetchJSON<OverviewData>(...)` returns a `Promise<OverviewData>`, and the compiler knows the shape of the resolved value.

3. **Automatic `Content-Type` header.** Every request sends `application/json`, and callers can override or add headers through the `options` parameter.

4. **Consistent error handling.** If the response status is not in the 2xx range, the function reads the response body as text and throws an `Error` with both the status code and the server's error message. This means every calling component can use a single `catch` block to handle API failures, and the error message is always human-readable.

5. **No authentication logic.** The Console proxy attaches the user's bearer token automatically. The frontend code does not need to manage tokens at all.

### Endpoint Functions

The rest of the module exports one function per API endpoint. Each function is a thin wrapper that constructs the URL, calls `fetchJSON` with the appropriate HTTP method and body, and returns the typed result:

- **`listRecommendations(filters?)`** -- GET with optional query parameters for namespace, direction, and state filtering
- **`getRecommendation(namespace, name)`** -- GET a single recommendation by namespace and name
- **`applyRecommendation(namespace, name, restartOption, scheduledAt?)`** -- POST to apply a recommendation, with the restart strategy and optional scheduled time in the request body
- **`revertRecommendation(namespace, name)`** -- POST to revert an applied recommendation
- **`excludeVM(namespace, name)`** -- POST to add the exclude annotation to a VM
- **`removeExclusion(namespace, name)`** -- DELETE to remove the exclude annotation
- **`getOverview()`** -- GET aggregate dashboard statistics
- **`getPolicy()`** -- GET the current policy
- **`updatePolicy(spec)`** -- PUT to save a modified policy

This pattern -- one exported async function per endpoint, all using the same `fetchJSON` foundation -- keeps the API surface clean and testable. Components never construct URLs or parse responses themselves.

## The Main Navigation Page (`RightsizingNavPage.tsx`)

**Source file:** `console-plugin/src/components/RightsizingNavPage.tsx`

This is the entry point for the entire plugin UI. When a user navigates to the Rightsizing section in the OpenShift Console, this component renders the page header and a tab bar with four tabs, each containing a sub-page component.

```typescript
const RightsizingNavPage: React.FC = () => {
  const [activeTab, setActiveTab] = useState(0);
  const [dialogRec, setDialogRec] = useState<RightsizingRecommendation | null>(null);

  return (
    <>
      <PageSection>
        <Title headingLevel="h1">Rightsizing</Title>
      </PageSection>
      <PageSection type="tabs">
        <Tabs activeKey={activeTab} onSelect={(_e, k) => setActiveTab(k as number)}>
          <Tab eventKey={0} title={<TabTitleText>Overview</TabTitleText>}>
            <OverviewPage />
          </Tab>
          <Tab eventKey={1} title={<TabTitleText>Recommendations</TabTitleText>}>
            <RecommendationsPage onRightsize={setDialogRec} />
          </Tab>
          <Tab eventKey={2} title={<TabTitleText>Excluded VMs</TabTitleText>}>
            <ExcludedVMsPage />
          </Tab>
          <Tab eventKey={3} title={<TabTitleText>Policy</TabTitleText>}>
            <PolicyPage />
          </Tab>
        </Tabs>
      </PageSection>

      {dialogRec && (
        <RightsizeDialog
          recommendation={dialogRec}
          isOpen={!!dialogRec}
          onClose={() => setDialogRec(null)}
          onApplied={() => setDialogRec(null)}
        />
      )}
    </>
  );
};
```

Two pieces of React state drive the entire page:

- **`activeTab`** -- An integer (0-3) tracking which tab is currently selected. PatternFly's `Tabs` component uses this to show/hide tab content.
- **`dialogRec`** -- Either `null` (no dialog open) or a `RightsizingRecommendation` object (the dialog is open for that recommendation). This state is **lifted up** to the navigation page because the dialog needs to appear as a modal overlay on top of the tab content, and the trigger for opening it comes from the `RecommendationsPage` tab.

The `RightsizeDialog` is conditionally rendered at the bottom. When `dialogRec` is `null`, nothing renders. When a user clicks "Rightsize" on a recommendation in the table, the `RecommendationsPage` calls `onRightsize(rec)`, which sets `dialogRec`, and the modal appears. When the dialog closes (either through cancellation or successful application), `dialogRec` is set back to `null` and the modal disappears.

This pattern -- lifting modal state to a parent component and rendering the modal outside the tab content -- is a standard React technique for ensuring modals overlay the entire page rather than being clipped by their parent container's layout.

## Overview Dashboard (`OverviewPage.tsx`)

**Source file:** `console-plugin/src/components/OverviewPage.tsx`

The Overview tab provides an at-a-glance dashboard with four metric cards. It follows a standard React data-loading pattern:

1. Initialize state with `useState`: `data` (null until loaded), `loading` (true), `error` (null unless something fails)
2. Fetch data in `useEffect` on mount: call `getOverview()`, set `data` on success, set `error` on failure, set `loading` to false in `finally`
3. Render conditionally: show a spinner while loading, an error message if the call failed, or the dashboard cards when data is available

The dashboard layout uses PatternFly's `Grid` component with four `GridItem` elements, each spanning 3 of 12 columns (creating a four-column layout):

| Card | Data Field | What It Shows |
|---|---|---|
| Total VMs Monitored | `totalVMs` | How many VMs OVRO is tracking |
| Downsize Candidates | `downsizeCandidates` | VMs that could be safely shrunk, with total CPU and memory savings |
| Upsize Needed | `upsizeNeeded` | VMs running above 90% utilization |
| Applied Today | `appliedToday` | How many recommendations were applied in the last 24 hours |

Each card uses a PatternFly `Card` with a `CardTitle` and `CardBody`. The key metric is displayed as a large heading (`Title` with `size="3xl"`), and supporting text provides context (like total savings figures or utilization thresholds).

The memory savings value arrives from the API in bytes and is converted to GiB for display:

```typescript
const memorySavingsGiB = Math.round(data.totalMemorySavings / (1024 * 1024 * 1024));
```

## Recommendations Table (`RecommendationsPage.tsx`)

**Source file:** `console-plugin/src/components/RecommendationsPage.tsx`

The Recommendations tab is the most complex page in the plugin. It displays a searchable, actionable table of all rightsizing recommendations across the cluster.

### Data Loading and Search

The component loads recommendations on mount using the same `useState`/`useEffect` pattern as the Overview page. A `loadData` function encapsulates the fetch logic so it can be called both on initial mount and after actions (like reverting a recommendation) to refresh the table.

A `SearchInput` toolbar lets users filter the table by VM name. The filtering is purely client-side:

```typescript
const filtered = recs.filter((r) =>
  r.spec.virtualMachineRef.name.toLowerCase().includes(search.toLowerCase()),
);
```

### Direction and State Labels

Each recommendation row displays color-coded labels for the direction and state:

- **Direction:** green `Label` for "Downsize" (saving resources is good), red `Label` for "Upsize" (the VM needs more resources -- action required)
- **State:** blue for "pending", green for "applied", orange for "applied-pending-restart", grey for "reverted", red for "failed"

These colors follow PatternFly's semantic color conventions and give administrators an instant visual summary of their fleet's health.

### Action Buttons -- The State Machine in the UI

The most interesting part of this component is how it translates the backend state machine into user actions. The `getActionButton` function uses a `switch` statement on the recommendation's state to determine which button to render:

```typescript
const getActionButton = (rec: RightsizingRecommendation) => {
  switch (rec.status.state) {
    case 'pending':
      return (
        <Button variant="primary" size="sm" onClick={() => onRightsize(rec)}>
          Rightsize
        </Button>
      );
    case 'applied-pending-restart':
      return (
        <Tooltip content="VM will restart at the scheduled time">
          <Button variant="warning" size="sm" isDisabled>Restart</Button>
        </Tooltip>
      );
    case 'applied':
      return (
        <Button variant="danger" size="sm" onClick={() => handleRevert(rec)}>
          Revert
        </Button>
      );
    default:
      return null;
  }
};
```

This function is a direct reflection of the state machine from [Chapter 1](01-CRD-Types.md):

| State | Button | Variant | Behavior |
|---|---|---|---|
| `pending` | "Rightsize" | Primary (blue) | Opens the RightsizeDialog to review and approve |
| `applied-pending-restart` | "Restart" | Warning (orange), disabled | Informational -- the restart is scheduled, no action needed |
| `applied` | "Revert" | Danger (red) | Immediately reverts to the original configuration |
| `reverted`, `failed`, `approved` | (none) | -- | No action available in these states |

The button variants communicate urgency through color: blue for the normal action path, orange for "waiting", and red for the destructive revert action. The disabled "Restart" button with a tooltip tells the user that the system is handling it -- they do not need to do anything.

### Error Handling

Errors are displayed as a PatternFly `Alert` component at the top of the page. Both the initial data load and the revert action update the same `error` state, so the user always sees the most recent error in a consistent location. The error is cleared (`setError(null)`) at the start of each new operation so stale errors do not linger.

## Rightsize Dialog (`RightsizeDialog.tsx`)

**Source file:** `console-plugin/src/components/RightsizeDialog.tsx`

When a user clicks "Rightsize" on a pending recommendation, this modal dialog opens. It is the point of decision -- the administrator reviews the proposed changes and chooses how to apply them.

### Hotplug Detection

The dialog's behavior changes significantly based on whether the target VM supports CPU/memory hotplug:

- **Hotplug-capable VMs** get an informational blue alert: "This VM supports CPU/memory hotplug. Changes will be applied live without downtime." The restart options are hidden because no restart is needed.
- **Non-hotplug VMs** get a warning orange alert: "This VM does not support hotplug. A restart is required to apply changes." The restart options are shown.

This distinction is critical for the user experience. Hotplug-capable VMs can be resized without any service interruption, which is a much lower-risk operation. Non-hotplug VMs require careful scheduling because the restart causes downtime.

### Resource Comparison Table

The dialog displays a compact comparison table showing exactly what will change:

| Resource | Current | | New | Saving |
|---|---|---|---|---|
| CPU Cores | 8 | --> | 4 | 4 cores |
| Memory | 16Gi | --> | 8Gi | 8Gi |

This table is built with PatternFly's `Table` component in `compact` variant. Below it, the dialog shows the metrics that justified the recommendation (lookback period, P95 CPU, P95 memory), so the administrator can verify the evidence before approving.

### Restart Options

For non-hotplug VMs, the dialog presents three restart strategies using PatternFly `Radio` components:

```typescript
{!isHotplug && (
  <StackItem>
    <Stack hasGutter>
      <StackItem><strong>When should the VM restart?</strong></StackItem>
      <StackItem>
        <Radio id="restart-now" name="restart"
          label="Restart now"
          description="Apply changes and restart immediately"
          isChecked={restartOption === 'now'}
          onChange={() => setRestartOption('now')} />
      </StackItem>
      <StackItem>
        <Radio id="restart-schedule" name="restart"
          label="Schedule restart"
          description="Pick a date and time"
          isChecked={restartOption === 'schedule'}
          onChange={() => setRestartOption('schedule')} />
        {restartOption === 'schedule' && (
          <TextInput type="datetime-local" value={scheduledAt}
            onChange={(_e, v) => setScheduledAt(v)}
            aria-label="Scheduled restart time" />
        )}
      </StackItem>
      <StackItem>
        <Radio id="restart-later" name="restart"
          label="Restart later"
          description="Apply spec changes now -- restart manually during your change window"
          isChecked={restartOption === 'later'}
          onChange={() => setRestartOption('later')} />
      </StackItem>
    </Stack>
  </StackItem>
)}
```

The three options map to distinct backend behaviors:

| Option | `RestartOption` | What Happens |
|---|---|---|
| Restart now | `'now'` | The VM spec is patched and the VM is immediately restarted |
| Schedule restart | `'schedule'` | The VM spec is patched, and the restart controller triggers the restart at the scheduled time |
| Restart later | `'later'` | The VM spec is patched but no restart is triggered; the recommendation enters `applied-pending-restart` state and waits for a manual restart |

When "Schedule restart" is selected, a `datetime-local` input appears. The browser renders a native date/time picker appropriate for the user's locale.

### DateTime Conversion

The `datetime-local` input produces a value like `"2024-03-15T14:30"` (local time, no timezone). The backend API expects RFC 3339 format (e.g., `"2024-03-15T14:30:00Z"`). The dialog handles this conversion:

```typescript
const scheduledRFC3339 = restartOption === 'schedule' && scheduledAt
  ? new Date(scheduledAt).toISOString()
  : undefined;
```

`new Date(scheduledAt)` interprets the `datetime-local` string in the user's local timezone, and `.toISOString()` converts it to a UTC-based ISO 8601 / RFC 3339 string. This ensures the backend always receives an unambiguous timestamp regardless of the user's timezone.

### Apply Logic

The `handleApply` function ties everything together:

```typescript
const handleApply = async () => {
  setApplying(true);
  setError(null);
  try {
    const option = isHotplug ? 'now' : restartOption;
    const scheduledRFC3339 = restartOption === 'schedule' && scheduledAt
      ? new Date(scheduledAt).toISOString()
      : undefined;
    await applyRecommendation(
      rec.metadata.namespace,
      rec.metadata.name,
      option,
      scheduledRFC3339,
    );
    onApplied();
    onClose();
  } catch (e: unknown) {
    setError(e instanceof Error ? e.message : 'Unknown error');
  } finally {
    setApplying(false);
  }
};
```

Note the `isHotplug ? 'now' : restartOption` logic: for hotplug-capable VMs, the restart option is always forced to `'now'` regardless of what the user might have selected, because hotplug changes are applied live and the concept of "scheduling a restart" does not apply.

The button text also reflects the hotplug status: "Apply Now (Live)" for hotplug VMs, "Apply Changes" for non-hotplug VMs.

## Excluded VMs Page (`ExcludedVMsPage.tsx`)

**Source file:** `console-plugin/src/components/ExcludedVMsPage.tsx`

The Excluded VMs tab shows VMs that have been opted out of rightsizing via the `rightsizing.redhatconsulting.io/exclude` annotation. This page has a simpler structure than the Recommendations page -- it is a searchable table with a single action per row.

Each row shows the VM name, namespace, and the date the exclusion was added. The sole action is a "Resume Monitoring" button that calls `removeExclusion(namespace, name)` to delete the exclude annotation. On success, the VM is removed from the local state array and disappears from the table immediately, without waiting for a full data reload.

When no VMs are excluded, the page displays a PatternFly `EmptyState` component with the message "No VMs are currently excluded from rightsizing monitoring" -- a clear signal that the absence of data is expected rather than an error.

## Policy Settings (`PolicyPage.tsx`)

**Source file:** `console-plugin/src/components/PolicyPage.tsx`

The Policy tab provides a form for editing the cluster-wide `RightsizingPolicy` CR. It is organized into four logical sections using PatternFly's `FormSection` component:

### Analysis Settings

| Field | Type | Controls |
|---|---|---|
| Lookback Period (days) | Number | How many days of metrics history to analyze |
| Percentile for Calculation | Number | Which percentile of utilization to use as the baseline |
| Headroom (%) | Number | Additional safety margin above the calculated size |
| Reconcile Interval (minutes) | Number | How often the controller re-evaluates all VMs |

### Thresholds

| Field | Type | Controls |
|---|---|---|
| Min CPU Savings (cores) | Number | Minimum cores saved before generating a recommendation |
| Min Memory Savings (GiB) | Text | Minimum memory saved (Kubernetes quantity format) |
| Upsize Utilization Threshold (%) | Number | Utilization above which an upsize is recommended |

### Revert Settings

| Field | Type | Controls |
|---|---|---|
| Revert Retention (days) | Number | How long the rollback option remains available |

### Auto Mode

| Field | Type | Controls |
|---|---|---|
| Enable Auto Rightsizing | Switch | Toggle automatic application of recommendations |
| Schedule (cron) | Text | Cron expression for when auto-mode runs |
| Require VM Auto-Approve Annotation | Switch | Whether VMs need an opt-in annotation for auto-mode |

The Auto Mode fields are conditionally disabled when the master switch is off (`isDisabled={!spec.autoMode.enabled}`), preventing users from configuring a schedule for a feature that is not active.

### State Management

The component initializes with a `defaultSpec` that mirrors the Go backend's `DefaultPolicySpec()` function:

```typescript
const defaultSpec: RightsizingPolicySpec = {
  lookbackDays: 14,
  algorithm: { percentile: 95, headroomPercent: 20 },
  thresholds: { minCpuSavings: 1, minMemorySavings: '1Gi', upsizeUtilizationPercent: 90 },
  revertRetentionDays: 30,
  autoMode: { enabled: false, schedule: '0 2 * * *', requireApproval: true },
  reconcileIntervalMinutes: 60,
};
```

On mount, the component calls `getPolicy()` to load the current policy from the cluster. If that call fails (e.g., no policy exists yet), it falls back to the `defaultSpec` -- the form is still usable with sensible defaults.

Each form field updates the `spec` state using spread syntax to immutably update nested objects:

```typescript
onChange={(_e, v) => setSpec({
  ...spec,
  algorithm: { ...spec.algorithm, percentile: parseInt(v) || 0 }
})}
```

The "Save Policy" button calls `updatePolicy(spec)`, and a "Reset" button restores the form to `defaultSpec`. Success and error feedback are displayed as inline PatternFly alerts below the form.

## React Patterns Used Throughout

Several React patterns appear consistently across all the plugin's components. Understanding them will help you read and modify any page.

### useState for Local State

Every component manages its own state with `useState`. The typical set is:

- `data` or domain-specific state (recommendations, overview data, policy spec)
- `loading` -- boolean, true during the initial fetch
- `error` -- `string | null`, set when an API call fails

### useEffect for Data Loading

Data is fetched in a `useEffect` hook with an empty dependency array (`[]`), meaning it runs once when the component mounts:

```typescript
useEffect(() => {
  getOverview()
    .then(setData)
    .catch((e) => setError(e.message))
    .finally(() => setLoading(false));
}, []);
```

The `.finally()` ensures `loading` is set to false whether the call succeeds or fails, so the spinner always disappears.

### async/await with try/catch

For user-triggered actions (apply, revert, save policy), the components use `async/await` with `try/catch` blocks. This pattern provides cleaner control flow than promise chains when there are multiple sequential steps:

```typescript
const handleSave = async () => {
  setSaving(true);
  setError(null);
  try {
    await updatePolicy(spec);
    setSuccess(true);
  } catch (e: unknown) {
    setError(e instanceof Error ? e.message : 'Failed to save policy');
  } finally {
    setSaving(false);
  }
};
```

The pattern is always the same: set loading/saving to true, clear previous errors, try the operation, catch and display errors, and always reset the loading state in `finally`.

### Callback Props for Cross-Component Communication

The `RightsizingNavPage` passes `setDialogRec` as the `onRightsize` prop to `RecommendationsPage`. This is a standard React pattern called "lifting state up" -- the child does not own the dialog state, it just signals the parent when a user action should open the dialog. This keeps the `RecommendationsPage` focused on table rendering and the `RightsizingNavPage` responsible for modal orchestration.

## How the Frontend Reflects the Backend State Machine

The Console Plugin is not just a display layer -- it is a faithful representation of the backend state machine described in [Chapter 1](01-CRD-Types.md). Every transition in the state machine corresponds to a user action or visual change in the UI:

```
Backend State              UI Representation
-----------              -----------------
pending         -->      Blue "pending" label + "Rightsize" button
approved        -->      (transient, quickly moves to applied/applied-pending-restart)
applied-pend-   -->      Orange label + disabled "Restart" button with tooltip
  ing-restart
applied         -->      Green "applied" label + red "Revert" button
reverted        -->      Grey "reverted" label, no action buttons
failed          -->      Red "failed" label, no action buttons
```

The state machine enforces that only valid actions are available. You cannot revert a recommendation that has not been applied. You cannot rightsize a VM that is already applied-pending-restart. The backend enforces these constraints, and the frontend reflects them by simply not rendering buttons for invalid transitions.

This is the patient portal analogy in action: just as a medical portal shows "Prescription filled" with no refill button when the prescription is complete, the OVRO Console Plugin shows "applied" with a "Revert" button (the undo) but no "Rightsize" button (you cannot apply a treatment twice).

## Key Takeaways

1. **Dynamic plugins extend without modifying.** The OpenShift Console Plugin mechanism lets OVRO add a full rightsizing management UI to the Console without touching the Console's own source code. The `ConsolePlugin` CR registers the plugin, and proxy routing handles API communication transparently.

2. **Type safety flows end to end.** TypeScript interfaces in `types.ts` mirror the Go CRD types, and the generic `fetchJSON<T>` wrapper ensures every API response is typed. Compile-time checks catch mismatches between what the API returns and what the UI expects.

3. **One function per endpoint.** The API client module exports a clean, thin wrapper for each REST endpoint. Components never construct URLs or call `fetch` directly. This makes the API surface easy to test, easy to mock, and easy to update if the backend changes.

4. **The UI reflects the state machine.** The `getActionButton` function in the Recommendations page is a direct translation of the backend state machine into user-facing actions. Valid transitions become buttons; invalid transitions are simply absent.

5. **Hotplug awareness changes the dialog.** The RightsizeDialog adapts its entire UX based on whether the target VM supports hotplug: different alerts, different button labels, and whether restart options are shown at all. This ensures administrators always understand the impact of their action.

6. **PatternFly provides consistency.** Every component uses PatternFly's design system -- Grid, Card, Table, Modal, Form, Alert, Label, Radio, Switch -- so the plugin's pages are visually indistinguishable from built-in Console pages.

7. **Standard React patterns keep it simple.** The plugin uses `useState` for state, `useEffect` for data loading, callback props for cross-component communication, and `async/await` with `try/catch` for user actions. No state management libraries, no custom hooks, no unnecessary abstraction -- just the React fundamentals applied consistently.

## Next Chapter

In [Chapter 8: CI/CD and Deployment](08-CICD-Deployment.md), we will see how all the pieces -- the operator binary, the Console Plugin, the CRD manifests, and the container images -- come together in a Tekton pipeline that builds, tests, and deploys OVRO to an OpenShift cluster on every commit.
