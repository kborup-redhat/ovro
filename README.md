# OVRO — OpenShift Virtualization Rightsizing Operator

> **This is a proof-of-concept (POC) operator. It is not a Red Hat product and is not supported by Red Hat in any way. Use at your own risk.**

OVRO analyses CPU and memory utilisation of KubeVirt virtual machines on OpenShift, generates rightsizing recommendations, and lets cluster administrators apply or revert changes through an OpenShift Console dynamic plugin. It includes an optional approval workflow that routes changes through VM owners before applying, with notifications via Slack, Teams, email, and other channels.

## Features

- **Long-term metrics storage** — deploys VictoriaMetrics as a dedicated TSDB with 90-day retention, seeded with historical data from Prometheus at install time.
- **Automated analysis** — queries VictoriaMetrics (or Prometheus/Thanos) for P95 and max utilisation over a configurable lookback window.
- **Rightsizing calculator** — recommends downsize or upsize with configurable headroom, minimum-savings thresholds, and percentile tuning.
- **Hotplug-aware** — detects live CPU/memory hotplug capability; applies changes without restart when possible.
- **Console plugin** — browse recommendations, apply/revert changes, exclude VMs, and view cluster-wide savings from the OpenShift Console.
- **Approval workflow** — when a VM has an owner label, changes require owner approval via a signed token link before being applied.
- **Multi-channel notifications** — notify VM owners via Slack, Microsoft Teams, SMTP email, SNMP traps, PagerDuty, Rocket.Chat, or ServiceNow.
- **RBAC-enforced** — every API call is scoped to the namespaces the requesting user can access via TokenReview and SubjectAccessReview.
- **Scheduled restarts** — for non-hotplug VMs, schedule a restart window or trigger it immediately.
- **Policy-driven** — a cluster-scoped `RightsizingPolicy` CR controls lookback, percentile, headroom, thresholds, and reconcile interval.
- **Demo mode** — generate synthetic recommendations for all VMs without real Prometheus data, useful for demos and testing.

## Architecture

| Component | Description |
|-----------|-------------|
| **Recommendation controller** | Watches `VirtualMachine` objects, queries Prometheus, runs the calculator, and creates/updates `RightsizingRecommendation` CRs. |
| **Restart controller** | Watches recommendations in `applied-pending-restart` state and triggers VM restarts at the scheduled time. |
| **REST API server** | Serves the console plugin with filtered, RBAC-scoped data. Handles apply, revert, exclude, and approval operations. |
| **Console plugin** | React/TypeScript OpenShift dynamic plugin using PatternFly 5. |
| **Approval proxy** | Standalone container that validates JWT tokens and serves the owner approval page. Exposed via an OpenShift Route. |

### Approval Workflow

```
Admin clicks "Rightsize" on VM with owner label
  -> Backend sets state to "awaiting-approval"
  -> Backend sends notification via configured forwarders (Slack, email, etc.)
  -> Notification includes signed approval link
  -> Owner clicks link -> hits approval proxy (via Route)
  -> Proxy validates JWT -> serves approval page
  -> Owner approves/rejects -> proxy forwards to backend
  -> Backend applies or rejects the recommendation
```

Approval tokens expire after 14 days. A reminder notification is sent at 7 days (except to ServiceNow, since the ticket already exists).

## Container Images

OVRO uses the following container images. For disconnected/air-gapped environments, mirror these to your internal registry before installation.

| Image | Purpose |
|-------|---------|
| `<REGISTRY>/ovro-backend:<TAG>` | Operator, recommendation controller, REST API server (built from source) |
| `<REGISTRY>/ovro-console-plugin:<TAG>` | OpenShift Console dynamic plugin (built from source) |
| `<REGISTRY>/ovro-approval-proxy:<TAG>` | Approval workflow proxy (built from source, optional) |
| `docker.io/victoriametrics/victoria-metrics:v1.141.0` | Long-term metrics storage |
| `docker.io/victoriametrics/vmctl:v1.141.0` | One-time historical data seed job |

## Prerequisites

