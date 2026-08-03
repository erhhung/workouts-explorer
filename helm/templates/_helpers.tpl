{{- define "workouts.name" -}}workouts-explorer{{- end }}
{{- define "workouts.fullname" -}}{{ include "workouts.name" . }}{{- end }}
{{- define "workouts.labels" -}}
app.kubernetes.io/name: {{ include "workouts.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "workouts.image" -}}
{{- $root := index . 0 -}}{{- $name := index . 1 -}}
{{- if $root.Values.image.registry }}{{ $root.Values.image.registry }}/{{ end }}{{ $name }}:{{ required "image.tag is required" $root.Values.image.tag }}
{{- end }}
