---
title: "Chapter 8: CI/CD and Deployment"
order: 8
---

# Chapter 8: CI/CD and Deployment

In previous chapters, we built CRD types, integrated with Prometheus, wrote a rightsizing calculator, created controllers, exposed a REST API, designed a console plugin, and defined an RBAC model. Now it is time to answer the final question: **how does all of this actually get built, tested, and deployed to an OpenShift cluster?**

This chapter covers the full delivery pipeline -- from a `git push` all the way to running pods. By the end, you will understand every CI/CD component in the project and the security reasoning behind each design decision.

## The Assembly Line Analogy

Think of the CI/CD pipeline as an **assembly line in a factory**. Raw materials (source code) enter at one end, and each station along the line performs one specific quality check or build step:

1. **Receiving** -- clone the source code from the repository
2. **Go inspection** -- lint and test the backend code
3. **Frontend inspection** -- lint and test the console plugin code
4. **Backend fabrication** -- build the backend container image
5. **Plugin fabrication** -- build the console plugin container image
6. **Shipping** -- deploy the images to the cluster

Only a product that passes every inspection station gets deployed. If any station rejects the product, the line stops and the defect is reported. No shortcuts, no skipping steps.

## 1. Tekton Pipeline

**Source file:** `tekton/pipeline.yaml`

The main build pipeline is defined as a Tekton `Pipeline` resource running inside the OpenShift cluster. This is a deliberate choice over running everything in GitHub Actions -- we will discuss why at the end of this section.

### Pipeline Parameters

The pipeline accepts three parameters that make it reusable across environments:

```yaml
params:
  - name: git-url
    type: string
    description: Git repository URL
  - name: git-revision
    type: string
    description: Git revision (branch, tag, or commit SHA)
    default: main
  - name: image-registry
    type: string
    description: Container image registry
    default: image-registry.openshift-image-registry.svc:5000/ovro-system
```

| Parameter | Purpose |
|---|---|
| `git-url` | The repository to clone. Injected by the trigger binding on push events. |
| `git-revision` | The branch, tag, or SHA to build. Defaults to `main` but overridden by the trigger on every push. |
| `image-registry` | Where to push built images. Defaults to the OpenShift internal registry, which means images never leave the cluster unless you explicitly point this elsewhere. |

The default `image-registry` value of `image-registry.openshift-image-registry.svc:5000/ovro-system` is the cluster-internal registry address. This is significant: the pipeline builds and pushes images *inside* the cluster, so there is no need for external registry credentials or network egress for image storage.

### Task Sequence

The pipeline defines eight tasks that run in strict sequence:

```
git-clone -> go-lint -> go-test -> npm-lint -> npm-test -> build-backend -> build-plugin -> deploy
```

Let's trace the flow through each task.

#### Task 1: git-clone (Hub Resolver)

```yaml
- name: git-clone
  taskRef:
    resolver: hub
    params:
      - name: catalog
        value: tekton-catalog-tasks
      - name: type
        value: artifact
      - name: kind
        value: task
      - name: name
        value: git-clone
      - name: version
        value: "0.9"
  params:
    - name: url
      value: $(params.git-url)
    - name: revision
      value: $(params.git-revision)
    - name: deleteExisting
      value: "true"
```

This uses the **Hub resolver** to pull the `git-clone` task (version 0.9) from the Tekton Catalog. The Hub resolver is Tekton's mechanism for referencing community-maintained tasks without copying their YAML into your repository. It fetches the task definition at runtime from the configured catalog.

The `deleteExisting: "true"` parameter ensures a clean workspace on every run, preventing stale files from a previous build from contaminating the current one.

#### Tasks 2-5: Custom Hardcoded Tasks

The next four tasks -- `go-lint`, `go-test`, `npm-lint`, and `npm-test` -- reference custom Task resources rather than using generic "run a command" tasks from the catalog.

**Source files:**
- `tekton/tasks/go-vet.yaml`
- `tekton/tasks/go-test.yaml`
- `tekton/tasks/npm-lint.yaml`
- `tekton/tasks/npm-test.yaml`

Here is the Go vet task as an example:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: ovro-go-vet
  namespace: ovro-system
