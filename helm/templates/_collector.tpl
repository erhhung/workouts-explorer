{{- define "workouts.collector.enabled" -}}
{{- if and .Values.otel.collector.enabled .Values.otel.endpoint -}}true{{- end -}}
{{- end }}

{{- define "workouts.collector.container" -}}
- name: metrics-collector
  image: {{ .Values.otel.collector.image }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  args: [--config=/etc/otelcol/config.yaml]
  securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true}
  env:
    - name: POD_UID
      valueFrom: {fieldRef: {fieldPath: metadata.uid}}
    - {name: GOMEMLIMIT, value: 192MiB}
  ports: [{name: otel-health, containerPort: 13133}]
  livenessProbe: {httpGet: {path: /, port: otel-health}, failureThreshold: 6, timeoutSeconds: 5}
  readinessProbe: {httpGet: {path: /, port: otel-health}, failureThreshold: 6, timeoutSeconds: 5}
  resources: {{- toYaml .Values.otel.collector.resources | nindent 4 }}
  volumeMounts:
    - {name: collector-config, mountPath: /etc/otelcol, readOnly: true}
    - {name: collector-ca, mountPath: /var/run/secrets/workouts-otel, readOnly: true}
{{- end }}

{{- define "workouts.collector.volumes" -}}
{{- $root := index . 0 -}}{{- $component := index . 1 -}}
{{- $ca := $root.Values.api.trustedCASecret -}}
{{- if $root.Values.otel.trustedCASecret.name -}}{{- $ca = $root.Values.otel.trustedCASecret -}}{{- end -}}
- name: collector-config
  configMap: {name: {{ include "workouts.fullname" $root }}-{{ $component }}-collector}
- name: collector-ca
  secret:
    secretName: {{ required "collector trusted CA secret name is required" $ca.name }}
    # A CA certificate is public; collectors need no database/private-key access.
    defaultMode: 0444
    items: [{key: {{ required "collector trusted CA secret key is required" $ca.key }}, path: ca.crt}]
{{- end }}
