{{/*
Chart name, suitable for resource names. Truncated to 63 chars per k8s.
*/}}
{{- define "odigos-ai-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name; honours fullnameOverride.
*/}}
{{- define "odigos-ai-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Standard labels block applied to every object.
*/}}
{{- define "odigos-ai-agent.labels" -}}
app.kubernetes.io/name: {{ include "odigos-ai-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: odigos
odigos.io/system-object: "true"
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
Selector labels - never change after install.
*/}}
{{- define "odigos-ai-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "odigos-ai-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Build an image reference. Honours an embedded registry in `repository`
(detected by a `.` or `:` before the first `/`) and falls back to
`Chart.AppVersion` when `tag` is empty.

Usage: {{ include "odigos-ai-agent.image" (dict "image" .Values.image.agent "registry" .Values.image.registry "defaultTag" .Chart.AppVersion) }}
*/}}
{{- define "odigos-ai-agent.image" -}}
{{- $image := .image -}}
{{- $registry := .registry -}}
{{- $defaultTag := .defaultTag -}}
{{- $tag := default $defaultTag $image.tag -}}
{{- $repo := $image.repository -}}
{{- $first := first (splitList "/" $repo) -}}
{{- if and (ne $first $repo) (or (contains "." $first) (contains ":" $first)) -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- printf "%s/%s:%s" $registry $repo $tag -}}
{{- end -}}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "odigos-ai-agent.serviceAccountName" -}}
{{- default (include "odigos-ai-agent.fullname" .) .Values.serviceAccount.name | default (include "odigos-ai-agent.fullname" .) -}}
{{- end -}}

{{/*
Resolve the Anthropic secret name. Either the chart-managed one or the
user-provided existing secret.
*/}}
{{- define "odigos-ai-agent.anthropicSecretName" -}}
{{- if .Values.anthropic.existingSecret -}}
{{- .Values.anthropic.existingSecret -}}
{{- else -}}
{{- printf "%s-anthropic" (include "odigos-ai-agent.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Resolve the agent-token secret name.
*/}}
{{- define "odigos-ai-agent.agentTokenSecretName" -}}
{{- if .Values.agentToken.existingSecret -}}
{{- .Values.agentToken.existingSecret -}}
{{- else -}}
{{- printf "%s-token" (include "odigos-ai-agent.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Resolve the GitHub-token secret name (optional).
*/}}
{{- define "odigos-ai-agent.githubSecretName" -}}
{{- if .Values.github.existingSecret -}}
{{- .Values.github.existingSecret -}}
{{- else -}}
{{- printf "%s-github" (include "odigos-ai-agent.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
True when a GitHub token is configured (either inline value or existing secret).
*/}}
{{- define "odigos-ai-agent.githubEnabled" -}}
{{- if or .Values.github.token .Values.github.existingSecret -}}true{{- end -}}
{{- end -}}

{{/*
Render imagePullSecrets list.
*/}}
{{- define "odigos-ai-agent.pullSecrets" -}}
{{- with .Values.imagePullSecrets -}}
imagePullSecrets:
{{- range . }}
  - name: {{ .name }}
{{- end }}
{{- end -}}
{{- end -}}