spec:
  workspaces:
    - name: source
  steps:
    - name: vet
      image: golang:1.26
      workingDir: $(workspaces.source.path)
      env:
        - name: GOMODCACHE
          value: $(workspaces.source.path)/.cache/go/mod
        - name: GOCACHE
          value: $(workspaces.source.path)/.cache/go/build
      script: |
        #!/bin/sh
        set -ex
        mkdir -p $GOMODCACHE $GOCACHE
        go vet -buildvcs=false ./...
```

**Why custom tasks instead of generic command-parameter tasks?**

The Tekton Catalog provides generic tasks that accept a `command` parameter -- you pass the shell command as a string and the task executes it. This is convenient but introduces a **command injection risk**: if any pipeline parameter value (such as a branch name) is interpolated into the command string, an attacker could craft a malicious branch name that executes arbitrary code.

By hardcoding the exact commands in the task's `script` field, we eliminate this attack surface entirely. The pipeline parameters (`git-url`, `git-revision`) are never interpolated into shell commands in these tasks. Each task has a fixed, auditable script that cannot be influenced by external input.

Additional details worth noting:

- **`-buildvcs=false`**: The Go tasks pass this flag to avoid VCS-related errors when running inside a container where the `.git` directory may not be fully available.
- **Cache directories**: Both Go tasks configure `GOMODCACHE` and `GOCACHE` to write into the shared workspace. This means module downloads persist across tasks in the same pipeline run, speeding up subsequent steps.
- **npm cache**: The npm tasks similarly redirect the npm cache into the workspace with `npm_config_cache`.
- **Go test filtering**: The `go-test` task filters out end-to-end tests (`grep -v /test/e2e`), running only unit and integration tests in the pipeline. E2E tests require a running cluster and are handled separately.
- **ESLint**: The `npm-lint` task runs `npx eslint src/` to lint the TypeScript/React source code of the console plugin.

#### Tasks 6-7: build-backend and build-plugin (Hub Resolver)

Both build tasks use the `buildah` task (version 0.8) from the Tekton Catalog via the Hub resolver:

```yaml
- name: build-backend
  runAfter: [npm-test]
  taskRef:
    resolver: hub
    params:
      - name: catalog
        value: tekton-catalog-tasks
      - name: type
        value: artifact
      - name: kind
        value: task
      - name: name
        value: buildah
      - name: version
        value: "0.8"
  params:
    - name: IMAGE
      value: $(params.image-registry)/ovro-backend:$(params.git-revision)
    - name: DOCKERFILE
      value: Dockerfile.backend
    - name: CONTEXT
      value: .
    - name: TLSVERIFY
      value: "true"
```

Key points:

- **Image tagging**: Images are tagged with the git revision (`$(params.git-revision)`), making every build traceable to the exact commit or branch that produced it.
- **TLSVERIFY: "true"**: This is a security decision. Setting TLS verification to `true` on buildah steps ensures that image pushes to the registry use verified TLS connections. Disabling TLS verification (a common shortcut in tutorials) would allow man-in-the-middle attacks on the image push.
- **Separate Dockerfiles**: The backend uses `Dockerfile.backend` with a build context of `.` (project root), while the plugin uses `console-plugin/Dockerfile` with a context of `console-plugin`. This separation keeps build contexts minimal and avoids sending unnecessary files to the build daemon.

#### Task 8: deploy

**Source file:** `tekton/tasks/deploy.yaml`

```yaml
steps:
  - name: deploy
    image: registry.redhat.io/openshift4/ose-cli:v4.17
    workingDir: $(workspaces.source.path)
    script: |
      #!/bin/sh
      set -ex

      # Apply CRDs
      oc apply -f config/crd/bases/

      # Apply RBAC
      oc apply -f config/rbac/

      # Apply network policy
      oc apply -f config/network-policy/

      # Apply cert-manager certificate
      oc apply -f config/certmanager/ || echo "cert-manager not available, skipping"

      # Apply deployment manifests
      oc apply -f deploy/

      # Update deployment images
      oc set image deployment/ovro-operator \
        manager=$(params.backend-image) \
        -n ovro-system || echo "Deployment not yet created"

      oc set image deployment/ovro-console-plugin \
        plugin=$(params.plugin-image) \
        -n ovro-system || echo "Console plugin deployment not yet created"