- OpenShift 4.14+ (tested on 4.18)
- OpenShift Virtualization (KubeVirt) installed with running VMs
- `oc` CLI logged in as cluster-admin
- A container image registry accessible from the cluster (e.g., the internal OpenShift registry or Quay)

## Installation

### 1. Create the namespace

```bash
oc apply -f deploy/namespace.yaml
```

This creates the `ovro-system` namespace where all OVRO components run.

### 2. Install CRDs

```bash
oc apply -f config/crd/bases/rightsizing.redhatconsulting.io_rightsizingrecommendations.yaml
oc apply -f config/crd/bases/rightsizing.redhatconsulting.io_rightsizingpolicies.yaml
```

### 3. Set up RBAC

```bash
oc apply -f config/rbac/service_account.yaml
oc apply -f config/rbac/role.yaml
oc apply -f config/rbac/role_binding.yaml
oc apply -f config/rbac/leader_election_role.yaml
oc apply -f config/rbac/leader_election_role_binding.yaml
```

### 4. Deploy VictoriaMetrics

VictoriaMetrics provides long-term metrics storage with 90-day retention, replacing direct Prometheus/Thanos queries.

```bash
oc apply -f deploy/victoriametrics-statefulset.yaml
oc apply -f deploy/victoriametrics-service.yaml
oc apply -f deploy/victoriametrics-networkpolicy.yaml
```

Wait for the pod to be ready:

```bash
oc -n ovro-system wait --for=condition=ready pod -l app.kubernetes.io/name=victoriametrics --timeout=120s
```

### 5. Configure Prometheus remote-write

A cluster-admin must configure platform Prometheus to forward metrics to VictoriaMetrics. If the ConfigMap doesn't exist yet, create it; otherwise edit it to add the `remoteWrite` section:

```bash
oc apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster-monitoring-config
  namespace: openshift-monitoring
data:
  config.yaml: |
    prometheusK8s:
      remoteWrite:
        - url: "http://victoriametrics.ovro-system.svc:8428/api/v1/write"
          writeRelabelConfigs:
            - sourceLabels: [__name__]
              regex: "kubevirt_vmi_cpu_usage_seconds_total|kubevirt_vmi_memory_resident_bytes|container_cpu_usage_seconds_total|container_memory_working_set_bytes|kube_pod_container_resource_requests|kube_pod_container_resource_limits|kube_node_status_allocatable|kube_node_info|kube_node_status_condition"
              action: keep
EOF
```

> **Note:** If `cluster-monitoring-config` already exists with other settings, merge the `remoteWrite` section into the existing `prometheusK8s` key rather than replacing it.

### 6. Seed historical data

Remote-write only forwards new data. To import existing historical data from Prometheus into VictoriaMetrics, run the seed job:

```bash
oc apply -f deploy/victoriametrics-seed-job.yaml
```

Edit the `SEED_START` env var in the job manifest to match how far back your Prometheus has data (default: 90 days ago). Monitor progress:

```bash
oc -n ovro-system logs -f job/victoriametrics-seed
```

The job reads from Prometheus via the remote-read API in hourly chunks and typically completes within minutes.

### 7. Build and push container images

OVRO consists of three container images. Replace `<REGISTRY>` with your registry (e.g., `quay.io/yourorg` or `image-registry.openshift-image-registry.svc:5000/ovro-system`).

```bash
export REGISTRY=<REGISTRY>
export TAG=latest

# Backend (operator + API server)
podman build -f Dockerfile.backend -t ${REGISTRY}/ovro-backend:${TAG} .
podman push ${REGISTRY}/ovro-backend:${TAG}

# Console plugin
cd console-plugin
podman build -t ${REGISTRY}/ovro-console-plugin:${TAG} .
podman push ${REGISTRY}/ovro-console-plugin:${TAG}
cd ..

# Approval proxy (optional — only needed if using the approval workflow)
podman build -f Dockerfile.approval-proxy -t ${REGISTRY}/ovro-approval-proxy:${TAG} .
podman push ${REGISTRY}/ovro-approval-proxy:${TAG}
```

### 8. Deploy the backend

