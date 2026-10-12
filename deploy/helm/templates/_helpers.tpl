{{/*
Expand the name of the chart.
*/}}
{{- define "ephemeron.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "ephemeron.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "ephemeron.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Manager fully qualified name.
*/}}
{{- define "ephemeron.manager.fullname" -}}
{{- printf "%s-manager" (include "ephemeron.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Manager common labels.
*/}}
{{- define "ephemeron.manager.labels" -}}
helm.sh/chart: {{ include "ephemeron.chart" . }}
{{ include "ephemeron.manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Manager selector labels.
*/}}
{{- define "ephemeron.manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ephemeron.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: manager
{{- end }}

{{/*
Registry fully qualified name.
*/}}
{{- define "ephemeron.registry.fullname" -}}
{{- printf "%s-registry" (include "ephemeron.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Registry common labels.
*/}}
{{- define "ephemeron.registry.labels" -}}
helm.sh/chart: {{ include "ephemeron.chart" . }}
{{ include "ephemeron.registry.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Registry selector labels.
*/}}
{{- define "ephemeron.registry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ephemeron.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: registry
{{- end }}

{{/*
Manager image reference, also used by the registry proxy sidecar.
*/}}
{{- define "ephemeron.manager.image" -}}
{{ .Values.manager.image.repository }}{{ if .Values.manager.image.digest }}@{{ .Values.manager.image.digest }}{{ else }}:{{ coalesce .Values.manager.image.tag .Chart.AppVersion }}{{ end }}
{{- end }}

{{/*
Registry port that external traffic targets: the proxy when immutability is enforced.
*/}}
{{- define "ephemeron.registry.publicPort" -}}
{{ if .Values.manager.env.immutableTagPatterns }}5080{{ else }}5000{{ end }}
{{- end }}