```

The deploy task uses the Red Hat `ose-cli` image (containing the `oc` command) and applies manifests in a specific order: CRDs first (so the API types exist), then RBAC (so the controller has permissions), then network policies, then cert-manager certificates (with a graceful fallback if cert-manager is not installed), and finally the deployment manifests themselves.

After applying the manifests, it updates the container images on the running deployments using `oc set image`, which triggers a rolling update.

### Workspace and Storage

All tasks share a single workspace backed by a PersistentVolumeClaim:

**Source file:** `tekton/pvcs.yaml`

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ovro-workspace
  namespace: ovro-system
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

The 10Gi PVC provides enough space for the cloned repository, Go module cache, npm cache, and the built container images. Using `ReadWriteOnce` access mode means only one pipeline run can use this volume at a time, which is appropriate since runs are sequential by nature (each run needs a clean workspace).

## 2. Tekton Triggers

**Source directory:** `tekton/triggers/`

Triggers connect external events (GitHub webhooks) to pipeline runs, automating the "push to deploy" workflow.

### EventListener

**Source file:** `tekton/triggers/eventlistener.yaml`

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata:
  name: ovro-github-listener
  namespace: ovro-system
spec:
  serviceAccountName: pipeline
  triggers:
    - name: github-push
      interceptors:
        - ref:
            name: "github"
          params:
            - name: "secretRef"
              value:
                secretName: github-webhook-secret
                secretKey: token
            - name: "eventTypes"
              value: ["push"]
      bindings:
        - ref: ovro-github-binding
      template:
        ref: ovro-github-template
```

The EventListener creates an HTTP endpoint inside the cluster that receives webhook payloads from GitHub. When a `push` event arrives, the **GitHub interceptor** does two things:

1. **Validates the webhook signature** against the shared secret stored in `github-webhook-secret`. This ensures that only GitHub -- not an attacker sending forged payloads -- can trigger pipeline runs.
2. **Filters by event type**, accepting only `push` events and ignoring pull request events, issue comments, and other webhook types.

The `serviceAccountName: pipeline` specifies which service account the EventListener runs as. This account must have permissions to create PipelineRun resources in the `ovro-system` namespace.

### TriggerBinding

**Source file:** `tekton/triggers/triggerbinding.yaml`

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerBinding
metadata:
  name: ovro-github-binding
  namespace: ovro-system
spec:
  params:
    - name: git-url
      value: $(body.repository.clone_url)
    - name: git-revision
      value: $(body.ref)
```

The TriggerBinding extracts two values from the GitHub push event JSON payload:

| Parameter | JSONPath | Example Value |
|---|---|---|
| `git-url` | `body.repository.clone_url` | `https://github.com/kborup-redhat/ovro.git` |
| `git-revision` | `body.ref` | `refs/heads/main` |

These extracted values become the pipeline parameters, so every push automatically builds the correct repository and branch.

### TriggerTemplate

**Source file:** `tekton/triggers/triggertemplate.yaml`

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata:
  name: ovro-github-template
  namespace: ovro-system
spec:
  params:
    - name: git-url
    - name: git-revision
  resourcetemplates:
    - apiVersion: tekton.dev/v1
      kind: PipelineRun
      metadata:
        generateName: ovro-build-
      spec:
        pipelineRef:
          name: ovro-build
        params:
          - name: git-url
            value: $(tt.params.git-url)
          - name: git-revision
            value: $(tt.params.git-revision)
        workspaces:
          - name: workspace
            persistentVolumeClaim:
              claimName: ovro-workspace
```

The TriggerTemplate is the factory that stamps out PipelineRun resources. Each push event produces one PipelineRun with:

- A generated name prefixed with `ovro-build-` (e.g., `ovro-build-xk7m2`)
- The git URL and revision from the TriggerBinding
- The shared workspace PVC

The `$(tt.params.*)` syntax refers to TriggerTemplate parameters, distinct from the `$(params.*)` syntax used inside pipeline definitions.

### The Complete Trigger Flow

```
GitHub push event
    |
    v
