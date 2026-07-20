{{/*
차트 이름 (nameOverride 우선)
*/}}
{{- define "qdrant-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
완전한 이름 — release 이름을 접두. 클러스터 범위 리소스(ClusterRole 등) 충돌 방지.
*/}}
{{- define "qdrant-operator.fullname" -}}
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
Chart 라벨 값 (name-version)
*/}}
{{- define "qdrant-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
공통 라벨
*/}}
{{- define "qdrant-operator.labels" -}}
helm.sh/chart: {{ include "qdrant-operator.chart" . }}
{{ include "qdrant-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: qdrant-operator
{{- end }}

{{/*
셀렉터 라벨 (Deployment selector 에 사용 — 불변)
*/}}
{{- define "qdrant-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "qdrant-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
컨트롤러 매니저 리소스 이름 (Deployment/SA 공용)
*/}}
{{- define "qdrant-operator.managerName" -}}
{{- printf "%s-controller-manager" (include "qdrant-operator.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ServiceAccount 이름
*/}}
{{- define "qdrant-operator.serviceAccountName" -}}
{{- if ne .Values.serviceAccount.create false }}
{{- default (include "qdrant-operator.managerName" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