Edit `config/manager/manager.yaml` and set the container image to your registry path, then apply:

```bash
oc apply -f config/manager/manager.yaml
oc apply -f deploy/backend-service.yaml
```

The backend starts listening on port 8443 with TLS certificates auto-generated by the OpenShift service-ca operator (via the `service.beta.openshift.io/serving-cert-secret-name` annotation on the Service).

### 9. Deploy the console plugin

Edit `deploy/console-plugin-deployment.yaml` and update the image reference to your registry, then apply:

```bash
oc apply -f deploy/console-plugin-deployment.yaml
oc apply -f deploy/consoleplugin.yaml
```

Enable the plugin in the OpenShift Console:

```bash
oc patch console.operator.openshift.io cluster \
  --type=merge \
  --patch='{"spec":{"plugins":["ovro-console-plugin"]}}'
```

Refresh the OpenShift Console. The **Rightsizing** tab appears in the left navigation under **Virtualization**.

### 10. Create a default RightsizingPolicy

```bash
oc apply -f config/samples/rightsizingpolicy.yaml
```

This creates a cluster-scoped `RightsizingPolicy` named `default` with sensible defaults (30-day lookback, P95 percentile, 20% headroom). You can edit these values from the Policy tab in the console or via `oc edit rightsizingpolicy default`.

### 11. Verify the installation

```bash
# Check all pods are running
oc get pods -n ovro-system

# Check the CRDs are installed
oc get crd | grep rightsizing

# Check recommendations are being generated (may take up to reconcileIntervalMinutes)
oc get rightsizingrecommendations -A
```

## Demo Mode

Demo mode generates synthetic rightsizing recommendations for all VMs without requiring real Prometheus metrics data. This is useful for demos, testing the UI, and validating the approval workflow.

### Quick setup