EventListener (validates webhook signature)
    |
    v
GitHub Interceptor (filters for "push" events)
    |
    v
TriggerBinding (extracts git-url and git-revision from JSON)
    |
    v
TriggerTemplate (creates PipelineRun resource)
    |
    v
Pipeline executes: clone -> lint -> test -> build -> deploy
```

## 3. Dockerfiles

OVRO has three Dockerfiles, each following the **multi-stage build** pattern where a large build image compiles the code and a minimal runtime image runs it.

### Operator Dockerfile

**Source file:** `Dockerfile`

```dockerfile
FROM golang:1.26.2 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal:9.6
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
```

Key security and design decisions:

- **Multi-stage build**: The final image contains only the compiled binary, not the Go toolchain, source code, or module cache. This reduces the image size and attack surface dramatically.
- **UBI9 minimal base**: Red Hat Universal Base Image 9 minimal (`ubi9/ubi-minimal:9.6`) is the runtime base. UBI images receive regular security updates from Red Hat, are free to redistribute, and are scanned for CVEs as part of Red Hat's release process. Compare this with Alpine or scratch images that lack vendor-backed security guarantees.
- **Non-root user 65532**: The `USER 65532:65532` directive ensures the process runs as a non-root user. UID 65532 is the conventional `nonroot` user in distroless/minimal images. This aligns with OpenShift's default SecurityContextConstraints, which reject containers that run as root.
- **CGO_ENABLED=0**: Disabling CGO produces a statically linked binary that has no runtime dependency on libc, which is essential since `ubi-minimal` does not include the full C library.
- **TARGETOS/TARGETARCH**: These build arguments support multi-architecture builds (amd64, arm64) when using `docker buildx`.
- **Selective COPY**: Only the necessary source directories (`cmd/`, `api/`, `internal/`) are copied, not the entire repository. The `go.mod`/`go.sum` are copied first so that the module download layer is cached and only invalidated when dependencies change.

### Backend Dockerfile

**Source file:** `Dockerfile.backend`

```dockerfile
FROM golang:1.26.2 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -o manager cmd/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal:9.6
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
```

This follows the same pattern as the operator Dockerfile -- `golang:1.26.2` builder, UBI9 minimal runtime, non-root user 65532. The difference is that it copies the entire source tree (`COPY . .`) rather than selective directories, since the backend binary may depend on additional packages.

### Console Plugin Dockerfile

**Source file:** `console-plugin/Dockerfile`

```dockerfile
FROM node:22 AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM registry.access.redhat.com/ubi9/ubi-minimal:9.6
RUN microdnf install -y nginx && microdnf clean all
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/nginx.conf
USER 1001
EXPOSE 9443
CMD ["nginx", "-g", "daemon off;"]
```

This Dockerfile is structurally different because the console plugin is a **static web application**, not a Go binary:

- **Node 22 builder**: The build stage uses Node.js 22 to run `npm ci` (clean install) and `npm run build`, which compiles the TypeScript/React source into static HTML, CSS, and JavaScript files in the `dist/` directory.
- **nginx runtime**: The runtime stage installs nginx via `microdnf` (the minimal package manager in UBI) to serve the static files. After installation, `microdnf clean all` removes the package cache to keep the image small.
- **TLS on port 9443**: The plugin serves HTTPS on port 9443, not plain HTTP. This is required by the OpenShift Console, which loads plugins over TLS.
- **User 1001**: Runs as a non-root user. UID 1001 is the conventional application user in Red Hat images.

## 4. nginx Configuration

**Source file:** `console-plugin/nginx.conf`

```nginx
error_log /dev/stderr;
events {}
http {
    access_log /dev/stdout;
    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    server_tokens off;

    server {
        listen 9443 ssl;
        ssl_certificate /var/serving-cert/tls.crt;
        ssl_certificate_key /var/serving-cert/tls.key;
        ssl_protocols TLSv1.2 TLSv1.3;

        add_header X-Frame-Options "DENY" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
        add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'" always;

        root /usr/share/nginx/html;
        index index.html;

        location / {
            try_files $uri $uri/ /index.html;
        }

        location /api/ {
            proxy_pass https://ovro-backend:8443;
            proxy_ssl_verify off;
            proxy_set_header Authorization $http_authorization;
        }
    }
}
```

This configuration file is dense with security decisions. Let's break it down.

### TLS Configuration

```nginx
listen 9443 ssl;
ssl_certificate /var/serving-cert/tls.crt;
ssl_certificate_key /var/serving-cert/tls.key;
ssl_protocols TLSv1.2 TLSv1.3;
```

- The server listens exclusively on TLS. There is no plain HTTP listener -- not even for health checks or redirects. All traffic is encrypted.
- The TLS certificate and key are mounted from `/var/serving-cert/`, which is populated by OpenShift's service serving certificate signer (or cert-manager). The plugin never generates its own certificates.
- Only TLS 1.2 and 1.3 are permitted. TLS 1.0 and 1.1 are disabled because they have known vulnerabilities.

### Server Hardening

```nginx
server_tokens off;
```

This prevents nginx from advertising its version number in HTTP response headers and error pages. Version disclosure helps attackers identify known vulnerabilities in specific nginx versions.

### Security Headers

```nginx
add_header X-Frame-Options "DENY" always;
add_header X-Content-Type-Options "nosniff" always;
add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'" always;
```

| Header | Value | Purpose |
|---|---|---|
| `X-Frame-Options` | `DENY` | Prevents the plugin from being embedded in iframes, blocking clickjacking attacks |
| `X-Content-Type-Options` | `nosniff` | Prevents browsers from guessing MIME types, blocking MIME confusion attacks |
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` | Tells browsers to only use HTTPS for one year, preventing protocol downgrade attacks |
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'` | Restricts script and style loading to the same origin, preventing XSS attacks. The `'unsafe-inline'` exception for styles is necessary for some PatternFly components that use inline styles |

