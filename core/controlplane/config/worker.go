package config

var CloudConfigWorker = []byte(`{{ define "instance-script" -}}
{{- $S3URI := self.Parts.s3.Asset.S3URL -}}
 . /etc/environment
export COREOS_PRIVATE_IPV4 COREOS_PRIVATE_IPV6 COREOS_PUBLIC_IPV4 COREOS_PUBLIC_IPV6
REGION=$(curl -s http://169.254.169.254/latest/dynamic/instance-identity/document | jq -r '.region')
USERDATA_FILE=userdata-worker

run() {
  bin="$1"; shift
  while ! /usr/bin/rkt run \
    --net=host \
    --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf \
    --volume=awsenv,kind=host,source=/var/run/coreos,readOnly=false --mount volume=awsenv,target=/var/run/coreos \
    --volume=envfile,kind=host,source={{.StackNameEnvFileName}},readOnly=false --mount volume=envfile,target={{.StackNameEnvFileName}}  \
    --trust-keys-from-https \
    {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=$bin -- "$@"; do
      sleep 1
  done
}
run bash -c "aws configure set s3.signature_version s3v4; aws s3 --region $REGION cp {{ $S3URI }} /var/run/coreos/$USERDATA_FILE"

INSTANCE_ID=$(curl -s http://169.254.169.254/latest/meta-data/instance-id)

exec /usr/bin/coreos-cloudinit --from-file /var/run/coreos/$USERDATA_FILE
{{ end }}

{{ define "instance" -}}
{ "Fn::Base64": { "Fn::Join" : ["", [
  "#!/bin/bash -xe\n",
  {"Fn::Join":["",[ "echo '{{.StackNameEnvVarName}}=", { "Ref": "AWS::StackName" }, "' >> {{.StackNameEnvFileName}}\n" ]]},
  {{ (execTemplate "instance-script" .) | toJSON  }}
]]}}
{{ end }}

{{ define "s3" -}}
#cloud-config
coreos:
  update:
    reboot-strategy: "off"
  flannel:
    interface: $private_ipv4
    etcd_cafile: /etc/kubernetes/ssl/etcd-trusted-ca.pem
    etcd_certfile: /etc/kubernetes/ssl/etcd-client.pem
    etcd_keyfile: /etc/kubernetes/ssl/etcd-client-key.pem

  units:
{{- range $u := .CustomSystemdUnits}}
    - name: {{$u.Name}}
      {{- if $u.Command }}
      command: {{ $u.Command }}
      {{- end}}
      {{- if $u.Enable }}
      enable: {{$u.Enable}}
      {{- end }}
      {{- if $u.Runtime }}
      runtime: {{$u.Runtime}}
      {{- end }}
      {{- if $u.DropInsPresent }}
      drop-ins:
        {{- range $d := $u.DropIns }}
        - name: {{ $d.Name }}
          content: |
            {{- range $i := $d.ContentArray }}
            {{ $i }}
            {{- end}}
        {{- end }}
      {{- end}}
      {{- if $u.ContentPresent }}
      content: |
        {{- range $l := $u.ContentArray }}
        {{ $l }}
        {{- end }}
      {{- end}}
{{- end}}
    - name: systemd-modules-load.service
      command: restart
{{range $volumeMountSpecIndex, $volumeMountSpec := .VolumeMounts}}
    - name: format-{{$volumeMountSpec.SystemdMountName}}.service
      command: start
      content: |
        [Unit]
        Description=Formats the EBS persistent volume drive for {{$volumeMountSpec.Device}}
        Before=local-fs-pre.target

        [Service]
        Type=oneshot
        ExecStart=-/usr/sbin/mkfs.xfs {{$volumeMountSpec.Device}}

        [Install]
        WantedBy=local-fs-pre.target

    - name: {{$volumeMountSpec.SystemdMountName}}.mount
      command: start
      content: |
        [Unit]
        Description=Mount volume to {{$volumeMountSpec.Path}}

        [Mount]
        What={{$volumeMountSpec.Device}}
        Where={{$volumeMountSpec.Path}}
{{end}}
{{if and (.AmazonSsmAgent.Enabled) (ne .AmazonSsmAgent.DownloadUrl "")}}
    - name: amazon-ssm-agent.service
      command: start
      enable: true
      content: |
        [Unit]
        Description=amazon-ssm-agent
        Requires=network-online.target
        After=network-online.target

        [Service]
        Type=simple
        ExecStartPre=/opt/ssm/bin/install-ssm-agent.sh
        ExecStart=/opt/ssm/bin/amazon-ssm-agent
        KillMode=control-group
        Restart=on-failure
        RestartSec=1min

        [Install]
        WantedBy=network-online.target
{{end}}
{{if .CloudWatchLogging.Enabled}}
    - name: journald-cloudwatch-logs.service
      command: start
      content: |
        [Unit]
        Description=Docker run journald-cloudwatch-logs to send journald logs to CloudWatch
        Requires=network-online.target
        After=network-online.target

        [Service]
        ExecStartPre=-/usr/bin/mkdir -p /var/journald-cloudwatch-logs
        ExecStart=/usr/bin/rkt run \
                  --insecure-options=image \
                  --volume resolv,kind=host,source=/etc/resolv.conf,readOnly=true \
                  --mount volume=resolv,target=/etc/resolv.conf \
                  --volume journald-cloudwatch-logs,kind=host,source=/var/journald-cloudwatch-logs \
                  --mount volume=journald-cloudwatch-logs,target=/var/journald-cloudwatch-logs \
                  --volume journal,kind=host,source=/var/log/journal,readOnly=true \
                  --mount volume=journal,target=/var/log/journal \
                  --volume machine-id,kind=host,source=/etc/machine-id,readOnly=true \
                  --mount volume=machine-id,target=/etc/machine-id \
                  --uuid-file-save=/var/journald-cloudwatch-logs/journald-cloudwatch-logs.uuid \
                  {{ .JournaldCloudWatchLogsImage.RktRepo }} -- {{.ClusterName}}
        ExecStopPost=/usr/bin/rkt rm --uuid-file=/var/journald-cloudwatch-logs/journald-cloudwatch-logs.uuid
        Restart=always
        RestartSec=60s

        [Install]
        WantedBy=multi-user.target
{{end}}
    - name: cfn-etcd-environment.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Fetches etcd static IP addresses list from CF
        After=network-online.target

        [Service]
        EnvironmentFile={{.StackNameEnvFileName}}
        Restart=on-failure
        RemainAfterExit=true
        ExecStartPre=/opt/bin/cfn-etcd-environment
        ExecStart=/usr/bin/mv -f /var/run/coreos/etcd-environment /etc/etcd-environment

    - name: docker.service
      drop-ins:
{{if .Experimental.EphemeralImageStorage.Enabled}}
        - name: 10-docker-mount.conf
          content: |
            [Unit]
            After=var-lib-docker.mount
            Wants=var-lib-docker.mount
{{end}}
        - name: 10-post-start-check.conf
          content: |
            [Service]
            RestartSec=10
            ExecStartPost=/usr/bin/docker pull {{.PauseImage.RepoWithTag}}

        - name: 40-flannel.conf
          content: |
            [Unit]
            Wants=flanneld.service
            [Service]
            EnvironmentFile=/etc/kubernetes/cni/docker_opts_cni.env
            ExecStartPre=/usr/bin/systemctl is-active flanneld.service

        - name: 60-logfilelimit.conf
          content: |
            [Service]
            Environment="DOCKER_OPTS=--log-opt max-size=50m --log-opt max-file=3"

    - name: flanneld.service
      drop-ins:
        - name: 10-etcd.conf
          content: |
            [Unit]
            Wants=cfn-etcd-environment.service
            After=cfn-etcd-environment.service

            [Service]
            EnvironmentFile=-/etc/etcd-environment
            EnvironmentFile=-/run/flannel/etcd-endpoints.opts
            ExecStartPre=/usr/bin/systemctl is-active cfn-etcd-environment.service
            ExecStartPre=/bin/sh -ec "echo FLANNELD_ETCD_ENDPOINTS=${ETCD_ENDPOINTS} >/run/flannel/etcd-endpoints.opts"
            {{- if .AssetsEncryptionEnabled}}
            ExecStartPre=/opt/bin/decrypt-assets
            {{- end}}
            Environment="ETCD_SSL_DIR=/etc/kubernetes/ssl"
            TimeoutStartSec=120

{{if .FlannelImage.RktPullDocker}}
        - name: 20-flannel-custom-image.conf
          content: |
            [Unit]
            PartOf=flanneld.service
            Before=docker.service

            [Service]
            Environment="FLANNEL_IMAGE={{.FlannelImage.RktRepo}}"
            Environment="RKT_RUN_ARGS={{.FlannelImage.Options}}"

    - name: flannel-docker-opts.service
      drop-ins:
        - name: 10-flannel-docker-options.conf
          content: |
            [Unit]
            PartOf=flanneld.service
            Before=docker.service

            [Service]
            Environment="FLANNEL_IMAGE={{.FlannelImage.RktRepo}}"
            Environment="RKT_RUN_ARGS={{.FlannelImage.Options}} --uuid-file-save=/var/lib/coreos/flannel-wrapper2.uuid"
{{end}}
    - name: kubelet.service
      command: start
      runtime: true
      content: |
        [Unit]
        Wants=flanneld.service cfn-etcd-environment.service
        After=cfn-etcd-environment.service
        {{- if .Gpu.Nvidia.IsEnabledOn .InstanceType }}
        Requires=nvidia-start.service
        After=nvidia-start.service
        {{- end }}
        [Service]
        EnvironmentFile=/etc/environment
        EnvironmentFile=-/etc/etcd-environment
        EnvironmentFile=-/etc/default/kubelet
        Environment=KUBELET_IMAGE_TAG={{.K8sVer}}
        Environment=KUBELET_IMAGE_URL={{.HyperkubeImage.RktRepoWithoutTag}}
        Environment="RKT_RUN_ARGS=--volume dns,kind=host,source=/etc/resolv.conf {{.HyperkubeImage.Options}}\
        --set-env=ETCD_CA_CERT_FILE=/etc/kubernetes/ssl/etcd-trusted-ca.pem \
        --set-env=ETCD_CERT_FILE=/etc/kubernetes/ssl/etcd-client.pem \
        --set-env=ETCD_KEY_FILE=/etc/kubernetes/ssl/etcd-client-key.pem \
        --mount volume=dns,target=/etc/resolv.conf \
        {{ if eq .ContainerRuntime "rkt" -}}
        --volume rkt,kind=host,source=/opt/bin/host-rkt \
        --mount volume=rkt,target=/usr/bin/rkt \
        --volume var-lib-rkt,kind=host,source=/var/lib/rkt \
        --mount volume=var-lib-rkt,target=/var/lib/rkt \
        --volume stage,kind=host,source=/tmp \
        --mount volume=stage,target=/tmp \
        {{ end -}}
        --volume var-lib-cni,kind=host,source=/var/lib/cni \
        --mount volume=var-lib-cni,target=/var/lib/cni \
        --volume var-log,kind=host,source=/var/log \
        --mount volume=var-log,target=/var/log{{ if .UseCalico }} \
        --volume cni-bin,kind=host,source=/opt/cni/bin \
        --mount volume=cni-bin,target=/opt/cni/bin{{ end }}"
        ExecStartPre=/usr/bin/systemctl is-active flanneld.service
        ExecStartPre=/usr/bin/systemctl is-active cfn-etcd-environment.service
        ExecStartPre=/usr/bin/mkdir -p /var/lib/cni
        ExecStartPre=/usr/bin/mkdir -p /var/log/containers
        ExecStartPre=/usr/bin/mkdir -p /opt/cni/bin
        ExecStartPre=/usr/bin/mkdir -p /etc/kubernetes/manifests
        ExecStartPre=/bin/sh -ec "find /etc/kubernetes/manifests /etc/kubernetes/cni/net.d/  -maxdepth 1 -type f | xargs --no-run-if-empty sed -i 's|#ETCD_ENDPOINTS#|${ETCD_ENDPOINTS}|'"
        ExecStartPre=/usr/bin/etcdctl \
                       --ca-file /etc/kubernetes/ssl/etcd-trusted-ca.pem \
                       --key-file /etc/kubernetes/ssl/etcd-client-key.pem \
                       --cert-file /etc/kubernetes/ssl/etcd-client.pem \
                       --endpoints "${ETCD_ENDPOINTS}" \
                       cluster-health
        {{if .UseCalico -}}
        ExecStartPre=/usr/bin/docker run --rm -e SLEEP=false -e KUBERNETES_SERVICE_HOST= -e KUBERNETES_SERVICE_PORT= -v /opt/cni/bin:/host/opt/cni/bin {{ .CalicoCniImage.RepoWithTag }} /install-cni.sh
        {{end -}}
        ExecStart=/usr/lib/coreos/kubelet-wrapper \
        --cni-conf-dir=/etc/kubernetes/cni/net.d \
        {{/* Work-around until https://github.com/kubernetes/kubernetes/issues/43967 is fixed via https://github.com/kubernetes/kubernetes/pull/43995 */ -}}
        --cni-bin-dir=/opt/cni/bin \
        --network-plugin={{.K8sNetworkPlugin}} \
        --container-runtime={{.ContainerRuntime}} \
        --rkt-path=/usr/bin/rkt \
        --rkt-stage1-image=coreos.com/rkt/stage1-coreos \
        --node-labels kubernetes.io/role=node{{if .NodeLabels.Enabled}},{{.NodeLabels.String}}{{end}} \
        --register-node=true \
        {{if .Taints}}--register-with-taints={{.Taints.String}}\
        {{end}}--allow-privileged=true \
        {{if .NodeStatusUpdateFrequency}}--node-status-update-frequency={{.NodeStatusUpdateFrequency}} \
        {{end}}--pod-manifest-path=/etc/kubernetes/manifests \
        {{ if .KubeDns.NodeLocalResolver }}--cluster-dns=${COREOS_PRIVATE_IPV4} \
        {{ else }}--cluster-dns={{.DNSServiceIP}} \
        {{ end }}--cluster-domain=cluster.local \
        --cloud-provider=aws \
        --cert-dir=/etc/kubernetes/ssl \
        {{- if and .Experimental.TLSBootstrap.Enabled .AssetsConfig.HasTLSBootstrapToken }}
        --experimental-bootstrap-kubeconfig=/etc/kubernetes/kubeconfig/worker-bootstrap.yaml \
        {{- if .Kubelet.RotateCerts.Enabled }}
        --rotate-certificates \
        {{- end }}
        {{- else }}
        --tls-cert-file=/etc/kubernetes/ssl/worker.pem \
        --tls-private-key-file=/etc/kubernetes/ssl/worker-key.pem \
        {{- end }}
        --kubeconfig=/etc/kubernetes/kubeconfig/worker.yaml \
        {{- if .FeatureGates.Enabled }}
        --feature-gates="{{.FeatureGates.String}}" \
        {{- end }}
        --require-kubeconfig \
        $KUBELET_OPTS
        Restart=always
        RestartSec=10
        [Install]
        WantedBy=multi-user.target

{{ if eq .ContainerRuntime "rkt" }}
    - name: rkt-api.service
      enable: true
      content: |
        [Unit]
        Before=kubelet.service
        [Service]
        ExecStart=/usr/bin/rkt api-service
        Restart=always
        RestartSec=10
        [Install]
        RequiredBy=kubelet.service

    - name: load-rkt-stage1.service
      enable: true
      content: |
        [Unit]
        Description=Load rkt stage1 images
        Documentation=http://github.com/coreos/rkt
        Requires=network-online.target
        After=network-online.target
        Before=rkt-api.service
        [Service]
        Type=oneshot
        RemainAfterExit=yes
        ExecStart=/usr/bin/rkt fetch /usr/lib/rkt/stage1-images/stage1-coreos.aci /usr/lib/rkt/stage1-images/stage1-fly.aci  --insecure-options=image
        [Install]
        RequiredBy=rkt-api.service
{{ end }}

{{if .AwsEnvironment.Enabled}}
    - name: set-aws-environment.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Set AWS environment variables in /etc/aws-environment
        After=network-online.target

        [Service]
        Type=oneshot
        EnvironmentFile={{.StackNameEnvFileName}}
        RemainAfterExit=true
        ExecStartPre=/bin/touch /etc/aws-environment
        ExecStart=/opt/bin/set-aws-environment
{{end}}

{{if .SpotFleet.Enabled}}
    - name: tag-spot-instance.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Tag this spot instance with cluster name
        After=network-online.target

        [Service]
        Type=oneshot
        RemainAfterExit=true
        ExecStart=/opt/bin/tag-spot-instance

{{if .LoadBalancer.Enabled}}
    - name: add-to-load-balancers.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Add this spot instance to load balancers
        After=network-online.target

        [Service]
        Type=oneshot
        RemainAfterExit=true
        ExecStart=/opt/bin/add-to-load-balancers
{{end}}

{{if .TargetGroup.Enabled}}
    - name: add-to-target-groups.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Add this spot instance to target groups
        After=network-online.target

        [Service]
        Type=oneshot
        RemainAfterExit=true
        ExecStart=/opt/bin/add-to-target-groups
{{end}}
{{end}}

{{ if $.ElasticFileSystemID }}
    - name: rpc-statd.service
      command: start
      enable: true
    - name: efs.service
      command: start
      content: |
        [Unit]
        After=network-online.target
        [Service]
        Type=oneshot
        ExecStartPre=-/usr/bin/mkdir -p /efs
        ExecStart=/bin/sh -c 'grep -qs /efs /proc/mounts || /usr/bin/mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2 $(/usr/bin/curl -s http://169.254.169.254/latest/meta-data/placement/availability-zone).{{ $.ElasticFileSystemID }}.efs.{{ $.Region }}.amazonaws.com:/ /efs'
        ExecStop=/usr/bin/umount /efs
        RemainAfterExit=yes
        [Install]
        WantedBy=kubelet.service
{{ end }}

{{ if .WaitSignal.Enabled }}
    - name: cfn-signal.service
      command: start
      content: |
        [Unit]
        Wants=kubelet.service docker.service
        After=kubelet.service

        [Service]
        Type=oneshot
        EnvironmentFile={{.StackNameEnvFileName}}
        ExecStartPre=/usr/bin/bash -c "while sleep 1; do if /usr/bin/curl  --insecure -s -m 20 -f  https://127.0.0.1:10250/healthz > /dev/null ; then break ; fi;  done"
        {{ if .UseCalico }}
        ExecStartPre=/usr/bin/bash -c "until /usr/bin/docker run --net=host --pid=host --rm {{ .CalicoCtlImage.RepoWithTag }} node status > /dev/null; do sleep 3; done && echo Calico running"
        {{ end }}
        ExecStart=/opt/bin/cfn-signal
{{end}}

{{if .Experimental.AwsNodeLabels.Enabled }}
    - name: kube-node-label.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Label this kubernetes node with additional AWS parameters
        After=kubelet.service
        Before=cfn-signal.service

        [Service]
        Type=oneshot
        ExecStop=/bin/true
        RemainAfterExit=true
        ExecStart=/opt/bin/kube-node-label
{{end}}

{{if .Experimental.EphemeralImageStorage.Enabled}}
    - name: format-ephemeral.service
      command: start
      content: |
        [Unit]
        Description=Formats the ephemeral drive
        ConditionFirstBoot=yes
        After=dev-{{.Experimental.EphemeralImageStorage.Disk}}.device
        Requires=dev-{{.Experimental.EphemeralImageStorage.Disk}}.device
        [Service]
        Type=oneshot
        RemainAfterExit=yes
        ExecStart=/usr/sbin/wipefs -f /dev/{{.Experimental.EphemeralImageStorage.Disk}}
        ExecStart=/usr/sbin/mkfs.{{.Experimental.EphemeralImageStorage.Filesystem}} -f /dev/{{.Experimental.EphemeralImageStorage.Disk}}
    - name: var-lib-docker.mount
      command: start
      content: |
        [Unit]
        Description=Mount ephemeral to /var/lib/docker
        Requires=format-ephemeral.service
        After=format-ephemeral.service
        [Mount]
        What=/dev/{{.Experimental.EphemeralImageStorage.Disk}}
{{if eq .ContainerRuntime "docker"}}
        Where=/var/lib/docker
{{else if eq .ContainerRuntime "rkt"}}
        Where=/var/lib/rkt
{{end}}
        Type={{.Experimental.EphemeralImageStorage.Filesystem}}
{{end}}

{{if .Gpu.Nvidia.IsEnabledOn .InstanceType}}
    - name: nvidia-start.service
      enable: false
      content: |
        [Unit]
        Description=Load NVIDIA module
        After=local-fs.target

        [Service]
        Type=oneshot
        RemainAfterExit=true
        ExecStartPre=/opt/nvidia-build/util/retry.sh 0 /opt/nvidia-build/build-and-install.sh
        TimeoutStartSec=900
        ExecStart=/opt/nvidia/current/bin/nvidia-start.sh

        [Install]
        WantedBy=multi-user.target

    - name: nvidia-persistenced.service
      enable: false
      content: |
        [Unit]
        Description=NVIDIA Persistence Daemon
        Wants=local-fs.target

        [Service]
        Type=forking
        ExecStart=/opt/nvidia/current/bin/nvidia-persistenced --user nvidia-persistenced --no-persistence-mode --verbose
        ExecStopPost=/bin/rm -rf /var/run/nvidia-persistenced
{{end}}

{{if .SSHAuthorizedKeys}}
ssh_authorized_keys:
  {{range $sshkey := .SSHAuthorizedKeys}}
  - {{$sshkey}}
  {{end}}
{{end}}
{{if .Region.IsChina}}
    - name: pause-amd64.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Pull and tag a mirror image for pause-amd64
        Wants=docker.service
        After=docker.service

        [Service]
        Restart=on-failure
        RemainAfterExit=true
        ExecStartPre=/usr/bin/systemctl is-active docker.service
        ExecStartPre=/usr/bin/docker pull {{.PauseImage.RepoWithTag}}
        ExecStart=/usr/bin/docker tag {{.PauseImage.RepoWithTag}} gcr.io/google_containers/pause-amd64:3.0
        ExecStop=/bin/true
        [Install]
        WantedBy=kubelet.service
{{end}}

{{if .Gpu.Nvidia.IsEnabledOn .InstanceType}}
users:
  - name: nvidia-persistenced
    gecos: NVIDIA Persistence Daemon
    homedir: /
    shell: /sbin/nologin
{{end}}

write_files:
  - path: /etc/ssh/sshd_config
    permissions: 0600
    owner: root:root
    content: |
      UsePrivilegeSeparation sandbox
      Subsystem sftp internal-sftp
      ClientAliveInterval 180
      UseDNS no
      UsePAM yes
      PrintLastLog no # handled by PAM
      PrintMotd no # handled by PAM
      PasswordAuthentication no
      ChallengeResponseAuthentication no
{{- if .CustomFiles}}
  {{- range $w := .CustomFiles}}
  - path: {{$w.Path}}
    permissions: {{$w.PermissionsString}}
    encoding: gzip+base64
    content: {{$w.GzippedBase64Content}}
  {{- end }}
{{- end }}
  - path: /etc/modules-load.d/ip_vs.conf
    content: |
      ip_vs
      ip_vs_rr
      ip_vs_wrr
      ip_vs_sh
      nf_conntrack_ipv4
{{if and (.AmazonSsmAgent.Enabled) (ne .AmazonSsmAgent.DownloadUrl "")}}
  - path: "/opt/ssm/bin/install-ssm-agent.sh"
    permissions: 0700
    content: |
      #!/bin/bash
      set -e

      TARGET_DIR=/opt/ssm
      if [[ -f "${TARGET_DIR}"/bin/amazon-ssm-agent ]]; then
        exit 0
      fi

      TMP_DIR=$(mktemp -d)
      trap "rm -rf ${TMP_DIR}" EXIT

      TAR_FILE=ssm.linux-amd64.tar.gz
      CHECKSUM_FILE="${TAR_FILE}.sha1"

      echo -n "{{ .AmazonSsmAgent.Sha1Sum }} ${TMP_DIR}/${TAR_FILE}" > "${TMP_DIR}/${CHECKSUM_FILE}"

      curl --silent -L -o "${TMP_DIR}/${TAR_FILE}" "{{ .AmazonSsmAgent.DownloadUrl }}"

      sha1sum --quiet -c "${TMP_DIR}/${CHECKSUM_FILE}"

      tar zfx "${TMP_DIR}"/"${TAR_FILE}" -C "${TMP_DIR}"
      chown -R root:root "${TMP_DIR}"/ssm

      CONFIG_DIR=/etc/amazon/ssm
      mkdir -p "${CONFIG_DIR}"
      mv -f "${TMP_DIR}"/ssm/amazon-ssm-agent.json "${CONFIG_DIR}"/amazon-ssm-agent.json
      mv -f "${TMP_DIR}"/ssm/seelog_unix.xml "${CONFIG_DIR}"/seelog.xml

      mv -f "${TMP_DIR}"/ssm/* "${TARGET_DIR}"/bin/

{{end}}
{{if .AwsEnvironment.Enabled}}
  - path: /opt/bin/set-aws-environment
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      rkt run \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true \
        --mount volume=dns,target=/etc/resolv.conf \
        --volume=awsenv,kind=host,source=/etc/aws-environment,readOnly=false \
        --mount volume=awsenv,target=/etc/aws-environment \
        --uuid-file-save=/var/run/coreos/set-aws-environment.uuid \
        --net=host \
        --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -ec \
          '
            cfn-init -v -c "aws-environment" --region {{.Region}} --resource {{.LogicalName}} --stack '${{.StackNameEnvVarName}}'
          '

      rkt rm --uuid-file=/var/run/coreos/set-aws-environment.uuid || :
{{end}}

  {{if .Experimental.AwsNodeLabels.Enabled -}}
  - path: /opt/bin/kube-node-label
    permissions: 0700
    owner: root:root
    content: |
      #!/bin/bash -e
      set -ue

      INSTANCE_ID="$(/usr/bin/curl -s http://169.254.169.254/latest/meta-data/instance-id)"
      SECURITY_GROUPS="$(/usr/bin/curl -s http://169.254.169.254/latest/meta-data/security-groups | tr '\n' ',')"
      {{if not .SpotFleet.Enabled -}}
      AUTOSCALINGGROUP="$(/usr/bin/docker run --rm --net=host \
        {{.AWSCliImage.RepoWithTag}} aws \
        autoscaling describe-auto-scaling-instances \
        --instance-ids ${INSTANCE_ID} --region {{.Region}} \
        --query 'AutoScalingInstances[].AutoScalingGroupName' --output text)"
      LAUNCHCONFIGURATION="$(/usr/bin/docker run --rm --net=host \
        {{.AWSCliImage.RepoWithTag}} \
        aws autoscaling describe-auto-scaling-groups \
        --auto-scaling-group-name $AUTOSCALINGGROUP --region {{.Region}} \
        --query 'AutoScalingGroups[].LaunchConfigurationName' --output text)"
      {{end -}}

      label() {
        /usr/bin/docker run --rm -t --net=host \
          -v /etc/kubernetes:/etc/kubernetes \
          -v /etc/resolv.conf:/etc/resolv.conf \
          -e INSTANCE_ID=${INSTANCE_ID} \
          -e SECURITY_GROUPS=${SECURITY_GROUPS} \
          {{if not .SpotFleet.Enabled -}}
          -e AUTOSCALINGGROUP=${AUTOSCALINGGROUP} \
          -e LAUNCHCONFIGURATION=${LAUNCHCONFIGURATION} \
          {{end -}}
          {{.HyperkubeImage.RepoWithTag}} /bin/bash \
            -ec 'echo "placing labels and annotations with additional AWS parameters."; \
             kctl="/kubectl --server={{.APIEndpointURL}}:443 --kubeconfig=/etc/kubernetes/kubeconfig/worker.yaml"; \
             kctl_label="$kctl label --overwrite nodes/$(hostname)"; \
             kctl_annotate="$kctl annotate --overwrite nodes/$(hostname)"; \
             {{if not .SpotFleet.Enabled -}}
             $kctl_label kube-aws.coreos.com/autoscalinggroup=${AUTOSCALINGGROUP}; \
             $kctl_label kube-aws.coreos.com/launchconfiguration=${LAUNCHCONFIGURATION}; \
             {{end -}}
             $kctl_annotate kube-aws.coreos.com/securitygroups=${SECURITY_GROUPS}; \
             echo "done."'
      }

      set +e

      max_attempts=5
      attempt_num=0
      attempt_initial_interval_sec=1

      until label
      do
        ((attempt_num++))
        if (( attempt_num == max_attempts ))
        then
            echo "Attempt $attempt_num failed and there are no more attempts left!"
            return 1
        else
            attempt_interval_sec=$((attempt_initial_interval_sec*2**$((attempt_num-1))))
            echo "Attempt $attempt_num failed! Trying again in $attempt_interval_sec seconds..."
            sleep $attempt_interval_sec;
        fi
      done

  {{end -}}

  - path: /opt/bin/cfn-signal
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      rkt run \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true \
        --mount volume=dns,target=/etc/resolv.conf \
        --volume=awsenv,kind=host,source=/var/run/coreos,readOnly=false \
        --mount volume=awsenv,target=/var/run/coreos \
        --uuid-file-save=/var/run/coreos/cfn-signal.uuid \
        --net=host \
        --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -ec \
          '
            cfn-signal -e 0 --region {{.Region}} --resource {{.LogicalName}} --stack '${{.StackNameEnvVarName}}'
          '

      rkt rm --uuid-file=/var/run/coreos/cfn-signal.uuid || :

  - path: /opt/bin/cfn-etcd-environment
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      rkt run \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true \
        --mount volume=dns,target=/etc/resolv.conf \
        --volume=awsenv,kind=host,source=/var/run/coreos,readOnly=false \
        --mount volume=awsenv,target=/var/run/coreos \
        --uuid-file-save=/var/run/coreos/cfn-etcd-environment.uuid \
        --net=host \
        --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -ec \
          '
            cfn-init -v -c "etcd-client" --region {{.Region}} --resource {{.LogicalName}} --stack '${{.StackNameEnvVarName}}'
          '

      rkt rm --uuid-file=/var/run/coreos/cfn-etcd-environment.uuid || :

  - path: /etc/default/kubelet
    permissions: 0755
    owner: root:root
    content: |
      KUBELET_OPTS="{{.Experimental.KubeletOpts}}"

  - path: /etc/kubernetes/cni/docker_opts_cni.env
    content: |
      DOCKER_OPT_BIP=""
      DOCKER_OPT_IPMASQ=""

  - path: /opt/bin/host-rkt
    permissions: 0755
    owner: root:root
    content: |
      #!/bin/sh
      # This is bind mounted into the kubelet rootfs and all rkt shell-outs go
      # through this rkt wrapper. It essentially enters the host mount namespace
      # (which it is already in) only for the purpose of breaking out of the chroot
      # before calling rkt. It makes things like rkt gc work and avoids bind mounting
      # in certain rkt filesystem dependancies into the kubelet rootfs. This can
      # eventually be obviated when the write-api stuff gets upstream and rkt gc is
      # through the api-server. Related issue:
      # https://github.com/coreos/rkt/issues/2878
      exec nsenter -m -u -i -n -p -t 1 -- /usr/bin/rkt "$@"

{{ if .ManageCertificates }}

  - path: /etc/kubernetes/ssl/etcd-client.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdClientCert}}

  - path: /etc/kubernetes/ssl/etcd-client-key.pem{{if .AssetsEncryptionEnabled}}.enc{{end}}
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdClientKey}}

  - path: /etc/kubernetes/ssl/etcd-trusted-ca.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdTrustedCA}}

{{ if not .Experimental.TLSBootstrap.Enabled }}
  - path: /etc/kubernetes/ssl/worker.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.WorkerCert}}

  - path: /etc/kubernetes/ssl/worker-key.pem{{if .AssetsEncryptionEnabled}}.enc{{end}}
    encoding: gzip+base64
    content: {{.AssetsConfig.WorkerKey}}
{{ end }}

  - path: /etc/kubernetes/ssl/ca.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.CACert}}

{{ end }}

{{ if .AssetsEncryptionEnabled }}
  - path: /opt/bin/decrypt-assets
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      rkt run \
        --volume=ssl,kind=host,source=/etc/kubernetes/ssl,readOnly=false \
        --mount=volume=ssl,target=/etc/kubernetes/ssl \
        {{- if .Experimental.TLSBootstrap.Enabled }}
        --volume=kube,kind=host,source=/etc/kubernetes,readOnly=false \
        --mount=volume=kube,target=/etc/kubernetes \
        {{- end }}
        --uuid-file-save=/var/run/coreos/decrypt-assets.uuid \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf \
        --net=host \
        --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -ec \
          'echo decrypting assets
           shopt -s nullglob
           for encKey in /etc/kubernetes/{ssl,{{ if and .Experimental.TLSBootstrap.Enabled .AssetsConfig.HasTLSBootstrapToken }}auth{{end}}}/*.enc; do
             echo decrypting $encKey
             f=$(mktemp $encKey.XXXXXXXX)
             /usr/bin/aws \
               --region {{.Region}} kms decrypt \
               --ciphertext-blob fileb://$encKey \
               --output text \
               --query Plaintext \
             | base64 -d > $f
             mv -f $f ${encKey%.enc}
           done;

           {{ if and .Experimental.TLSBootstrap.Enabled .AssetsConfig.HasTLSBootstrapToken }}
           echo injecting token into the kubelet bootstrap kubeconfig file
           bootstrap_token=$(cat /etc/kubernetes/auth/kubelet-tls-bootstrap-token.tmp);
           sed -i -e "s#\$KUBELET_BOOTSTRAP_TOKEN#$bootstrap_token#g" /etc/kubernetes/kubeconfig/worker-bootstrap.yaml
           {{ end }}
           echo done.'

      rkt rm --uuid-file=/var/run/coreos/decrypt-assets.uuid || :

{{ end }}

{{if .SpotFleet.Enabled}}
  - path: /opt/bin/tag-spot-instance
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)

      TAGS=""
      TAGS="${TAGS}Key=\"kubernetes.io/cluster/{{ .ClusterName }}\",Value=\"owned\" "
      TAGS="${TAGS}Key=\"kube-aws:node-pool:name\",Value=\"{{.NodePoolName}}\" "
      TAGS="${TAGS}Key=\"Name\",Value=\"{{.ClusterName}}-{{.StackName}}-kube-aws-worker\" "

      {{if .Autoscaling.ClusterAutoscaler.Enabled -}}
      TAGS="${TAGS}Key=\"{{.Autoscaling.ClusterAutoscaler.AutoDiscoveryTagKey}}\",Value=\"\" "
      {{end -}}

      {{range $k, $v := .StackTags -}}
      TAGS="${TAGS}Key=\"{{$k}}\",Value=\"{{$v}}\" "
      {{end -}}

      {{range $k, $v := .InstanceTags -}}
      TAGS="${TAGS}Key=\"{{$k}}\",Value=\"{{$v}}\" "
      {{end -}}

      echo Tagging this EC2 instance with: "$TAGS"

      rkt run \
        --volume=ssl,kind=host,source=/etc/kubernetes/ssl,readOnly=false \
        --mount=volume=ssl,target=/etc/kubernetes/ssl \
        --uuid-file-save=/var/run/coreos/tag-spot-instance.uuid \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf \
        --net=host \
        --trust-keys-from-https \
        --insecure-options=ondisk \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -vxec \
          'echo tagging this spot instance
           instance_id="'$instance_id'"
           /usr/bin/aws \
             --region {{.Region}} ec2 create-tags \
             --resource $instance_id \
             --tags '"$TAGS"'
           echo done.'

      rkt rm --uuid-file=/var/run/coreos/tag-spot-instance.uuid || :

{{if .Experimental.LoadBalancer.Enabled}}
  - path: /opt/bin/add-to-load-balancers
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)

      rkt run \
        --volume=ssl,kind=host,source=/etc/kubernetes/ssl,readOnly=false \
        --mount=volume=ssl,target=/etc/kubernetes/ssl \
        --uuid-file-save=/var/run/coreos/add-to-load-balancers.uuid \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf \
        --net=host \
        --trust-keys-from-https \
        --insecure-options=ondisk \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -vxec \
          'echo adding this spot instance to load balancers
           instance_id="'$instance_id'"
           lbs=({{range $lb := .Experimental.LoadBalancer.Names}}"{{$lb}}" {{end}})
           add_to_lb="/usr/bin/aws --region {{.Region}} elb register-instances-with-load-balancer --instances $instance_id --load-balancer-name"
           for lb in ${lbs[@]}; do
             echo "$lb"
             $add_to_lb "$lb"
           done
           echo done.'

      rkt rm --uuid-file=/var/run/coreos/add-to-load-balancers.uuid || :
{{end}}

{{if .Experimental.TargetGroup.Enabled}}
  - path: /opt/bin/add-to-target-groups
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)

      rkt run \
        --volume=ssl,kind=host,source=/etc/kubernetes/ssl,readOnly=false \
        --mount=volume=ssl,target=/etc/kubernetes/ssl \
        --uuid-file-save=/var/run/coreos/add-to-target-groups.uuid \
        --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf \
        --net=host \
        --trust-keys-from-https \
        --insecure-options=ondisk \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -vxec \
          'echo adding this spot instance to target groups
           instance_id="'$instance_id'"
           tgs=({{range $tg := .Experimental.TargetGroup.Arns}}"{{$tg}}" {{end}})
           add_to_tg="/usr/bin/aws --region {{.Region}} elbv2 register-targets --targets Id=$instance_id --target-group-arn"
           for tg in ${tgs[@]}; do
             echo "$tg"
             $add_to_tg "$tg"
           done
           echo done.'

      rkt rm --uuid-file=/var/run/coreos/add-to-target-groups.uuid || :
{{end}}
{{end}}

  # File needed on every node (used by the kube-proxy DaemonSet), including controllers
  - path: /etc/kubernetes/kubeconfig/kube-proxy.yaml
    content: |
        apiVersion: v1
        kind: Config
        clusters:
        - name: default
          cluster:
            certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
            server: {{.APIEndpointURL}}:443
        users:
        - name: default
          user:
            tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
        contexts:
        - context:
            cluster: default
            user: default
          name: default
        current-context: default

{{ if and .Experimental.TLSBootstrap.Enabled .AssetsConfig.HasTLSBootstrapToken }}
  - path: /etc/kubernetes/kubeconfig/worker-bootstrap.yaml
    content: |
        apiVersion: v1
        kind: Config
        clusters:
        - name: local
          cluster:
            certificate-authority: /etc/kubernetes/ssl/ca.pem
            server: {{.APIEndpointURL}}:443
        users:
        - name: tls-bootstrap
          user:
            token: $KUBELET_BOOTSTRAP_TOKEN
        contexts:
        - context:
            cluster: local
            user: tls-bootstrap
          name: tls-bootstrap-context
        current-context: tls-bootstrap-context
{{ else }}
  - path: /etc/kubernetes/kubeconfig/worker.yaml
    content: |
        apiVersion: v1
        kind: Config
        clusters:
        - name: local
          cluster:
            certificate-authority: /etc/kubernetes/ssl/ca.pem
            server: {{.APIEndpointURL}}:443
        users:
        - name: kubelet
          user:
            client-certificate: /etc/kubernetes/ssl/worker.pem
            client-key: /etc/kubernetes/ssl/worker-key.pem
        contexts:
        - context:
            cluster: local
            user: kubelet
          name: kubelet-context
        current-context: kubelet-context
{{ end }}

{{ if not .UseCalico }}
  - path: /etc/kubernetes/cni/net.d/10-flannel.conf
    content: |
        {
            "name": "podnet",
            "type": "flannel",
            "delegate": {
                "isDefaultGateway": true
            }
        }

{{ else }}

  - path: /etc/kubernetes/cni/net.d/10-calico.conf
    content: |
      {
        "name": "calico",
        "type": "flannel",
        "delegate": {
          "type": "calico",
          "etcd_endpoints": "#ETCD_ENDPOINTS#",
          "etcd_key_file": "/etc/kubernetes/ssl/etcd-client-key.pem",
          "etcd_cert_file": "/etc/kubernetes/ssl/etcd-client.pem",
          "etcd_ca_cert_file": "/etc/kubernetes/ssl/etcd-trusted-ca.pem",
          "log_level": "info",
          "policy": {
            "type": "k8s",
            "k8s_api_root": "{{.APIEndpointURL}}/api/v1/",
            {{- if .Experimental.TLSBootstrap.Enabled }}
            "k8s_client_key": "/etc/kubernetes/ssl/kubelet-client.key",
            "k8s_client_certificate": "/etc/kubernetes/ssl/kubelet-client.crt",
            {{- else }}
            "k8s_client_key": "/etc/kubernetes/ssl/worker-key.pem",
            "k8s_client_certificate": "/etc/kubernetes/ssl/worker.pem",
            {{- end }}
            "k8s_certificate_authority": "/etc/kubernetes/ssl/ca.pem"
          }
        }
      }
{{ end }}

{{ if and .Experimental.TLSBootstrap.Enabled .AssetsConfig.HasTLSBootstrapToken }}
  - path: /etc/kubernetes/auth/kubelet-tls-bootstrap-token.tmp{{if .AssetsEncryptionEnabled}}.enc{{end}}
    encoding: gzip+base64
    content: {{.AssetsConfig.TLSBootstrapToken}}
{{ end }}

{{ if .Gpu.Nvidia.IsEnabledOn .InstanceType }}
  - path: /opt/nvidia-build/README
    owner: root:root
    permissions: 0644
    content: |
      Most of scripts in this directory are borrowed from https://github.com/Clarifai/coreos-nvidia/
      Especially from https://github.com/Clarifai/coreos-nvidia/pull/4

  - path: /opt/nvidia-build/LICENSE
    owner: root:root
    permissions: 0644
    content: |
      Please see https://github.com/Clarifai/coreos-nvidia/

  - path: /opt/nvidia-build/build-and-install.sh
    owner: root:root
    permissions: 0755
    content: |
      #! /bin/bash
      set -e
      function is_gpu_enabled(){
        local instance_type=$1
        [[ -n $instance_type ]] && ([[ $instance_type == p2* ]] || [[ $instance_type == p3* ]] || [[ $instance_type ==  g2* ]] || [[ $instance_type == g3* ]])
      }

      INSTANCE_TYPE=$(curl -s http://169.254.169.254/latest/meta-data/instance-type)

      if is_gpu_enabled $INSTANCE_TYPE; then
        MOD_INSTALLED=$(lsmod | grep nvidia | wc -l)
        if [[ $MOD_INSTALLED -ne 5 ]]; then
          (lsmod | grep nvidia_uvm) && rmmod -f nvidia_uvm
          (lsmod | grep nvidia_drm) && rmmod -f nvidia_drm
          (lsmod | grep nvidia_modeset) && rmmod -f nvidia_modeset
          (lsmod | grep nvidia) && rmmod -f nvidia

          cd /opt/nvidia-build/
          bash -x build.sh {{.Gpu.Nvidia.Version}}
          bash -x nvidia-install.sh {{.Gpu.Nvidia.Version}}
        else
          echo "Nvidia drivers seems to be installed already. Skipped."
        fi
      else
        echo "GPU is NOT supported in $INSTANCE_TYPE. Nvidia drivers won't build nor install."
      fi

  - path: /opt/nvidia-build/71-nvidia.rules
    owner: root:root
    permissions: 0644
    content: |
      # Tag the device as master-of-seat so that logind is happy
      # (see LP: #1365336)
      SUBSYSTEM=="pci", ATTRS{vendor}=="0x10de", DRIVERS=="nvidia", TAG+="seat", TAG+="master-of-seat"

      # Start and stop nvidia-persistenced on power on and power off
      # respectively
      ACTION=="add" DEVPATH=="/bus/acpi/drivers/NVIDIA ACPI Video Driver" SUBSYSTEM=="drivers" RUN+="/bin/systemctl start --no-block nvidia-persistenced.service"
      ACTION=="remove" DEVPATH=="/bus/acpi/drivers/NVIDIA ACPI Video Driver" SUBSYSTEM=="drivers" RUN+="/bin/systemctl stop --no-block nvidia-persistenced"

      # Start and stop nvidia-persistenced when loading and unloading
      # the driver
      ACTION=="add" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/bin/systemctl start --no-block nvidia-persistenced.service"
      ACTION=="remove" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/bin/systemctl stop --no-block nvidia-persistenced"

      # Load and unload nvidia-modeset module
      ACTION=="add" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/opt/nvidia/current/bin/nvidia-insmod.sh nvidia-modeset.ko"
      ACTION=="remove" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/usr/sbin/rmmod -r nvidia-modeset"

      # Load and unload nvidia-drm module
      ACTION=="add" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/opt/nvidia/current/bin/nvidia-insmod.sh nvidia-drm.ko"
      ACTION=="remove" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/usr/sbin/rmmod nvidia-drm"

      # Load and unload nvidia-uvm module
      ACTION=="add" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/opt/nvidia/current/bin/nvidia-insmod.sh nvidia-uvm.ko"
      ACTION=="remove" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/usr/sbin/rmmod -r nvidia-uvm"

      # This will create the device nvidia device nodes
      ACTION=="add" DEVPATH=="/module/nvidia" SUBSYSTEM=="module" RUN+="/opt/nvidia/current/bin/nvidia-smi"

      # Create the device node for the nvidia-uvm module
      ACTION=="add" DEVPATH=="/module/nvidia_uvm" SUBSYSTEM=="module" RUN+="/opt/nvidia/current/bin/create-uvm-dev-node.sh"

  - path: /opt/nvidia-build/_container_build.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/sh

      # Default: use binary packages instead of building everything from source
      EMERGE_SOURCE_FLAGS=gK
      while :; do
        case $1 in
          --emerge-sources)
            EMERGE_SOURCE_FLAGS=
            ;;
          *)
            break
        esac
        shift
      done


      VERSION=$1
      echo Building ${VERSION}

      function finish {
        cat /nvidia_installers/NVIDIA-Linux-x86_64-${VERSION}/nvidia-installer.log
      }

      set -e
      trap finish exit

      emerge-gitclone
      . /usr/share/coreos/release
      git -C /var/lib/portage/coreos-overlay checkout build-${COREOS_RELEASE_VERSION%%.*}
      emerge -${EMERGE_SOURCE_FLAGS}q --jobs 4 --load-average 4 coreos-sources

      cd /usr/src/linux
      cp /lib/modules/*-coreos*/build/.config .config

      make olddefconfig
      make modules_prepare

      cd /nvidia_installers/NVIDIA-Linux-x86_64-${VERSION}
      ./nvidia-installer -s -n --kernel-source-path=/usr/src/linux \
        --no-check-for-alternate-installs --no-opengl-files \
        --kernel-install-path=${PWD} --log-file-name=${PWD}/nvidia-installer.log

  - path: /opt/nvidia-build/_export.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/sh

      set -e

      ARTIFACT_DIR=$1
      VERSION=$2
      COMBINED_VERSION=$3

      TOOLS="nvidia-debugdump nvidia-cuda-mps-control nvidia-xconfig nvidia-modprobe nvidia-smi nvidia-cuda-mps-server
      nvidia-persistenced nvidia-settings"

      # Create archives with no paths
      tar -C ${ARTIFACT_DIR} -cvj $(basename -a ${ARTIFACT_DIR}/*.so.*) > libraries-${VERSION}.tar.bz2
      tar -C ${ARTIFACT_DIR} -cvj ${TOOLS} > tools-${VERSION}.tar.bz2
      tar -C ${ARTIFACT_DIR}/kernel -cvj $(basename -a ${ARTIFACT_DIR}/kernel/*.ko) > modules-${COMBINED_VERSION}.tar.bz2

  - path: /opt/nvidia-build/build.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/bash
      #
      # Build NVIDIA drivers for a given CoreOS version
      #

      KEEP_CONTAINER=false
      EMERGE_SOURCES=""
      while :; do
        case $1 in
          --keep)
            KEEP_CONTAINER=true
            ;;
          --emerge-sources)
            EMERGE_SOURCES=$1
            ;;
          -?*)
            echo Unknown flag $1
            exit 1
            ;;
          *)
            break
        esac
        shift
      done

      echo "Keeping container around after build: ${KEEP_CONTAINER}"
      echo "Additional flags: ${EMERGE_SOURCES}"

      # If we are on CoreOS by default build for the current CoreOS version
      if [[ -f /etc/lsb-release && -f /etc/coreos/update.conf ]]; then
          source /etc/lsb-release
          source /etc/coreos/update.conf

          COREOS_TRACK_DEFAULT=$GROUP
          COREOS_VERSION_DEFAULT=$DISTRIB_RELEASE
      fi

      DRIVER_VERSION=${1:-{{.Gpu.Nvidia.Version}}}
      COREOS_TRACK=${2:-$COREOS_TRACK_DEFAULT}
      COREOS_VERSION=${3:-$COREOS_VERSION_DEFAULT}

      DRIVER_ARCHIVE=NVIDIA-Linux-x86_64-${DRIVER_VERSION}
      DRIVER_ARCHIVE_PATH=${PWD}/nvidia_installers/${DRIVER_ARCHIVE}.run
      DEV_CONTAINER=coreos_developer_container.bin.${COREOS_VERSION}
      WORK_DIR=pkg/run_files/${COREOS_VERSION}
      ORIGINAL_DIR=${PWD}

      function onerr {
        echo Caught error
        finish
      }

      function onexit {
        finish
      }

      function finish {
        if [ "${KEEP_CONTAINER}" != "true" ]
        then
          cd ${ORIGINAL_DIR}
          echo Cleaning up
          sudo rm -Rf ${DEV_CONTAINER} ${WORK_DIR}/${DRIVER_ARCHIVE} tmp
        fi
        exit
      }

      set -e
      trap onerr ERR
      trap onexit exit

      if [ ! -f ${DEV_CONTAINER} ]
      then
        echo Downloading CoreOS ${COREOS_TRACK} developer image ${COREOS_VERSION}
        SITE=${COREOS_TRACK}.release.core-os.net/amd64-usr
        curl -s -L https://${SITE}/${COREOS_VERSION}/coreos_developer_container.bin.bz2 \
          -z ${DEV_CONTAINER}.bz2 \
          -o ${DEV_CONTAINER}.bz2
        echo Decompressing
        bunzip2 -k ${DEV_CONTAINER}.bz2
      fi

      if [ ! -f ${DRIVER_ARCHIVE_PATH} ]
      then
        echo Downloading NVIDIA Linux drivers version ${DRIVER_VERSION}
        mkdir -p nvidia_installers
        SITE=us.download.nvidia.com/XFree86/Linux-x86_64
        curl -s -L http://${SITE}/${DRIVER_VERSION}/${DRIVER_ARCHIVE}.run \
          -z ${DRIVER_ARCHIVE_PATH} \
          -o ${DRIVER_ARCHIVE_PATH}
      fi

      rm -Rf ${PWD}/tmp
      mkdir -p ${PWD}/tmp ${WORK_DIR}
      cp -ul ${DRIVER_ARCHIVE_PATH} ${WORK_DIR}

      cd ${WORK_DIR}
      chmod +x ${DRIVER_ARCHIVE}.run
      sudo rm -Rf ./${DRIVER_ARCHIVE}
      ./${DRIVER_ARCHIVE}.run -x -s
      cd ${ORIGINAL_DIR}

      systemd-nspawn -i ${DEV_CONTAINER} \
        --bind=${PWD}/_container_build.sh:/build.sh \
        --bind=${PWD}/${WORK_DIR}:/nvidia_installers \
        /bin/bash -x /build.sh ${EMERGE_SOURCES} ${DRIVER_VERSION} || echo "nspawn fails as expected.  Because kernel modules can't install in the container"

      sudo chown -R ${UID}:${GROUPS[0]} ${PWD}/${WORK_DIR}

      bash -x _export.sh ${WORK_DIR}/*-${DRIVER_VERSION} \
        ${DRIVER_VERSION} ${COREOS_VERSION}-${DRIVER_VERSION}

  - path: /opt/nvidia-build/create-uvm-dev-node.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/sh
      # This script is borrowed from https://github.com/Clarifai/coreos-nvidia/pull/4
      # Get the major device number for nvidia-uvm and create the node
      echo "Set up NVIDIA UVM"
      major=` + "`grep nvidia-uvm /proc/devices | awk '{print $1}'`" + `
      if [ -n "$major" ]; then
          mknod -m 666 /dev/nvidia-uvm c $major 0
      fi

  - path: /opt/nvidia-build/nvidia-insmod.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/sh
      # This script is borrowed from https://github.com/Clarifai/coreos-nvidia/pull/4
      /usr/sbin/insmod /opt/nvidia/current/lib/modules/$(uname -r)/video/$1

  - path: /opt/nvidia-build/nvidia-start.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/sh
      # This script is borrowed from https://github.com/Clarifai/coreos-nvidia/pull/4

      /opt/nvidia/current/bin/nvidia-insmod.sh nvidia.ko

      # Start the first devices
      /usr/bin/mknod -m 666 /dev/nvidiactl c 195 255 2>/dev/null
      NVDEVS=` + "`lspci | grep -i NVIDIA`" + `
      N3D=` + "`echo \"$NVDEVS\" | grep \"3D controller\" | wc -l`" + `
      NVGA=` + "`echo \"$NVDEVS\" | grep \"VGA compatible controller\" | wc -l`" + `
      N=` + "`expr $N3D + $NVGA - 1`" + `
      for i in ` + "`seq 0 $N`" + `; do
        mknod -m 666 /dev/nvidia$i c 195 $i
      done

      /opt/nvidia/current/bin/set-gpu-name-to-kubelet-opts.sh

  - path: /opt/nvidia-build/set-gpu-name-to-kubelet-opts.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/bash
      # Register GPU model name to node label
      # Currently, we assume all GPU devices in a node are homogeneous (the same model).
      [ -e /etc/default/kubelet ] || echo "KUBELET_OPTS=\"\"" > /etc/default/kubelet
      source /etc/default/kubelet
      if [ ! "$KUBELET_OPTS" == *nvidia-gpu-name* ]; then
        NVIDIA_GPU_NAME=$(/opt/nvidia/current/bin/nvidia-smi --query-gpu=gpu_name --format=csv,noheader --id=0 | sed -E 's/ +/_/g')
        KUBELET_OPTS="--node-labels='alpha.kubernetes.io/nvidia-gpu-name=$NVIDIA_GPU_NAME' $KUBELET_OPTS"
        KUBELET_OPTS="--node-labels='kube-aws.coreos.com/gpu=nvidia' $KUBELET_OPTS"
        KUBELET_OPTS="--node-labels='kube-aws.coreos.com/nvidia-gpu-version={{.Gpu.Nvidia.Version}}' $KUBELET_OPTS"
        echo "KUBELET_OPTS=\"$KUBELET_OPTS\"" > /etc/default/kubelet
      fi

  - path: /opt/nvidia-build/nvidia-install.sh
    owner: root:root
    permissions: 0755
    content: |
      #!/bin/bash
      # This script is borrowed from https://github.com/Clarifai/coreos-nvidia/pull/4

      if [[ $(uname -r) != *"-coreos"* ]]; then
          echo "OS is not CoreOS"
          exit 1
      fi

      # If we are on CoreOS by default use the current CoreOS version
      if [[ -f /etc/lsb-release && -f /etc/coreos/update.conf ]]; then
          source /etc/lsb-release
          source /etc/coreos/update.conf

          COREOS_TRACK_DEFAULT=$GROUP
          COREOS_VERSION_DEFAULT=$DISTRIB_RELEASE
          if [[ $DISTRIB_ID != *"CoreOS"* ]]; then
              echo "Distribution is not CoreOS"
              exit 1
          fi
      fi

      DRIVER_VERSION=${1:-{{.Gpu.Nvidia.Version}}}
      COREOS_TRACK=${2:-$COREOS_TRACK_DEFAULT}
      COREOS_VERSION=${3:-$COREOS_VERSION_DEFAULT}

      # this is where the modules go
      release=$(uname -r)

      mkdir -p /opt/nvidia/$DRIVER_VERSION/lib64 2>/dev/null
      mkdir -p /opt/nvidia/$DRIVER_VERSION/bin 2>/dev/null
      ln -sfT lib64 /opt/nvidia/$DRIVER_VERSION/lib 2>/dev/null
      mkdir -p /opt/nvidia/$DRIVER_VERSION/lib64/modules/$release/video/

      tar xvf libraries-$DRIVER_VERSION.tar.bz2 -C /opt/nvidia/$DRIVER_VERSION/lib64/
      tar xvf modules-$COREOS_VERSION-$DRIVER_VERSION.tar.bz2 -C /opt/nvidia/$DRIVER_VERSION/lib64/modules/$release/video/
      tar xvf tools-$DRIVER_VERSION.tar.bz2 -C /opt/nvidia/$DRIVER_VERSION/bin/

      install -m 755 create-uvm-dev-node.sh /opt/nvidia/$DRIVER_VERSION/bin/
      install -m 755 nvidia-start.sh /opt/nvidia/$DRIVER_VERSION/bin/
      install -m 755 nvidia-insmod.sh /opt/nvidia/$DRIVER_VERSION/bin/
      install -m 755 set-gpu-name-to-kubelet-opts.sh /opt/nvidia/$DRIVER_VERSION/bin/
      ln -sfT $DRIVER_VERSION /opt/nvidia/current 2>/dev/null

      cp -f 71-nvidia.rules /etc/udev/rules.d/
      udevadm control --reload-rules

      mkdir -p /etc/ld.so.conf.d/ 2>/dev/null
      echo "/opt/nvidia/current/lib64" > /etc/ld.so.conf.d/nvidia.conf
      rm /opt/nvidia/current/lib64/libEGL.so.1
      ln -s /opt/nvidia/current/lib64/libEGL.so.$DRIVER_VERSION /opt/nvidia/current/lib64/libEGL.so.1
      ldconfig

  - path: /opt/nvidia-build/util/retry.sh
    owner: root:root
    permissions: 0755
    content: |
      #! /bin/bash
      max_attempts="$1"; shift
      cmd="$@"
      attempt_num=1
      attempt_interval_sec=3

      until $cmd
      do
          if (( attempt_num == max_attempts ))
          then
              echo "Attempt $attempt_num failed and there are no more attempts left!"
              return 1
          else
              echo "Attempt $attempt_num failed! Trying again in $attempt_interval_sec seconds..."
              ((attempt_num++))
              sleep $attempt_interval_sec;
          fi
      done

{{ end }}
{{ end }}`)