Demo mode requires the signing key secret, the approval proxy, and the approval route to be deployed so that the full approval flow works end-to-end. Follow steps 1–4 of the [Approval Workflow Setup](#approval-workflow-setup-optional) section first, then enable demo mode:

```bash
# 1. Set the approval route hostname
APPROVAL_HOST=$(oc get route ovro-approval -n ovro-system -o jsonpath='{.spec.host}')

# 2. Enable demo mode and configure the backend
oc set env deployment/controller-manager -n ovro-system \
  OVRO_DEMO_MODE=true \
  OVRO_APPROVAL_ROUTE_HOST=${APPROVAL_HOST} \
  SIGNING_KEY_PATH=/etc/ovro/signing-key/key

# 3. Mount the signing key into the backend (if not already done)
oc patch deployment controller-manager -n ovro-system --type=json -p='[
  {"op": "add", "path": "/spec/template/spec/volumes/-", "value": {"name": "signing-key", "secret": {"secretName": "ovro-approval-signing-key"}}},
  {"op": "add", "path": "/spec/template/spec/containers/0/volumeMounts/-", "value": {"name": "signing-key", "mountPath": "/etc/ovro/signing-key", "readOnly": true}}
]'
```

### Behaviour

In demo mode:
- Every VM gets a synthetic recommendation (half are downsize, half are upsize, based on VM name)
- Clicking "Rightsize" always triggers the approval workflow and displays the approval URL with a JWT token directly in the dialog
- No owner labels or notification configuration is required — a synthetic owner (`demo-user@example.com`) is used automatically
- You can copy the approval URL from the dialog and open it in a browser to walk through the full approval/rejection flow
- The backend and approval proxy must share the same signing key secret so that tokens generated by the backend can be validated by the proxy

To disable demo mode:

```bash
oc set env deployment/controller-manager -n ovro-system OVRO_DEMO_MODE-
```

## Approval Workflow Setup (Optional)

The approval workflow is optional. Without it, clicking "Rightsize" applies changes immediately. To enable it:

### 1. Create the signing key secret

The signing key is used to generate and validate JWT approval tokens. Both the backend and the approval proxy must use the same key.

```bash
# Generate a random signing key and create the secret in one step
oc create secret generic ovro-approval-signing-key \
  --from-file=key=<(openssl rand -base64 32) \
  -n ovro-system
```

> **Important:** If the secret already exists with a placeholder value (e.g. from deploying the sample manifests), replace it with a real key:
> ```bash
> oc delete secret ovro-approval-signing-key -n ovro-system
> oc create secret generic ovro-approval-signing-key \
>   --from-file=key=<(openssl rand -base64 32) \
>   -n ovro-system
> ```

### 2. Label VMs with owners

The approval workflow activates when a VM (or its namespace) has the owner label. Without it, changes are applied immediately as before.

```bash
# Label a specific VM
oc label vm <vm-name> -n <namespace> rightsizing.redhatconsulting.io/owner=user@example.com

# Or label an entire namespace (applies to all VMs in it)
oc label namespace <namespace> rightsizing.redhatconsulting.io/owner=team-lead@example.com
```

VM-level labels take precedence over namespace-level labels.

### 3. Deploy the approval proxy

Edit `deploy/approval-proxy-deployment.yaml` and update the image reference, then apply:

```bash
oc apply -f deploy/approval-proxy-serviceaccount.yaml
oc apply -f deploy/approval-proxy-deployment.yaml
oc apply -f deploy/approval-proxy-service.yaml
```

### 4. Create the approval route

Edit `deploy/approval-proxy-route.yaml` and set the `spec.host` to your desired hostname (e.g., `ovro-approval.apps.<cluster-domain>`), then apply:

```bash
oc apply -f deploy/approval-proxy-route.yaml
```

### 5. Configure the backend with the signing key and approval route

The backend needs access to the same signing key used by the approval proxy, plus the route hostname so it can generate valid approval URLs.

```bash
# Get the route hostname
APPROVAL_HOST=$(oc get route ovro-approval -n ovro-system -o jsonpath='{.spec.host}')

# Set environment variables on the backend
oc set env deployment/controller-manager -n ovro-system \
  OVRO_APPROVAL_ROUTE_HOST=${APPROVAL_HOST} \
  SIGNING_KEY_PATH=/etc/ovro/signing-key/key

# Mount the signing key secret into the backend pod
oc patch deployment controller-manager -n ovro-system --type=json -p='[
  {"op": "add", "path": "/spec/template/spec/volumes/-", "value": {"name": "signing-key", "secret": {"secretName": "ovro-approval-signing-key"}}},
  {"op": "add", "path": "/spec/template/spec/containers/0/volumeMounts/-", "value": {"name": "signing-key", "mountPath": "/etc/ovro/signing-key", "readOnly": true}}
]'
```

> **Note:** If you change the signing key secret, restart both the backend and the approval proxy so they pick up the new key:
> ```bash
> oc rollout restart deployment/controller-manager deployment/ovro-approval-proxy -n ovro-system
> ```

## Notification Configuration (Optional)

Notifications are sent to VM owners when a rightsizing change requires approval. Configure notification channels via a ConfigMap.

### Supported forwarders

| Type | Description | Credentials Secret Fields |
|------|-------------|---------------------------|
| `slack` | DMs the owner or posts to a channel via Slack Bot API | `botToken` |
| `teams` | DMs the owner or posts to a channel via Microsoft Graph API | `tenantId`, `clientId`, `clientSecret` |
| `smtp` | Sends email via SMTP (STARTTLS, implicit TLS, or plain) | `username`, `password` |
| `snmp` | Sends SNMP trap | (no credentials — uses community string from config) |
| `pagerduty` | Creates PagerDuty incident | `routingKey` |
| `rocketchat` | Posts to a Rocket.Chat channel | `authToken`, `userId` |
| `servicenow` | Creates ServiceNow incident with approval link | `username`, `password` |

> **Note:** Not all notification forwarders have been thoroughly tested. The Slack and SMTP forwarders have received the most testing. If you encounter issues with other forwarders, please report them.

### Owner-based routing (Slack and Teams)

Slack and Teams resolve the notification target dynamically from the `rightsizing.redhatconsulting.io/owner` label:

| Owner label value | Behaviour |
|-------------------|-----------|
| `user@company.com` | Looks up `user` first (without domain), then full email. Sends a DM to the matched user. |
| `#channel-name` | Posts to the named channel (Slack) or uses the webhook fallback (Teams). |
| `CXXXXXXXX` | Posts to the Slack channel by ID. |

If user lookup fails, both forwarders fall back to the `channel` field in the ConfigMap. SMTP is the only forwarder that sends directly to the `user@company.com` address as-is.

### Create the notification ConfigMap

Create a ConfigMap `ovro-notifications` in the `ovro-system` namespace. A template is provided at `deploy/ovro-notifications-configmap.yaml`:

```bash
oc apply -f deploy/ovro-notifications-configmap.yaml
```

Edit the ConfigMap to enable and configure the forwarders you need:

```bash
oc edit configmap ovro-notifications -n ovro-system
```

Example configuration enabling Slack and SMTP:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ovro-notifications
  namespace: ovro-system
data:
  config.yaml: |
    forwarders:
      # Slack: DMs the owner by email, falls back to channel if lookup fails
      - type: slack
        enabled: true
        channel: "#vm-rightsizing"    # fallback only
        secretRef: ovro-slack-credentials
      - type: smtp
        enabled: true
        from: "ovro@company.com"
        to: "{{owner}}"
        smtpServer: "smtp.company.com"
        smtpPort: 587
        smtpTLS: "starttls"              # "starttls", "tls", or "none"
        smtpTLSSkipVerify: false
        secretRef: ovro-smtp-credentials
```

### Create credential secrets

Each enabled forwarder that requires credentials references a Kubernetes Secret by name via the `secretRef` field. Create these secrets in the `ovro-system` namespace:

```bash
# Slack (requires a Slack Bot token with chat:write and users:read.email scopes)
oc create secret generic ovro-slack-credentials \
  --from-literal=botToken='xoxb-your-bot-token' \
  -n ovro-system

# Teams (requires an Azure AD app registration with Chat.Create, ChatMessage.Send, User.Read.All)
oc create secret generic ovro-teams-credentials \
  --from-literal=tenantId='your-tenant-id' \
  --from-literal=clientId='your-client-id' \
  --from-literal=clientSecret='your-client-secret' \
  -n ovro-system

# SMTP
oc create secret generic ovro-smtp-credentials \
  --from-literal=username='ovro@company.com' \
  --from-literal=password='app-password-here' \
  -n ovro-system

# ServiceNow
oc create secret generic ovro-servicenow-credentials \
  --from-literal=username='api-user' \
  --from-literal=password='api-password' \
  -n ovro-system
```

### Mount the notification config into the backend

The backend reads the notification config from `/etc/ovro/notifications/config.yaml` by default. Mount the ConfigMap:

```bash
oc patch deployment controller-manager -n ovro-system --type=json -p='[
  {"op": "add", "path": "/spec/template/spec/volumes/-", "value": {"name": "notification-config", "configMap": {"name": "ovro-notifications"}}},
  {"op": "add", "path": "/spec/template/spec/containers/0/volumeMounts/-", "value": {"name": "notification-config", "mountPath": "/etc/ovro/notifications", "readOnly": true}}
]'
```

## CI/CD with Tekton

A Tekton pipeline is included at `tekton/pipeline.yaml`. It builds all three container images and deploys them to the cluster.

### Pipeline parameters

| Parameter | Description |
|-----------|-------------|
| `git-url` | Git repository URL |
| `git-revision` | Branch or tag to build (default: `main`) |
| `image-registry` | Container registry prefix for built images |

### Pipeline tasks

1. **git-clone** — clones the repository
2. **build-backend** — builds `Dockerfile.backend` and pushes `ovro-backend` image
3. **build-plugin** — builds `console-plugin/Dockerfile` and pushes `ovro-console-plugin` image (runs in parallel with build-approval-proxy)
4. **build-approval-proxy** — builds `Dockerfile.approval-proxy` and pushes `ovro-approval-proxy` image (runs in parallel with build-plugin)
5. **deploy** — deploys all components to the cluster

### Running the pipeline

```bash
# Apply the pipeline
oc apply -f tekton/pipeline.yaml