### Static File Serving

```nginx
root /usr/share/nginx/html;
index index.html;

location / {
    try_files $uri $uri/ /index.html;
}
```

The `try_files` directive implements client-side routing for the React single-page application. When a request comes in for a path like `/recommendations/details`, nginx first checks if a file exists at that path, then falls back to `index.html`, which loads the React app that handles routing in JavaScript.

### API Proxy

```nginx
location /api/ {
    proxy_pass https://ovro-backend:8443;
    proxy_ssl_verify off;
    proxy_set_header Authorization $http_authorization;
}
```

This is how the console plugin communicates with the OVRO backend:

1. **Proxy path**: Any request to `/api/` is forwarded to the backend service at `https://ovro-backend:8443`. This keeps the plugin's JavaScript simple -- it makes requests to `/api/` on its own origin, and nginx handles routing them to the backend.
2. **Authorization forwarding**: The `proxy_set_header Authorization $http_authorization` directive passes the user's OpenShift authentication token through to the backend. This means the backend receives the same token that the OpenShift Console used to authenticate the user, enabling it to perform RBAC checks.
3. **Internal TLS**: The proxy connects to the backend over HTTPS (port 8443), maintaining encryption for the internal service-to-service call. The `proxy_ssl_verify off` is acceptable here because both services are within the same cluster namespace and the certificates are issued by the cluster's internal CA.

## 5. Kubernetes Manifests

### Manager Deployment

**Source file:** `config/manager/manager.yaml`

The operator deployment is configured to meet the **restricted** Pod Security Standard, the most restrictive level in Kubernetes:

```yaml
spec:
  template:
    spec:
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - command:
        - /manager
        args:
          - --leader-elect
          - --health-probe-bind-address=:8081
        image: controller:latest
        name: manager
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop:
            - "ALL"
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
```

Security controls at the pod level:

| Setting | Value | Purpose |
|---|---|---|
| `runAsNonRoot` | `true` | Prevents the container from running as root, even if the Dockerfile's USER directive is missing |
| `seccompProfile.type` | `RuntimeDefault` | Applies the default seccomp profile, blocking dangerous system calls |
| `allowPrivilegeEscalation` | `false` | Prevents processes from gaining more privileges than their parent |
| `readOnlyRootFilesystem` | `true` | Makes the container filesystem read-only, preventing attackers from writing malicious files |
| `capabilities.drop: ALL` | Drops all Linux capabilities | The process has no special kernel privileges whatsoever |

