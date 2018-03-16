package config

var CloudConfigEtcd = []byte(`{{ define "instance" -}}
{{- $S3URI := self.Parts.s3.Asset.S3URL -}}
#!/bin/bash -xe
echo '{{.EtcdIndexEnvVarName}}={{extra.etcdIndex}}' >> {{.EtcdNodeEnvFileName}}
 . /etc/environment
export COREOS_PRIVATE_IPV4 COREOS_PRIVATE_IPV6 COREOS_PUBLIC_IPV4 COREOS_PUBLIC_IPV6
REGION=$(curl -s http://169.254.169.254/latest/dynamic/instance-identity/document | jq -r '.region')
USERDATA_FILE=userdata-etcd

run() {
  bin="$1"; shift
  while ! /usr/bin/rkt run \
     --net=host \
     --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true --mount volume=dns,target=/etc/resolv.conf  \
     --volume=awsenv,kind=host,source=/var/run/coreos,readOnly=false --mount volume=awsenv,target=/var/run/coreos \
     --volume=etcdenv,kind=host,source={{.EtcdNodeEnvFileName}},readOnly=false --mount volume=etcdenv,target={{.EtcdNodeEnvFileName}}  \
     --trust-keys-from-https \
     {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=$bin -- "$@"; do
    sleep 1
  done
}
run bash -c "aws configure set s3.signature_version s3v4; aws s3 --region $REGION cp {{ $S3URI }} /var/run/coreos/$USERDATA_FILE"

INSTANCE_ID=$(curl -s http://169.254.169.254/latest/meta-data/instance-id)

run /bin/sh -c 'echo {{.StackNameEnvVarName}}=$(aws ec2 describe-tags --region "'$REGION'" --filters \
  "Name=resource-id,Values='$INSTANCE_ID'" \
  "Name=key,Values=aws:cloudformation:stack-name" \
  --output json \
| jq -r ".Tags[].Value") >> {{.EtcdNodeEnvFileName}}'


exec /usr/bin/coreos-cloudinit --from-file /var/run/coreos/$USERDATA_FILE
{{ end }}
{{ define "s3" -}}
#cloud-config
coreos:
  update:
    reboot-strategy: "off"
  units:
{{- range $u := .Etcd.CustomSystemdUnits}}
    - name: {{$u.Name}}
      {{- if $u.Command }}
      command: {{ $u.Command }}
      {{- end}}
      {{- if $u.Enable }}
      enable: {{ $u.Enable }}
      {{- end }}
      {{- if $u.Runtime }}
      runtime: {{ $u.Runtime }}
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
        {{- range $l := $u.ContentArray}}
        {{ $l }}
        {{- end }}
      {{- end}}
{{- end}}
    - name: cfn-etcd-environment.service
      enable: true
      command: start
      runtime: true
      content: |
        [Unit]
        Description=Configures EBS volume and R53 record set for this node and derives env vars for etcd bootstrap
        After=network-online.target
        Before=format-etcd2-volume.service

        [Service]
        EnvironmentFile={{.EtcdNodeEnvFileName}}
        Restart=on-failure
        RemainAfterExit=true
        ExecStartPre=/opt/bin/cfn-etcd-environment
        ExecStartPre=/usr/bin/mv -f /var/run/coreos/etcd-environment /etc/etcd-environment
        ExecStart=/bin/true
        TimeoutStartSec=120

        [Install]
        RequiredBy=format-etcd2-volume.service
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
    {{if .Etcd.DisasterRecovery.SupportsEtcdVersion .Etcd.Version -}}
    - name: etcdadm-reconfigure.service
      enable: true
      content: |
        [Unit]
        Description=etcdadm reconfigure runner
        BindsTo={{.Etcd.SystemdUnitName}}
        Before={{.Etcd.SystemdUnitName}}
        Wants=cfn-etcd-environment.service
        After=cfn-etcd-environment.service
        After=network.target

        [Service]
        Type=oneshot
        RemainAfterExit=yes
        RestartSec=5
        EnvironmentFile=-/etc/etcd-environment
        EnvironmentFile=-/var/run/coreos/etcdadm-environment
        ExecStartPre=/usr/bin/systemctl is-active cfn-etcd-environment.service
        ExecStartPre=/usr/bin/mkdir -p /var/run/coreos/etcdadm/snapshots
        ExecStart=/opt/bin/etcdadm reconfigure
        {{if .Etcd.DisasterRecovery.IsAutomatedForEtcdVersion .Etcd.Version -}}
        ExecStartPost=/usr/bin/systemctl start etcdadm-check.timer
        {{end -}}
        TimeoutStartSec=120

        [Install]
        WantedBy=cfn-etcd-environment.service

    - name: etcdadm-update-status.service
      enable: true
      content: |
        [Unit]
        Description=etcdadm update status
        BindsTo={{.Etcd.SystemdUnitName}}
        After={{.Etcd.SystemdUnitName}}
        After=network.target

        [Service]
        Type=oneshot
        RemainAfterExit=yes
        RestartSec=5
        EnvironmentFile=-/etc/etcd-environment
        EnvironmentFile=-/var/run/coreos/etcdadm-environment
        ExecStart=/opt/bin/etcdadm member_status_set_started
        {{if .Etcd.Snapshot.IsAutomatedForEtcdVersion .Etcd.Version -}}
        ExecStartPost=/usr/bin/systemctl start etcdadm-save.timer
        {{end -}}
        TimeoutStartSec=120
    {{- end}}

    - name: etcdadm-check.service
      enable: true
      content: |
        [Unit]
        Description=etcd health check

        [Service]
        Type=oneshot
        EnvironmentFile=-/etc/etcd-environment
        EnvironmentFile=-/var/run/coreos/etcdadm-environment
        ExecStartPre=/usr/bin/systemctl is-active {{.Etcd.SystemdUnitName}}
        ExecStart=/opt/bin/etcdadm check
        TimeoutStartSec=120

    {{if .Etcd.DisasterRecovery.IsAutomatedForEtcdVersion .Etcd.Version -}}
    - name: etcdadm-check.timer
      enable: true
      content: |
        [Unit]
        Description=periodic etcd health check

        [Timer]
        OnBootSec=120sec
        # Actual interval would be OnUnitInactiveSec+0~AccuracySec=10+0~5 sec
        OnUnitInactiveSec=10sec
        AccuracySec=5sec

        [Install]
        WantedBy=timers.target
    {{- end}}

    - name: etcdadm-save.service
      enable: true
      content: |
        [Unit]
        Description=etcd snapshot

        [Service]
        Type=oneshot
        EnvironmentFile=-/etc/etcd-environment
        EnvironmentFile=-/var/run/coreos/etcdadm-environment
        ExecStartPre=/usr/bin/systemctl is-active {{.Etcd.SystemdUnitName}}
        ExecStart=/opt/bin/etcdadm save
        TimeoutStartSec=300

    {{if .Etcd.Snapshot.IsAutomatedForEtcdVersion .Etcd.Version -}}
    - name: etcdadm-save.timer
      enable: true
      content: |
        [Unit]
        Description=periodic etcd snapshot

        [Timer]
        OnBootSec=120sec
        # Actual interval would be 60+0~5 sec
        OnUnitInactiveSec=60sec
        AccuracySec=5sec

        [Install]
        WantedBy=timers.target
    {{- end}}

    - name: {{.Etcd.SystemdUnitName}}
      drop-ins:
        - name: 20-aws-cluster.conf
          content: |
            [Unit]
            Wants=cfn-etcd-environment.service
            After=cfn-etcd-environment.service
            {{- if .AssetsEncryptionEnabled}}
            Wants=decrypt-assets.service
            After=decrypt-assets.service
            {{- end}}
            {{if .Etcd.DisasterRecovery.SupportsEtcdVersion .Etcd.Version -}}
            {{/* can be ` + "`Wants`" + ` if you like etcd-member to not stop when etcdadm-reconfigure failed */}}
            BindsTo=etcdadm-reconfigure.service etcdadm-update-status.service
            After=etcdadm-reconfigure.service
            Before=etcdadm-update-status.service
            {{end -}}

            [Service]
            EnvironmentFile=-/etc/etcd-environment
            EnvironmentFile=-/var/run/coreos/etcdadm/etcd-member.env

            PermissionsStartOnly=true
            ExecStartPre=/usr/bin/systemctl is-active cfn-etcd-environment.service
            {{- if .AssetsEncryptionEnabled}}
            ExecStartPre=/usr/bin/systemctl is-active decrypt-assets.service
            {{- end}}
            ExecStartPre=/usr/bin/chown -R etcd:etcd /var/lib/etcd2
        {{if .Etcd.Version.Is3 }}
        - name: 40-version.conf
          content: |
            [Service]
            Environment="ETCD_IMAGE_TAG=v{{.Etcd.Version}}"
        {{end}}
      enable: true
      command: start

    - name: var-lib-etcd2.mount
      enable: true
      content: |
        [Unit]
        Before={{.Etcd.SystemdUnitName}}

        [Mount]
        What=/dev/xvdf
        Where=/var/lib/etcd2
        Type=ext4

        [Install]
        RequiredBy={{.Etcd.SystemdUnitName}}

    - name: format-etcd2-volume.service
      enable: true
      content: |
        [Unit]
        Description=Formats etcd2 ebs volume
        After=dev-xvdf.device
        Requires=dev-xvdf.device
        Before=var-lib-etcd2.mount

        [Service]
        Type=oneshot
        RemainAfterExit=yes
        ExecStart=/opt/bin/ext4-format-volume-once /dev/xvdf

        [Install]
        RequiredBy=var-lib-etcd2.mount

{{if .AssetsEncryptionEnabled}}
    - name: decrypt-assets.service
      enable: true
      content: |
        [Unit]
        Description=decrypt etcd2 tls assets using amazon kms
        Before={{.Etcd.SystemdUnitName}}

        [Service]
        Restart=on-failure
        RemainAfterExit=yes
        ExecStartPre=/usr/bin/rkt run \
          --uuid-file-save=/var/run/coreos/decrypt-assets.uuid \
          --volume=ssl,kind=host,source=/etc/ssl/certs,readOnly=false \
          --mount=volume=ssl,target=/etc/ssl/certs \
          --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true \
          --mount volume=dns,target=/etc/resolv.conf \
          --net=host \
          --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
            -ec \
            'echo decrypting tls assets; \
             shopt -s nullglob; \
             for encKey in /etc/ssl/certs/*.pem.enc; do \
             echo decrypting $encKey; \
             /usr/bin/aws \
               --region {{.Region}} kms decrypt \
               --ciphertext-blob fileb://$encKey \
               --output text \
               --query Plaintext \
             | base64 -d > $${encKey%.enc}; \
             done; \
             echo done.'
        ExecStart=-/usr/bin/rkt rm --uuid-file=/var/run/coreos/decrypt-assets.uuid

        [Install]
        RequiredBy={{.Etcd.SystemdUnitName}}
{{ end }}

{{ if .WaitSignal.Enabled }}
    - name: cfn-signal.service
      command: start
      content: |
        [Unit]
        Wants={{.Etcd.SystemdUnitName}}
        After={{.Etcd.SystemdUnitName}}

        [Service]
        Type=simple
        Restart=on-failure
        RestartSec=10

        EnvironmentFile={{.EtcdNodeEnvFileName}}
        ExecStartPre=/usr/bin/systemctl is-active {{.Etcd.SystemdUnitName}}
        ExecStartPre=/usr/bin/rkt fetch {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}}
        ExecStart=-/opt/bin/cfn-signal
{{end}}

{{if .SSHAuthorizedKeys}}
ssh_authorized_keys:
  {{range $sshkey := .SSHAuthorizedKeys}}
  - {{$sshkey}}
  {{end}}
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
{{- if .Etcd.CustomFiles}}
  {{- range $w := .Etcd.CustomFiles}}
  - path: {{$w.Path}}
    permissions: {{$w.PermissionsString}}
    encoding: gzip+base64
    content: {{$w.GzippedBase64Content}}
  {{- end }}
{{- end }}
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
  - path: /opt/bin/cfn-init-etcd-server
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      cfn-init -v -c "etcd-server" --region {{.Region}} --resource {{.Etcd.LogicalName}}${{.EtcdIndexEnvVarName}} --stack ${{.StackNameEnvVarName}}

  - path: /opt/bin/attach-etcd-volume
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      # To omit the ` + "`--region {{.Region}}`" + ` flag for every aws-cli invocation
      export AWS_DEFAULT_REGION={{.Region}}

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)
      az=$(curl http://169.254.169.254/latest/meta-data/placement/availability-zone)

      # values shared between cloud-config-etcd and stack-template.json
      stack_name=${{.StackNameEnvVarName}}
      name_tag_key="{{$.Etcd.NameTagKey}}"
      advertised_hostname_tag_key="{{$.Etcd.AdvertisedFQDNTagKey}}"
      eip_allocation_id_tag_key="{{$.Etcd.EIPAllocationIDTagKey}}"
      network_interface_id_tag_key="{{$.Etcd.NetworkInterfaceIDTagKey}}"

      etcd_index=${{.EtcdIndexEnvVarName}}

      state_prefix=/var/run/coreos/etcd-volume
      output_prefix=/var/run/coreos/
      common_volume_filter="Name=tag:aws:cloudformation:stack-name,Values=$stack_name Name=tag:kube-aws:etcd:index,Values=$etcd_index"

      export $(cat /var/run/coreos/etcd-environment | grep -v ^# | xargs)

      export | grep ETCD

      # TODO: Locate the corresponding EBS volume via a tag on the ASG managing this EC2 instance
      # See https://github.com/coreos/kube-aws/pull/332#issuecomment-281531769

      # Skip the ` + "`while`" + ` block below when the EBS volume is already attached to this EC2 instance
      aws ec2 describe-volumes \
        --filters $common_volume_filter Name=attachment.instance-id,Values=$instance_id \
        | jq -r '([] + .Volumes)[0]' \
        > ${state_prefix}.json

      attached_vol_id=$(
        cat ${state_prefix}.json \
          | jq -r '"" + .VolumeId'
      )

      # Decide which volume to attach hence hostname to assume
      while [ "$attached_vol_id" = "" ]; do
        sleep 3

        aws ec2 describe-volumes \
          --filters $common_volume_filter Name=status,Values=available Name=availability-zone,Values=$az \
          > ${state_prefix}-candidates.json

        cat ${state_prefix}-candidates.json \
          | jq -r '([] + .Volumes)[0]' \
          > ${state_prefix}.json

        candidate_vol_id=$(
          cat ${state_prefix}.json \
            | jq -r '"" + .VolumeId'
        )

        if [ "$candidate_vol_id" = "" ]; then
          echo "[bug] no etcd volume found" 1>&2
          exit 1
        fi

        # See http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/device_naming.html for device naming
        if aws ec2 attach-volume --volume-id $candidate_vol_id --instance-id $instance_id --device "/dev/xvdf"; then
          attached_vol_id=$candidate_vol_id
        fi
      done

      # Wait until the volume attachment completes
      until [ "$volume_status" = ok ]; do
        sleep 3
        describe_volume_status_result=$(aws ec2 describe-volume-status --volume-id $attached_vol_id)
        volume_status=$(echo "$describe_volume_status_result" | jq -r "([] + .VolumeStatuses)[0].VolumeStatus.Status")
      done

      cat ${state_prefix}.json \
        | jq -r "([] + .Tags)[] | select(.Key == \"$name_tag_key\").Value" \
        > ${output_prefix}name

      cat ${state_prefix}.json \
        | jq -r "([] + .Tags)[] | select(.Key == \"$advertised_hostname_tag_key\").Value" \
        > ${output_prefix}advertised-hostname

      cat ${state_prefix}.json \
        | jq -r "([] + .Tags)[] | select(.Key == \"$eip_allocation_id_tag_key\").Value" \
        > ${output_prefix}eip-allocation-id

      cat ${state_prefix}.json \
        | jq -r "([] + .Tags)[] | select(.Key == \"$network_interface_id_tag_key\").Value" \
        > ${output_prefix}network-interface-id

  {{if $.Etcd.NodeShouldHaveSecondaryENI -}}
  - path: /opt/bin/assume-advertised-hostname-with-eni
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      # To omit the ` + "`--region {{.Region}}`" + ` flag for every aws-cli invocation
      export AWS_DEFAULT_REGION={{.Region}}

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)
      network_interface_id=$1

      # Persist outputs from awscli instead of just capturing them into shell variables and then echoing,
      # so that we can make debugging easier while making it won't break when
      # a possible huge output from awscli exceeds the bash limit of ARG_MAX
      state_prefix=/var/run/coreos/network-interface
      state_attached=${state_prefix}-attached.json
      state_attachment_id=${state_prefix}-attachment-id
      state_attachment=${state_prefix}-attachment.json
      state_attachment_status=${state_prefix}-status
      state_network_interface=${state_prefix}.json

      aws ec2 describe-network-interfaces \
        --network-interface-id $network_interface_id \
        | jq -r '.NetworkInterfaces[0]' \
        > $state_network_interface

      attached=$(
        cat $state_network_interface \
          | jq -r 'select(.Attachment.InstanceId) | "yes"' \
      )

      if [ "$attached" != yes ]; then
        aws ec2 attach-network-interface \
          --network-interface-id $network_interface_id \
          --instance-id $instance_id \
          --device-index {{$.Etcd.NetworkInterfaceDeviceIndex}} \
          > $state_attached
      fi

      until [ "$status" = attached ]; do
        sleep 3

        aws ec2 describe-network-interface-attribute \
          --network-interface-id $network_interface_id \
          --attribute attachment \
          > $state_attachment

        cat $state_attachment \
          | jq -r '.Attachment.Status' \
          > $state_attachment_status

        status=$(cat $state_attachment_status)
      done

      aws ec2 describe-network-interfaces \
        --network-interface-id $network_interface_id \
        > $state_network_interface

      cat $state_network_interface \
        | jq -r '.NetworkInterfaces[0].PrivateIpAddresses[] | select(.Primary == true).PrivateIpAddress' \
        > /var/run/coreos/listen-private-ip

  - path: /opt/bin/reconfigure-ip-routing
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      # Reconfigure ip routes and rules so that etcd can communicate via the newly attached ENI
      # Otherwise, an etcd process ends up producing ` + "`publish error: etcdserver: request timed out`" + ` errors repeatedly and
      # the etcd cluster never come up

      primary_ip=$(curl http://169.254.169.254/latest/meta-data/local-ipv4)
      secondary_ip=$(cat /var/run/coreos/listen-private-ip)

      # There's some possibility that the network interface kept configuring thus unable to be used at all.
      # Anyway, set the device down and then up to see if it alleviates the issue.
      # See https://gist.github.com/mumoshu/2e82cab514dd82e165df4ca223f554e2 for how it looked like when happened
      device=eth{{.Etcd.NetworkInterfaceDeviceIndex}}

      networkctl status $device
      ip link set $device down
      ip link set $device up

      configured=1
      while [ $configured -ne 0 ]; do
        sleep 3
        networkctl status $device
        networkctl status $device | grep State | grep routable
        configured=$?
      done

      # Dump various ip configs for debugging purpose
      ip rule show
      ip route show table main

      # TODO: Use subnet CIDR +1 instead?
      default_gw_for_subnet=$(ip route show | grep default | sed 's/default\svia \([0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\) .*/\1/' | head -n 1)

      ip route add default via $default_gw_for_subnet dev eth0 tab 1
      ip route add default via $default_gw_for_subnet dev $device tab 2

      ip rule add from $primary_ip/32 tab 1 priority 500
      ip rule add from $secondary_ip/32 tab 2 priority 600

      # Clear the rule from eth0 to subnets inside the VPC from the default table to so that packets to other etcd nodes goes through the newly attached ENI
      # Without losing internet connectivity provided via eth0(which has a public IP when this EC2 instance is in a public subnet)
      ip route show | grep eth0 | grep -v metric | while read -r route; do ip route del ${route}; done

      ip route show
  {{- end }}

  {{if $.Etcd.NodeShouldHaveEIP -}}
  - path: /opt/bin/assume-advertised-hostname-with-eip
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      # To omit the ` + "`--region {{.Region}}`" + ` flag for every aws-cli invocation
      export AWS_DEFAULT_REGION={{.Region}}

      instance_id=$(curl http://169.254.169.254/latest/meta-data/instance-id)
      eip_alloc_id=$1

      aws ec2 associate-address --instance-id $instance_id --allocation-id $eip_alloc_id

      curl http://169.254.169.254/latest/meta-data/public-hostname

      curl http://169.254.169.254/latest/meta-data/local-ipv4 > /var/run/coreos/listen-private-ip
  {{- end }}

  - path: /opt/bin/append-etcd-server-env
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -vxe

      private_ip=$(cat /var/run/coreos/listen-private-ip)
      name=$(cat /var/run/coreos/name)
      advertised_hostname=$(cat /var/run/coreos/advertised-hostname)

      echo "KUBE_AWS_ASSUMED_HOSTNAME=$advertised_hostname
      ETCD_NAME=$name
      ETCD_PEER_TRUSTED_CA_FILE=/etc/ssl/certs/etcd-trusted-ca.pem
      ETCD_PEER_CERT_FILE=/etc/ssl/certs/etcd.pem
      ETCD_PEER_KEY_FILE=/etc/ssl/certs/etcd-key.pem

      ETCD_CLIENT_CERT_AUTH=true
      ETCD_TRUSTED_CA_FILE=/etc/ssl/certs/etcd-trusted-ca.pem
      ETCD_CERT_FILE=/etc/ssl/certs/etcd.pem
      ETCD_KEY_FILE=/etc/ssl/certs/etcd-key.pem

      ETCD_INITIAL_CLUSTER_STATE=new
      ETCD_DATA_DIR=/var/lib/etcd2
      ETCD_LISTEN_CLIENT_URLS=https://$private_ip:2379
      ETCD_ADVERTISE_CLIENT_URLS=https://$advertised_hostname:2379
      ETCD_LISTEN_PEER_URLS=https://$private_ip:2380
      ETCD_INITIAL_ADVERTISE_PEER_URLS=https://$advertised_hostname:2380" >> /var/run/coreos/etcd-environment

  - path: /opt/bin/cfn-etcd-environment
    owner: root:root
    permissions: 0700
    content: |
      #!/bin/bash -e

      run() {
        rkt run \
           --volume=dns,kind=host,source=/etc/resolv.conf,readOnly=true \
           --mount volume=dns,target=/etc/resolv.conf \
           --volume=awsenv,kind=host,source=/var/run/coreos,readOnly=false \
           --mount volume=awsenv,target=/var/run/coreos \
           --volume=optbin,kind=host,source=/opt/bin,readOnly=false \
           --mount volume=optbin,target=/opt/bin \
           --uuid-file-save=/var/run/coreos/$1.uuid \
           --set-env={{.StackNameEnvVarName}}=${{.StackNameEnvVarName}} \
           --set-env={{.EtcdIndexEnvVarName}}=${{.EtcdIndexEnvVarName}} \
           --net=host \
           --trust-keys-from-https \
           {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/opt/bin/$1 -- $2

           rkt rm --uuid-file=/var/run/coreos/$1.uuid || :
        }

      run cfn-init-etcd-server
      run attach-etcd-volume

      eip_allocation_id=$(cat /var/run/coreos/eip-allocation-id)
      network_interface_id=$(cat /var/run/coreos/network-interface-id)
      if [ "$eip_allocation_id" != "" ]; then
        run assume-advertised-hostname-with-eip $eip_allocation_id
      elif [ "$network_interface_id" != "" ]; then
        run assume-advertised-hostname-with-eni $network_interface_id
        /opt/bin/reconfigure-ip-routing
      else
        echo '[bug] neither eip_allocation_id nor network_interface_id for this node found'
      fi

      run append-etcd-server-env

      /usr/bin/sed -i "s/^ETCDCTL_ENDPOINT.*$/ETCDCTL_ENDPOINT=https:\/\/$(cat /var/run/coreos/advertised-hostname):2379/" /etc/environment

  - path: /opt/bin/etcdadm
    permissions: 0755
    encoding: gzip+base64
    content: {{.Etcdadm}}

  - path: /etc/environment
    permissions: 0644
    content: |
      COREOS_PUBLIC_IPV4=$public_ipv4
      COREOS_PRIVATE_IPV4=$private_ipv4
      ETCDCTL_CA_FILE=/etc/ssl/certs/etcd-trusted-ca.pem
      ETCDCTL_CERT_FILE=/etc/ssl/certs/etcd-client.pem
      ETCDCTL_KEY_FILE=/etc/ssl/certs/etcd-client-key.pem
      ETCDCTL_ENDPOINT=

  - path: /opt/bin/ext4-format-volume-once
    permissions: 0700
    owner: root:root
    content: |
      #!/bin/bash -e
      if [[ "$(wipefs -n -p $1 | grep ext4)" == "" ]];then
        mkfs.ext4 $1
      else
        echo "volume $1 is already formatted"
      fi

{{ if .WaitSignal.Enabled }}
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
        --set-env={{.StackNameEnvVarName}}=${{.StackNameEnvVarName}} \
        --set-env={{.EtcdIndexEnvVarName}}=${{.EtcdIndexEnvVarName}} \
        --net=host \
        --trust-keys-from-https \
        {{.AWSCliImage.Options}}{{.AWSCliImage.RktRepo}} --exec=/bin/bash -- \
          -vxec \
          '
           cfn-signal -e 0 --region {{.Region}} --resource {{.Etcd.LogicalName}}${{.EtcdIndexEnvVarName}} --stack ${{.StackNameEnvVarName}}
          '

      rkt rm --uuid-file=/var/run/coreos/cfn-signal.uuid || :
{{end}}

{{ if .ManageCertificates }}

  - path: /etc/ssl/certs/etcd-trusted-ca.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdTrustedCA}}

  - path: /etc/ssl/certs/etcd-key.pem{{if .AssetsEncryptionEnabled}}.enc{{end}}
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdKey}}

  - path: /etc/ssl/certs/etcd.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdCert}}

  - path: /etc/ssl/certs/etcd-client.pem
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdClientCert}}

  - path: /etc/ssl/certs/etcd-client-key.pem{{if .AssetsEncryptionEnabled}}.enc{{end}}
    encoding: gzip+base64
    content: {{.AssetsConfig.EtcdClientKey}}

{{ end }}

{{ end }}`)