# Start a pipeline run
tkn pipeline start ovro-build \
  -p git-url=https://github.com/kborup-redhat/ovro.git \
  -p git-revision=main \
  -p image-registry=image-registry.openshift-image-registry.svc:5000/ovro-system \
  -w name=shared-workspace,volumeClaimTemplateFile=tekton/workspace-pvc.yaml
```

## Configuration Reference

### RightsizingPolicy

The `RightsizingPolicy` CR controls operator behaviour. Create one named `default` (cluster-scoped):

| Field | Default | Description |
|-------|---------|-------------|
| `lookbackDays` | 30 | Days of metric history to analyse |
| `algorithm.percentile` | 95 | Utilisation percentile for sizing |
| `algorithm.headroomPercent` | 20 | Extra capacity above the percentile |
| `thresholds.minCpuSavings` | 1 | Minimum CPU core savings to recommend |
| `thresholds.minMemorySavings` | 1Gi | Minimum memory savings to recommend |
| `thresholds.upsizeUtilizationPercent` | 90 | P95 above this triggers upsize |
| `reconcileIntervalMinutes` | 60 | How often to re-evaluate each VM |
| `revertRetentionDays` | 30 | Days a revert option stays available |
| `metricsStorage.retentionDays` | 90 | VictoriaMetrics data retention period |
| `metricsStorage.storageSize` | 50Gi | VictoriaMetrics PVC size |

### Environment Variables

Set on the `controller-manager` deployment:

| Variable | Description |
|----------|-------------|
| `OVRO_DEMO_MODE` | Set to `true` to enable demo mode |
| `OVRO_APPROVAL_ROUTE_HOST` | Hostname of the approval proxy route (e.g., `ovro-approval.apps.cluster.example.com`) |
| `SIGNING_KEY_PATH` | Path to the JWT signing key file (e.g., `/etc/ovro/signing-key/key`) |
| `TLS_CERT_FILE` | Path to TLS certificate (auto-set by serving-cert) |
| `TLS_KEY_FILE` | Path to TLS key (auto-set by serving-cert) |

### Annotations and Labels

| Key | Target | Description |
|-----|--------|-------------|
| `rightsizing.redhatconsulting.io/exclude: "true"` | VM annotation | Excludes the VM from rightsizing analysis |
| `rightsizing.redhatconsulting.io/owner` | VM or Namespace label | Sets the owner for the approval workflow (email or username) |

## Recommendation States

| State | Description |
|-------|-------------|
| `pending` | Recommendation created, waiting for admin action |
| `awaiting-approval` | Admin clicked Rightsize, owner notified, waiting for owner to approve/reject |
| `approved` | Owner approved (transitional — moves to applied) |
| `applied-pending-restart` | Changes applied to VM spec, waiting for restart |
| `applied` | Changes applied and active |
| `reverted` | Changes were reverted to the original configuration |
| `failed` | Something went wrong during apply |

## Building from Source

```bash
# Backend binary
go build -o bin/manager cmd/main.go

# Approval proxy binary
go build -o bin/approval-proxy cmd/approval-proxy/main.go

# Console plugin
cd console-plugin && npm ci && npm run build

# Run Go tests
go test ./...

# TypeScript type check
cd console-plugin && npx tsc --noEmit
```

## Uninstalling

```bash
# Remove the console plugin
oc patch console.operator.openshift.io cluster \
  --type=merge \
  --patch='{"spec":{"plugins":[]}}'

# Remove the Prometheus remote-write config
# Edit cluster-monitoring-config and remove the remoteWrite section:
oc -n openshift-monitoring edit configmap cluster-monitoring-config

# Delete all OVRO resources (including VictoriaMetrics)
oc delete -f deploy/
oc delete -f config/rbac/
oc delete -f config/crd/bases/

# Delete VictoriaMetrics PVC
oc -n ovro-system delete pvc data-victoriametrics-0

# Delete the namespace
oc delete namespace ovro-system
```

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