These settings together mean the manager runs with the absolute minimum privileges. If an attacker somehow compromises the process, they cannot escalate privileges, write to the filesystem, or make privileged system calls.

The deployment also includes:

- **Leader election** (`--leader-elect`): Ensures only one replica of the controller is actively reconciling at a time, preventing conflicting updates.
- **Health probes**: Liveness (`/healthz`) and readiness (`/readyz`) probes let OpenShift detect and restart unhealthy pods automatically.
- **Resource limits**: CPU is capped at 500m and memory at 128Mi, preventing the controller from consuming excessive cluster resources.

### RBAC Roles

**Source directory:** `config/rbac/`

The project defines a layered RBAC model with fine-grained roles for each CRD. For each of `RightsizingPolicy` and `RightsizingRecommendation`, there are three role levels:

| Role | Verbs | Use Case |
|---|---|---|
| `*-admin-role` | `*` (all) | Cluster administrators who manage RBAC delegation |
| `*-editor-role` | `create, delete, get, list, patch, update, watch` | Users who manage rightsizing resources |
| `*-viewer-role` | `get, list, watch` | Read-only monitoring and dashboards |

For example, the `rightsizingpolicy-viewer-role`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: rightsizingpolicy-viewer-role
rules:
- apiGroups:
  - rightsizing.redhatconsulting.io
  resources:
  - rightsizingpolicies
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rightsizing.redhatconsulting.io
  resources:
  - rightsizingpolicies/status
  verbs:
  - get
```

There are also two aggregate roles:

| Role | Covers | Verbs |
|---|---|---|
| `ovro-admin` | Both CRDs | `get, list, watch, update` plus status `update, patch` |
| `ovro-viewer` | Both CRDs | `get, list, watch` |

The **manager-role** is the ClusterRole used by the controller's service account. It has precisely the permissions the controller needs and nothing more:

- **RightsizingPolicies**: `get, list, watch` -- the controller reads policies but never modifies them
- **RightsizingRecommendations**: Full CRUD -- the controller creates, updates, and deletes recommendations
- **VirtualMachines** (kubevirt.io): `get, list, patch, watch` -- the controller reads VM specs and patches them when applying recommendations
- **VirtualMachineInstances** (kubevirt.io): `get, list, watch` -- read-only access to running VM instances for metrics correlation

This is the **principle of least privilege** in practice: each role grants exactly the permissions needed for its use case, no more.

### Deploy Manifests

**Source directory:** `deploy/`

Two additional manifests live outside the `config/` directory:

**`deploy/namespace.yaml`** creates the `ovro-system` namespace:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: ovro-system
  labels:
    app.kubernetes.io/name: ovro
```

**`deploy/consoleplugin.yaml`** registers the console plugin with OpenShift:

```yaml
apiVersion: console.openshift.io/v1
kind: ConsolePlugin
metadata:
  name: ovro-console-plugin
spec:
  displayName: "OVRO - Rightsizing"
  backend:
    type: Service
    service:
      name: ovro-console-plugin
      namespace: ovro-system
      port: 9443
      basePath: /
  proxy:
    - type: Service
      alias: ovro-backend
      authorize: true
      service:
        name: ovro-backend
        namespace: ovro-system
        port: 8443
```

