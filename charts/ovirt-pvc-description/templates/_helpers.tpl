{{- define "ovirt-pvc-description.name" -}}
{{- .Values.deploymentName | default "ovirt-pvc-description" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ovirt-pvc-description.namespace" -}}
{{- .Values.namespace | default .Release.Namespace -}}
{{- end -}}

{{- define "ovirt-pvc-description.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "ovirt-pvc-description.name" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{- define "ovirt-pvc-description.labels" -}}
app: {{ include "ovirt-pvc-description.name" . }}
app.kubernetes.io/name: ovirt-pvc-description
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "ovirt-pvc-description.selectorLabels" -}}
app: {{ include "ovirt-pvc-description.name" . }}
{{- end -}}
