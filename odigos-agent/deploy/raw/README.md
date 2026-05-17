# Raw kustomize manifests (dev install)

For devs without Helm. Same objects the Helm chart renders, with concrete
placeholders that you must replace before applying.

## Replace placeholders

1. Copy `anthropic.env.example` to `anthropic.env` and fill in the API
   key. Same for `token.env.example` -> `token.env`. The local
   `deploy/raw/.gitignore` excludes `*.env` so these stay out of git.
2. Edit `kustomization.yaml` and bump the image tags under `images:` if you
   are not building locally.
3. (Optional) Drop the `mcp-graph` container by deleting it from
   `deployment.yaml` and removing the matching env vars / probes - the
   Helm chart's `codebaseGraph.enabled=false` is the equivalent knob there.

## Apply

```sh
kubectl create namespace odigos-system  # if not already present
kubectl apply -k odigos-agent/deploy/raw
```

The agent service is reachable inside the cluster at
`odigos-ai-agent.odigos-system:8765`.

## Why both raw and Helm?

The Helm chart is the supported install path. The raw manifests are a
faster inner loop while iterating on the agent itself - no chart re-render,
no values flag, easy to `sed` and re-apply.