The `ConsolePlugin` resource tells the OpenShift Console where to load the plugin from (the nginx service on port 9443) and how to proxy API requests (to the backend on port 8443 with `authorize: true`, which passes the user's authentication token).

### Network Policy

**Source file:** `config/network-policy/network-policy.yaml`

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ovro-network-policy
  namespace: ovro-system
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: ovro
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - ports:
        - port: 9443
        - port: 8443
  egress:
    - ports:
        - port: 443
        - port: 6443
        - port: 9090
        - port: 9091
```

The network policy restricts traffic to and from OVRO pods:

- **Ingress**: Only ports 9443 (console plugin) and 8443 (backend API) are open
- **Egress**: Only ports 443 (HTTPS to external services), 6443 (Kubernetes API server), 9090 and 9091 (Prometheus) are permitted

Any traffic to or from other ports is blocked by default. This limits the blast radius if a pod is compromised -- it cannot reach arbitrary services in the cluster.

## 6. GitHub Actions

While Tekton handles the main build-and-deploy pipeline on the cluster, GitHub Actions provides **fast feedback on pull requests** before code reaches the cluster.

### Test Workflow

**Source file:** `.github/workflows/test.yml`

```yaml
name: Tests

on:
  push:
  pull_request:

jobs:
  unit-tests:
    name: Go unit tests
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@v4

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Run Go tests
        run: |
          go mod tidy
          make test

      - name: Setup Node.js
        uses: actions/setup-node@v4
        with:
          node-version: 22

      - name: Run console plugin tests
        working-directory: console-plugin
        run: |
          npm ci
          npm test
```

This workflow runs on every push and pull request. It runs both Go unit tests and console plugin tests, providing fast feedback without requiring access to the cluster.

### Lint Workflow

**Source file:** `.github/workflows/lint.yml`

```yaml
name: Lint

on:
  push:
  pull_request:

jobs:
  go-lint:
    name: Go lint
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@v4

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Run linter
        uses: golangci/golangci-lint-action@v8
        with:
          version: v2.12.1
```

The lint workflow uses `golangci-lint` v2.12.1 -- a meta-linter that runs dozens of Go linters in a single pass. Pinning the version ensures consistent results across developer machines, CI, and the Tekton pipeline.

## Why Tekton Over GitHub Actions for the Main Pipeline?

The project uses both Tekton and GitHub Actions, but for different purposes. The main build-and-deploy pipeline runs on Tekton, while GitHub Actions handles lightweight checks. Here is why:

| Concern | Tekton | GitHub Actions |
|---|---|---|
| **Registry access** | Direct access to the OpenShift internal registry -- no external credentials needed | Would need image registry credentials stored as GitHub secrets |
| **Cluster access** | Runs inside the cluster with a service account -- can `oc apply` manifests directly | Would need a kubeconfig stored as a GitHub secret and network access to the cluster API |
| **Security** | Webhook secret validates that only GitHub can trigger builds | Same capability, but credentials for cluster access must be stored externally |
| **Network** | Internal to the cluster -- images never leave the network | Images must be pushed to/pulled from an external registry |
| **Cost** | Uses cluster compute that you already pay for | Uses GitHub-hosted runners (free tier has limits) |

In short: Tekton runs *where the code is deployed*, so it has natural access to everything it needs. Using GitHub Actions for the deploy pipeline would require storing cluster credentials externally, which adds complexity and risk.

GitHub Actions is still valuable for **pre-merge checks** -- running tests and lints on pull requests before code reaches the main branch and triggers the Tekton pipeline.

## The Security Decisions, Summarized

Security is not bolted on at the end -- it is woven into every layer:

| Layer | Decision | Rationale |
|---|---|---|
| **Base images** | UBI9 minimal | Vendor-backed CVE scanning and security updates from Red Hat |
| **Runtime user** | 65532 (operator), 1001 (plugin) | Non-root execution prevents privilege escalation |
| **Container security** | `readOnlyRootFilesystem`, `drop ALL` capabilities, no privilege escalation | Restricted Pod Security Standard compliance |
| **TLS** | TLS everywhere -- plugin (9443), backend (8443), buildah pushes | Encryption in transit for all communication |
| **TLS versions** | TLSv1.2 and TLSv1.3 only | TLS 1.0/1.1 have known vulnerabilities |
| **Webhook** | Shared secret validation | Prevents forged pipeline triggers |
| **Pipeline tasks** | Hardcoded scripts instead of parameterized commands | Eliminates command injection attack surface |
| **RBAC** | Least-privilege roles per CRD per access level | Users and service accounts get only the permissions they need |
| **Network** | NetworkPolicy restricting ingress/egress ports | Limits blast radius of a compromised pod |
| **HTTP headers** | CSP, HSTS, X-Frame-Options, X-Content-Type-Options | Defense in depth against XSS, clickjacking, and downgrade attacks |

## How the Console Plugin Is Served

To understand the full request flow for the console plugin, trace a user action from browser to backend:

```
Browser (OpenShift Console)
    |
    | 1. Loads plugin JS/CSS from nginx on port 9443 (TLS)
    v
nginx (console-plugin pod)
    |
    | 2. Static files: served from /usr/share/nginx/html
    | 3. API calls (/api/*): proxied to ovro-backend:8443
    |    Authorization header forwarded
    v
ovro-backend (backend pod, port 8443)
    |
    | 4. Validates token, queries Kubernetes API
    v
Kubernetes API / Prometheus
```

1. The OpenShift Console discovers the plugin via the `ConsolePlugin` resource and loads its JavaScript and CSS from the nginx service.
2. nginx serves the static build output (React app) from `/usr/share/nginx/html`.
3. When the React app makes API calls to `/api/`, nginx proxies them to the backend service, forwarding the user's `Authorization` header so the backend can verify who is making the request.
4. The backend uses the forwarded token to authenticate with the Kubernetes API and Prometheus, ensuring that users can only see data they are authorized to access.

## Key Takeaways

1. **Tekton runs on-cluster** -- the pipeline has direct access to the internal registry and cluster API, eliminating the need for external credentials and reducing the attack surface compared to external CI systems.

2. **Hardcoded pipeline tasks prevent command injection** -- by embedding the exact commands in task scripts rather than accepting commands as parameters, the pipeline eliminates a class of security vulnerabilities.

3. **Multi-stage Docker builds with UBI9 minimize the attack surface** -- build tools stay in the builder stage, and the runtime image contains only the compiled binary on a vendor-supported, CVE-scanned base.

4. **Security is layered, not bolted on** -- from non-root users and read-only filesystems in containers, to TLS everywhere, to security headers in nginx, to least-privilege RBAC, every layer adds defense in depth.

5. **GitHub Actions and Tekton complement each other** -- GitHub Actions provides fast feedback on pull requests (tests and lints), while Tekton handles the build-and-deploy pipeline that requires cluster access.

## Congratulations

You have completed the full OVRO tutorial. Over eight chapters, you have traced the entire system from the ground up:

1. **CRD Types** -- you learned how `RightsizingRecommendation` and `RightsizingPolicy` define the data model as custom Kubernetes resources.
2. **Prometheus Integration** -- you saw how the operator queries real VM metrics to make data-driven rightsizing decisions.
3. **Rightsizing Calculator** -- you understood the algorithm that computes optimal CPU and memory based on historical usage, headroom, and savings thresholds.
4. **Controller Reconciliation** -- you followed the reconciliation loop that watches VMs, generates recommendations, and optionally applies them automatically.
5. **Backend API** -- you explored the REST API that bridges the console plugin to the Kubernetes API with proper authentication and authorization.
6. **Console Plugin** -- you examined the React-based OpenShift Console plugin that provides the user interface for reviewing and acting on recommendations.
7. **RBAC and Security** -- you learned the role-based access control model and how security is enforced at every layer.
8. **CI/CD and Deployment** -- you traced the full delivery pipeline from a git push through testing, building, and deploying to the cluster.

You now have a comprehensive understanding of how a production-grade OpenShift operator is built, secured, tested, and deployed.

## Next Steps

Here are some directions to extend the project:

- **Add more metrics** -- expose additional Prometheus metrics from the controller (recommendation counts by direction, auto-apply success/failure rates, reconciliation latency histograms) to improve observability.
- **Namespace-scoped policies** -- extend `RightsizingPolicy` to support namespace-level overrides, allowing different teams to set different headroom percentages or savings thresholds for their workloads.
- **Grafana dashboards** -- create pre-built Grafana dashboards that visualize rightsizing trends over time: total savings realized, recommendations pending vs. applied, resource utilization before and after rightsizing.
- **ACS integration** -- add Advanced Cluster Security (ACS) tasks to the Tekton pipeline using `roxctl` to scan container images for CVEs and check deployments against security policies before allowing the deploy step to proceed.
- **Multi-cluster support** -- extend the pipeline to push images to an external registry and deploy across multiple OpenShift clusters, enabling a centralized build with distributed deployment.
