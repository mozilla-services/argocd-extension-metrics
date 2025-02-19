# ArgoCD Extension Metrics

The project introduces the ArgoCD extension to enable Metrics on Resource tab.
![](./docs/images/screenshot.png)

This extension is composed by 2 components:
- `argocd-metrics-server` is a backend service that queries and expose
  prometheus metrics to the UI extension
- UI extension render graphs based on metrics returned by the `argocd-metrics-server`

## Prerequisites

- Argo CD version 2.6+
- Prometheus

## Quick Start

### Install `argocd-metrics-server`

The `manifests` folder in this repo contains an example of how the
`argocd-metrics-server` can be installed.

```sh
git clone https://github.com/argoproj-labs/argocd-extension-metrics.git
cd argocd-extension-metrics
kustomize build ./manifests | kubectl apply -f -
```

All graphs are configured in the `argocd-metrics-server-configmap`.
The example configmap provided in the `manifests` defines how to
extract and query Prometheus to display the golden signal metrics in
Argo CD UI. This configmap must be changed depending on the metrics
available in your Prometheus instance.


### Install UI extension

The UI extension needs to be installed by mounting the React component
in Argo CD API server. This process can be automated by using the
[argocd-extension-installer][1]. This installation method will run an
init container that will download, extract and place the file in the
correct location.

The yaml file below is an example of how to define a kustomize patch
to install this UI extension:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-server
spec:
  template:
    spec:
      initContainers:
        - name: extension-metrics
          image: quay.io/argoprojlabs/argocd-extension-installer:v0.0.1
          env:
          - name: EXTENSION_URL
            value: https://github.com/argoproj-labs/argocd-extension-metrics/releases/download/v1.0.0/extension.tar.gz
          - name: EXTENSION_CHECKSUM_URL
            value: https://github.com/argoproj-labs/argocd-extension-metrics/releases/download/v1.0.0/extension_checksums.txt
          volumeMounts:
            - name: extensions
              mountPath: /tmp/extensions/
          securityContext:
            runAsUser: 1000
            allowPrivilegeEscalation: false
      containers:
        - name: argocd-server
          volumeMounts:
            - name: extensions
              mountPath: /tmp/extensions/
      volumes:
        - name: extensions
          emptyDir: {}
```

### Enabling the Metrics extension in Argo CD

Argo CD needs to have the proxy extension feature enabled for the
metrics extension to work. In order to do so add the following entry
in the `argocd-cmd-params-cm`:

```
server.enable.proxy.extension: "true"
```

The metrics extension needs to be authorized in Argo CD API server. To
enable it for all users add the following entry in `argocd-rbac-cm`:

```
policy.csv: |-
  p, role:readonly, extensions, invoke, metrics, allow
```

**Note**: make sure to assign a proper role to the extension policy if you
want to restrict users.

Finally Argo CD needs to be configured so it knows how to reach the
metrics server. In order to do so, add the following section in the
`argocd-cm`.

```
extension.config: |-
  extensions:
    - name: metrics
      backend:
        services:
          - url: <METRICS_SERVER_URL>
```

**Attention**: Make sure to change the `METRICS_SERVER_URL` to the URL
where argocd-metrics-server is configured. The metrics server URL
needs to be reacheable by the Argo CD API server.

## Contributing

### Build and Debug Locally

Copy the [config.json](app/config.json) file and update the `endpoints` section of [config.json](app/config.json) to use the endpoint URLs found [here](https://mozilla-hub.atlassian.net/wiki/spaces/CS1/pages/1280344209/Yardstick+Admin+Guide#GMP-Endpoints-for-Scoping-Projects). This copy will be mounted when starting the container in steps below.

```
cp app/config.json ~/config.json
# edit the copy to add urls
```

Dump a valid access token (one that has access to metrics in prometheus prod and nonprod scoping projects) into a `bearer.token` file. This will also be mounted in the container. If your google account has access to view them, simply run the following:
```
gcloud auth login
gcloud auth print-access-token > ~/bearer.token
```

For debugging, build the image with the `builder` target specified. This will set the entrypoint to `dlv debug /app/cmd/main.go --headless --listen=:9004`, which starts a delve server that waits for a debugger client to connect.
```
docker build -t us-west1-docker.pkg.dev/moz-fx-platform-artifacts/platform-shared-images/argocd-extension-metrics:latest-debug --target builder .
```

To run without debugging, build without a target specified:
```
docker build -t us-west1-docker.pkg.dev/moz-fx-platform-artifacts/platform-shared-images/argocd-extension-metrics:latest .
```

Start a container with ports `9003` (webserver) and `9004` (delve) bound, and mount both the `bearer.token` and a `config.json` file specifying the endpoints to use.
```
docker run -p 9003:9003 -p 9004:9004 --mount type=bind,src=~/config.json,dst=/app/app/config.json --mount type=bind,src=~/bearer.token,dst=/token/bearer.token --name metrics us-west1-docker.pkg.dev/moz-fx-platform-artifacts/platform-shared-images/argocd-extension-metrics:latest-debug
```

Container should now be started. If debug image was used, server will pause until a debugger attaches, so run `Attach to Process` from the debug pane in VS Code.

### Sending Requests to a Local Build

This argo extension is a [proxy extension](https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/proxy-extensions/), so by convention it expects requests from Argo CD to include mandatory headers (`Argocd-Application-Name` and `Argocd-Project-Name`). This extension in particular also expects some mandatory query parameters to be specified.

Here's an example request that can be used for testing. Replace the `<argo-application-name>`, `<argo-project-name>`, `<pod-name>`, and `<k8s-namespace>` placeholders with the relevant details for your pod.
```
curl '127.0.0.1:9003/api/applications/<argo-application-name>/groupkinds/pod/rows/cpu_limit/graphs/pod_cpu_limit_utilization?name=<pod-name>.*&namespace=<k8s-namespace>&application_name=<argo-application-name>&project=<argo-project-name>&duration=1h' \
-H "Argocd-Application-Name: <k8s-namespace>:<argo-application-name>" \
-H "Argocd-Project-Name: <argo-project-name>"
```

[1]: https://github.com/argoproj-labs/argocd-extension-installer
