package config

var StackTemplateTemplate = []byte(`{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Description": "kube-aws Kubernetes cluster {{.ClusterName}}",
  "Parameters": {
    {{if .CloudWatchLogging.Enabled}}
    "CloudWatchLogGroupARN": {
      "Type": "String",
      "Description": "CloudWatch LogGroup to send journald logs to"
    }
    {{end}}
  },
  "Resources": {
    "{{.Controller.LogicalName}}": {
      "Type": "AWS::AutoScaling::AutoScalingGroup",
      "Properties": {
        "HealthCheckGracePeriod": 600,
        "HealthCheckType": "EC2",
        "LaunchConfigurationName": {
          "Ref": "{{.Controller.LogicalName}}LC"
        },
        "MaxSize": "{{.MaxControllerCount}}",
        "MetricsCollection": [
          {
            "Granularity": "1Minute"
          }
        ],
        "MinSize": "{{.MinControllerCount}}",
        "Tags": [
          {{range $k, $v := $.Controller.InstanceTags -}}
          {
            "Key": "{{$k}}",
            "PropagateAtLaunch": "true",
            "Value": "{{$v}}"
          },
          {{end -}}
          {
            "Key": "kubernetes.io/cluster/{{.ClusterName}}",
            "PropagateAtLaunch": "true",
            "Value": "true"
          },
          {
            "Key": "Name",
            "PropagateAtLaunch": "true",
            "Value": "{{.ClusterName}}-{{.StackName}}-kube-aws-controller"
          },
          {
            "Key": "kubernetes.io/role/master",
            "PropagateAtLaunch": "true",
            "Value": ""
          }
        ],
        "VPCZoneIdentifier": [
          {{range $index, $subnet := .Controller.Subnets}}
          {{if gt $index 0}},{{end}}
          {{$subnet.Ref}}
          {{end}}
        ],
        "LoadBalancerNames" : [
          {{range $i, $ref := .APIEndpoints.ELBClassicRefs -}}
          {{- if gt $i 0}},{{end}}
          {{ $ref }}
          {{- end}}
        ],
        "TargetGroupARNs": [
          {{range $i, $ref := .APIEndpoints.ELBV2TargetGroupRefs -}}
          {{- if gt $i 0}},{{end}}
          {{ $ref }}
          {{- end}}
        ]
      },
      {{if .WaitSignal.Enabled}}
      "CreationPolicy" : {
        "ResourceSignal" : {
          "Count" : "{{.MinControllerCount}}",
          "Timeout" : "{{.Controller.CreateTimeout}}"
        }
      },
      {{end}}
      "UpdatePolicy" : {
        "AutoScalingRollingUpdate" : {
          "MinInstancesInService" : "{{.ControllerRollingUpdateMinInstancesInService}}",
          "MaxBatchSize" : "1",
          {{if .WaitSignal.Enabled}}
          "WaitOnResourceSignals" : "true",
          "PauseTime": "{{.Controller.CreateTimeout}}"
          {{else}}
          "PauseTime": "PT2M"
          {{end}}
        }
      },
      "Metadata" : {
        "AWS::CloudFormation::Init" : {
          "configSets" : {
              "etcd-client": [ "etcd-client-env" ]{{if .Experimental.AwsEnvironment.Enabled}},
              "aws-environment": [ "aws-environment-env" ]{{end}}
              {{ if .SharedPersistentVolume }},
                "load-efs-pv": [ "load-efs-pv-env" ]
              {{end}}
          },
          {{ if .Experimental.AwsEnvironment.Enabled }}
          "aws-environment-env" : {
            "commands": {
              "write-environment": {
                "command": { "Fn::Join" : ["", [ "echo '",
                  {{range $variable, $function := .Experimental.AwsEnvironment.Environment}}
                  "{{$variable}}=", {{$function}} , "\n",
                  {{end}}
                  "' > /etc/aws-environment" ] ] }
              }
            }
          },
          {{ end }}
          {{ if .SharedPersistentVolume }}
          "load-efs-pv-env" : {
            "files" : {
              "/etc/kubernetes/efs-pv.yaml": {
                "content": { "Fn::Join" : [ "", [
                  "apiVersion: v1\n",
                  "kind: PersistentVolume\n",
                  "metadata:\n",
                  "  name: shared-efs\n",
                  "spec:\n",
                  "  accessModes:\n",
                  "  - ReadWriteMany\n",
                  "  capacity:\n",
                  "    storage: 500Gi\n",
                  "  nfs:\n",
                  "    path: /\n",
                  "    server: ", {"Ref": "FileSystemCustom"}, ".efs.{{ $.Region }}.amazonaws.com", "\n",
                  "  persistentVolumeReclaimPolicy: Recycle\n"
                ]]}
              }
            }
          },
          {{ end }}
          "etcd-client-env": {
            "files" : {
              "/var/run/coreos/etcd-environment": {
                "content": { "Fn::Join" : [ "", [
                  "ETCD_ENDPOINTS='",
                  {{range $index, $etcdInstance := $.EtcdNodes}}
                  {{if $index}}",", {{end}} "https://",
                  {{$etcdInstance.AdvertisedFQDNRef}}, ":2379",
                  {{end}}
                  "'\n"
                ]]}
              }
            }
          }
        }
      },
      "DependsOn": ["{{$.Etcd.LogicalName}}{{sub $.Etcd.Count 1}}"]
    },
    {{ if not .Controller.IAMConfig.InstanceProfile.Arn }}
    "IAMInstanceProfileController": {
      "Properties": {
        "Path": "/",
        "Roles": [
          {
            "Ref": "IAMRoleController"
          }
        ]
      },
      "Type": "AWS::IAM::InstanceProfile"
    },
    "IAMManagedPolicyController" : {
      "Type" : "AWS::IAM::ManagedPolicy",
      "Properties" : {
        "Description" : "Policy for managing kube-aws k8s controllers",
        "Path" : "/",
        "PolicyDocument" :   {
          "Version":"2012-10-17",
          "Statement": [
                {{range $s := .Controller.IAMConfig.Policy.Statements }}
                {
                  "Action": {{toJSON $s.Actions}},
                  "Effect": {{toJSON $s.Effect}},
                  "Resource": {{toJSON $s.Resources}}
                },
                {{end}}
                {
                  "Action": "ec2:*",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {
                  "Action": "elasticloadbalancing:*",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {{if .CloudWatchLogging.Enabled}}
                {
                  "Effect": "Allow",
                  "Action": [
                    "logs:CreateLogStream",
                    "logs:PutLogEvents",
                    "logs:DescribeLogStreams"
                  ],
                  "Resource": [
                    { "Ref": "CloudWatchLogGroupARN" },
                    { "Fn::Join" : [ "", [{ "Ref": "CloudWatchLogGroupARN" }, ":log-stream:*"]] }
                  ]
                },{{ end }}
                {{ if .UserDataController.Parts.s3 }}
		            {
                  "Effect": "Allow",
                  "Action": [
                  "s3:GetObject"
                  ],
                  "Resource": "arn:{{.Region.Partition}}:s3:::{{ .UserDataController.Parts.s3.Asset.S3Prefix }}*"
		            },
                {{ end }}
                {{if .CloudWatchLogging.Enabled}}
                {
                  "Effect": "Allow",
                  "Action": [
                    "logs:CreateLogStream",
                    "logs:PutLogEvents",
                    "logs:DescribeLogStreams"
                  ],
                  "Resource": [
                    { "Ref": "CloudWatchLogGroupARN" },
                    { "Fn::Join" : [ "", [{ "Ref": "CloudWatchLogGroupARN" }, ":log-stream:*"]] }
                  ]
                },{{ end }}
                {{if .WaitSignal.Enabled}}
                {
                  "Action": "cloudformation:SignalResource",
                  "Effect": "Allow",
                  "Resource":
                    { "Fn::Join": [ "", [
                      "arn:{{.Region.Partition}}:cloudformation:",
                      { "Ref": "AWS::Region" },
                      ":",
                      { "Ref": "AWS::AccountId" },
                      ":stack/",
                      { "Ref": "AWS::StackName" },
                      "/*" ]
                    ] }
                },
                {{end}}
                {{if .Experimental.AwsNodeLabels.Enabled}}
                {
                  "Action": "autoscaling:Describe*",
                  "Effect": "Allow",
                  "Resource": [ "*" ]
                },
                {{end}}
                {{if (and .Addons.ClusterAutoscaler.Enabled .Experimental.ClusterAutoscalerSupport.Enabled) }}
                {
                  "Action": [
                    "autoscaling:DescribeAutoScalingGroups",
                    "autoscaling:DescribeAutoScalingInstances",
                    "autoscaling:DescribeTags",
                    "autoscaling:DescribeLaunchConfigurations"
                  ],
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {
                  "Action": [
                    "autoscaling:SetDesiredCapacity",
                    "autoscaling:TerminateInstanceInAutoScalingGroup"
                  ],
                  "Condition": {
                    "Null": { "autoscaling:ResourceTag/kubernetes.io/cluster/{{.ClusterName}}": "false" }
                  },
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {{end}}
                {{if or .Experimental.Kube2IamSupport.Enabled .Experimental.KIAMSupport.Enabled }}
                {
                  "Action": "sts:AssumeRole",
                  "Effect":"Allow",
                  "Resource":"*"
                },
                {{end}}
                {{if .AssetsEncryptionEnabled}}
                {
                  "Action" : "kms:Decrypt",
                  "Effect" : "Allow",
                  "Resource" : "{{.KMSKeyARN}}"
                },
                {{end}}
                {{if .Experimental.NodeDrainer.Enabled }}
                {
                  "Action": [
                    "autoscaling:DescribeAutoScalingInstances",
                    "autoscaling:DescribeLifecycleHooks",
                    "autoscaling:DescribeAutoScalingGroups"
                  ],
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {
                  "Action": [
                    "autoscaling:CompleteLifecycleAction"
                  ],
                  "Effect": "Allow",
                  "Condition": {
                    "Null": { "autoscaling:ResourceTag/kubernetes.io/cluster/{{.ClusterName}}": "false" }
                  },
                  "Resource": "*"
                },
                {{end}}
                {
                  "Action": [
                    "ecr:GetAuthorizationToken",
                    "ecr:BatchCheckLayerAvailability",
                    "ecr:GetDownloadUrlForLayer",
                    "ecr:GetRepositoryPolicy",
                    "ecr:DescribeRepositories",
                    "ecr:ListImages",
                    "ecr:BatchGetImage"
                  ],
                  "Resource": "*",
                  "Effect": "Allow"
                }
          ]
        }
      }
    },
    {{if and .Experimental.KIAMSupport.Enabled .KubeResourcesAutosave.Enabled }}
    "IAMManagedPolicyResourcesAutoSave" : {
      "Type" : "AWS::IAM::ManagedPolicy",
      "Properties" : {
        "Description" : "Policy for managing Resources Auto Save",
        "Path" : "/",
        "PolicyDocument" :   {
          "Version":"2012-10-17",
          "Statement": [
            {
              "Effect": "Allow",
              "Action": [
                "s3:PutObject"
              ],
              "Resource": "arn:{{.Region.Partition}}:s3:::{{ .KubeResourcesAutosave.S3Path }}/*"
            }
          ]
        }
      }
    },
    "IAMRoleResourcesAutoSave": {
      "Properties": {
        "AssumeRolePolicyDocument": {
          "Statement": [
            {
              "Action": [
                "sts:AssumeRole"
              ],
              "Effect": "Allow",
              "Principal": {
                "Service": [
                  "ec2.{{.Region.PublicDomainName}}"
                ]
              }
            },
            {
              "Action": [
                "sts:AssumeRole"
              ],
              "Effect": "Allow",
              "Principal": {
                "AWS": [
                  {"Fn::GetAtt": ["IAMRoleController", "Arn"]}
                ]
              }
            }
          ],
          "Version": "2012-10-17"
        },
        "Path": "/",
        "RoleName":  "{{$.ClusterName}}-IAMRoleResourcesAutoSave",
        "ManagedPolicyArns": [
          {"Ref": "IAMManagedPolicyResourcesAutoSave"}
        ]
      },
      "Type": "AWS::IAM::Role"
    },
    {{end}}
    "IAMRoleController": {
      "Properties": {
        "AssumeRolePolicyDocument": {
          "Statement": [
            {
              "Action": [
                "sts:AssumeRole"
              ],
              "Effect": "Allow",
              "Principal": {
                "Service": [
                  "ec2.{{.Region.PublicDomainName}}"
                ]
              }
            }
          ],
          "Version": "2012-10-17"
        },
        "Path": "/",
        {{if .Controller.IAMConfig.Role.Name }}
        "RoleName":  {"Fn::Join": ["",[{"Ref": "AWS::Region"},"-","{{.Controller.IAMConfig.Role.Name}}"]]},
        {{end}}
        "ManagedPolicyArns": [
          {{range $policyIndex, $policyArn := .Controller.IAMConfig.Role.ManagedPolicies }}
            "{{$policyArn.Arn}}",
          {{end}}
          {"Ref": "IAMManagedPolicyController"}
        ]
      },
      "Type": "AWS::IAM::Role"
    },
    {{end}}
    {{ if not .Etcd.IAMConfig.InstanceProfile.Arn }}
    "IAMInstanceProfileEtcd": {
      "Properties": {
        "Path": "/",
        "Roles": [
          {
            "Ref": "IAMRoleEtcd"
          }
        ]
      },
      "Type": "AWS::IAM::InstanceProfile"
    },
    "IAMManagedPolicyEtcd" : {
      "Type" : "AWS::IAM::ManagedPolicy",
      "Properties" : {
        "Description" : "Policy for managing kube-aws k8s etcd nodes",
        "Path" : "/",
        "PolicyDocument" :   {
          "Version":"2012-10-17",
          "Statement": [
            {{range $s := .Etcd.IAMConfig.Policy.Statements }}
            {
              "Action": {{toJSON $s.Actions}},
              "Effect": {{toJSON $s.Effect}},
              "Resource": {{toJSON $s.Resources}}
            },
            {{end}}
            {{if .AssetsEncryptionEnabled}}
            {
              "Action" : "kms:Decrypt",
              "Effect" : "Allow",
              "Resource" : "{{.KMSKeyARN}}"
            },
            {{end}}
            {{if $.Etcd.KMSKeyARN -}}
            {{/* Required for mounting encrypted data volume */}}
            {
            "Action": [
              "kms:CreateGrant",
              "kms:Decrypt",
              "kms:Describe*",
              "kms:Encrypt",
              "kms:GenerateDataKey*",
              "kms:ReEncrypt*"
            ],
            "Effect": "Allow",
            "Resource": "{{ $.Etcd.KMSKeyARN }}"
            },
            {{end -}}
            {
              "Action": "ec2:DescribeTags",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{/* Required for cfn-etcd-environment.service to discover the volume */}}
            {
              "Action": "ec2:DescribeVolumes",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{/* Required for cfn-etcd-environment.service to start attaching the volume */}}
            {
              "Action": "ec2:AttachVolume",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{/* Required for cfn-etcd-environment.service to wait until the volume is attached */}}
            {
              "Action": "ec2:DescribeVolumeStatus",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{if $.Etcd.NodeShouldHaveEIP -}}
            {{/* Required for cfn-etcd-environment.service to associate an EIP */}}
            {
              "Action": "ec2:AssociateAddress",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{end -}}
            {{if $.Etcd.NodeShouldHaveSecondaryENI -}}
            {{/* Required for cfn-etcd-environment.service to associate a network interface */}}
            {
              "Action": "ec2:AttachNetworkInterface",
              "Effect": "Allow",
              "Resource": "*"
            },
            {
              "Action": "ec2:DescribeNetworkInterfaces",
              "Effect": "Allow",
              "Resource": "*"
            },
            {
              "Action": "ec2:DescribeNetworkInterfaceAttribute",
              "Effect": "Allow",
              "Resource": "*"
            },
            {{end -}}
            {{- if $.UserDataEtcd.Parts.s3 }}
            {
              "Effect": "Allow",
              "Action": [
                "s3:GetObject"
              ],
              "Resource": "arn:{{.Region.Partition}}:s3:::{{ $.UserDataEtcd.Parts.s3.Asset.S3Prefix }}*"
            },
            {{- end }}
            {{/* Required for ` + "`etcdadm reconfigure`" + ` to check existence of an etcd snapshot in S3 */}}
            {
              "Effect": "Allow",
              "Action": [
                "s3:ListBucket"
              ],
              "Resource": "arn:{{.Region.Partition}}:s3:::{{$.EtcdSnapshotsS3Bucket}}"
            },
            {{if .CloudWatchLogging.Enabled}}
            {
              "Effect": "Allow",
              "Action": [
                "logs:CreateLogStream",
                "logs:PutLogEvents",
                "logs:DescribeLogStreams"
              ],
              "Resource": [
                { "Ref": "CloudWatchLogGroupARN" },
                { "Fn::Join" : [ "", [{ "Ref": "CloudWatchLogGroupARN" }, ":log-stream:*"]] }
              ]
            },{{ end }}
            {
              "Effect": "Allow",
              "Action": [
                "s3:List*",
                "s3:GetObject*"
              ],
              "Resource": "arn:{{.Region.Partition}}:s3:::{{$.EtcdSnapshotsS3Bucket}}",
              "Condition": {
                "StringLike": {
                  "s3:prefix": { "Fn::Join" : [ "", [{{$.EtcdSnapshotsS3PrefixRef}}, "/*" ]]}
                }
              }
            },
            {{/* Required for ` + "`etcdadm save`" + ` to save etcd snapshots in S3 */}}
            {
              "Effect": "Allow",
              "Action": [
                "s3:*"
              ],
              "Resource": { "Fn::Join" : [ "", ["arn:{{.Region.Partition}}:s3:::", {{$.EtcdSnapshotsS3PathRef}}, "/*" ]]}
            },
            {{/* Required for ` + "`etcdadm reconfigure`" + ` to determine the number of active etcd nodes */}}
            {
              "Action": "ec2:DescribeInstances",
              "Resource": "*",
              "Effect": "Allow"
            }
          ]
        }
      }
    },
    "IAMRoleEtcd": {
      "Properties": {
        "AssumeRolePolicyDocument": {
          "Statement": [
            {
              "Action": [
                "sts:AssumeRole"
              ],
              "Effect": "Allow",
              "Principal": {
                "Service": [
                  "ec2.{{.Region.PublicDomainName}}"
                ]
              }
            }
          ],
          "Version": "2012-10-17"
        },
        "Path": "/",
        "ManagedPolicyArns": [
          {{range $policyIndex, $policyArn := .Etcd.IAMConfig.Role.ManagedPolicies }}
            "{{$policyArn.Arn}}",
          {{end}}
          {"Ref": "IAMManagedPolicyEtcd"}
        ]
      },
      "Type": "AWS::IAM::Role"
    },
    {{end}}
    {{if $.Etcd.HostedZoneManaged}}
    "{{$.Etcd.HostedZoneLogicalName}}": {
      "Type": "AWS::Route53::HostedZone",
      "Properties": {
        "HostedZoneConfig": {
          "Comment": "My hosted zone for {{$.Etcd.InternalDomainName}}"
        },
        "Name": "{{$.Etcd.InternalDomainName}}",
        "VPCs": [{
          "VPCId": {{$.VPCRef}},
          "VPCRegion": { "Ref": "AWS::Region" }
        }],
        "HostedZoneTags" : [{
          "Key": "kubernetes.io/cluster/{{$.ClusterName}}",
          "Value": "owned"
        }]
      }
    },
    {{end}}
    {{range $etcdIndex, $etcdInstance := .EtcdNodes}}
    {{if $etcdInstance.RecordSetManaged}}
    "{{$etcdInstance.RecordSetLogicalName}}" : {
      "Type" : "AWS::Route53::RecordSet",
        "Properties" : {
        "HostedZoneId": {{$.Etcd.HostedZoneRef}},
        "Name": {{$etcdInstance.AdvertisedFQDNRef}},
        "Comment" : "A record for the private IP address of Etcd node named {{$etcdInstance.Name}} at index {{$etcdIndex}}",
        "Type" : "A",
        "TTL" : "300",
        "ResourceRecords" : [
          {{$etcdInstance.NetworkInterfacePrivateIPRef}}
        ]
      }
    },
    {{end}}
    {{if $etcdInstance.NetworkInterfaceManaged}}
    "{{$etcdInstance.NetworkInterfaceLogicalName}}": {
      "Properties": {
        "SubnetId": {{$etcdInstance.SubnetRef}},
        "GroupSet": [
          {{range $sgIndex, $sgRef := $.Etcd.SecurityGroupRefs}}
          {{if gt $sgIndex 0}},{{end}}
          {{$sgRef}}
          {{end}}
        ]
      },
      "Type": "AWS::EC2::NetworkInterface"
    },
    {{end}}
    {{if $etcdInstance.EIPManaged}}
    "{{$etcdInstance.EIPLogicalName}}": {
      "Properties": {
        "Domain": "vpc"
      },
      "Type": "AWS::EC2::EIP"
    },
    {{end}}
    {{if not $.Etcd.DataVolume.Ephemeral}}
    "{{$etcdInstance.EBSLogicalName}}": {
      "Properties": {
          "AvailabilityZone": "{{$etcdInstance.SubnetAvailabilityZone}}",
          "Size": "{{$.Etcd.DataVolume.Size}}",
          {{if gt $.Etcd.DataVolume.IOPS 0}}
          "Iops": "{{$.Etcd.DataVolume.IOPS}}",
          {{end}}
          {{if $.Etcd.DataVolume.Encrypted}}
          "Encrypted": {{$.Etcd.DataVolume.Encrypted}},
          {{if $.Etcd.KMSKeyARN}}
          "KmsKeyId": "{{$.Etcd.KMSKeyARN}}",
          {{end}}
          {{end}}
          "VolumeType": "{{$.Etcd.DataVolume.Type}}",
          "Tags": [
            {
              "Key": "kube-aws:etcd:index",
              "Value": "{{$etcdIndex}}"
            },
            {{if $etcdInstance.EIPManaged}}{
              "Key": "{{$.Etcd.EIPAllocationIDTagKey}}",
              "Value": {{$etcdInstance.EIPAllocationIDRef}}
            },{{end}}
            {{if $etcdInstance.NetworkInterfaceManaged}}{
              "Key": "{{$.Etcd.NetworkInterfaceIDTagKey}}",
              "Value": {{$etcdInstance.NetworkInterfaceIDRef}}
            },{{end}}
            {
              "Key": "{{$.Etcd.AdvertisedFQDNTagKey}}",
              "Value": {{$etcdInstance.AdvertisedFQDNRef}}
            },
            {
              "Key": "{{$.Etcd.NameTagKey}}",
              "Value": "{{$etcdInstance.Name}}"
            }
          ]
      },
      "Type": "AWS::EC2::Volume"
    },
    {{end}}
    "{{$etcdInstance.LogicalName}}": {
      "Type": "AWS::AutoScaling::AutoScalingGroup",
      "Properties": {
        "HealthCheckGracePeriod": 600,
        "HealthCheckType": "EC2",
        "LaunchConfigurationName": {
          "Ref": "{{$etcdInstance.LaunchConfigurationLogicalName}}"
        },
        "MaxSize": "1",
        "MetricsCollection": [
          {
            "Granularity": "1Minute"
          }
        ],
        "MinSize": "1",
        "Tags": [
          {{range $k, $v := $.Etcd.InstanceTags -}}
          {
            "Key": "{{$k}}",
            "PropagateAtLaunch": "true",
            "Value": "{{$v}}"
          },
          {{end -}}
          {
            "Key": "kubernetes.io/cluster/{{$.ClusterName}}",
            "PropagateAtLaunch": "true",
            "Value": "owned"
          },
          {
            "Key": "Name",
            "PropagateAtLaunch": "true",
            "Value": "{{$.ClusterName}}-{{$.StackName}}-kube-aws-etcd-{{$etcdIndex}}"
          },
          {
            "Key": "kube-aws:role",
            "PropagateAtLaunch": "true",
            "Value": "etcd"
          }
        ],
        "VPCZoneIdentifier": [
          {{$etcdInstance.SubnetRef}}
        ]
      },
      {{if $.WaitSignal.Enabled}}
      "CreationPolicy" : {
        "ResourceSignal" : {
          "Count" : "1",
          "Timeout" : "{{$.Controller.CreateTimeout}}"
        }
      },
      {{end}}
      "UpdatePolicy" : {
        "AutoScalingRollingUpdate" : {
          "MinInstancesInService" : "0",
          "MaxBatchSize" : "1",
          {{if $.WaitSignal.Enabled}}
          "WaitOnResourceSignals" : "true",
          "PauseTime": "{{$.Controller.CreateTimeout}}"
          {{else}}
          "PauseTime": "PT2M"
          {{end}}
        }
      },
      "Metadata" : {
        "AWS::CloudFormation::Init" : {
          "configSets" : {
              "etcd-server": [ "etcd-server-env" ]
          },
          "etcd-server-env": {
            "files" : {
              "/var/run/coreos/etcd-environment": {
                "content": { "Fn::Join" : [ "", [
                  "ETCD_INITIAL_CLUSTER='",
                    {{range $etcdIndex, $etcdInstance := $.EtcdNodes}}
                    {{if $etcdIndex}}",", {{end}}
                    "{{$etcdInstance.Name}}",
                    "=https://",
                    {{$etcdInstance.AdvertisedFQDNRef}},
                    ":2380",
                    {{end}}
                  "'\n"
                ]]}
              },
              "/var/run/coreos/etcdadm-environment": {
                "content": { "Fn::Join" : [ "", [
                  "ETCD_ENDPOINTS='",
                    {{range $index, $etcdInstance := $.EtcdNodes}}
                    {{if $index}}",", {{end}}
                    "https://",
                    {{$etcdInstance.AdvertisedFQDNRef}},
                    ":2379",
                    {{end}}
                  "'\n",
                  "AWS_DEFAULT_REGION='",
                    "{{$.Region}}",
                  "'\n",
                  "KUBERNETES_CLUSTER='",
                    "{{$.ClusterName}}",
                  "'\n",
                  "ETCDCTL_CACERT='",
                    "/etc/ssl/certs/etcd-trusted-ca.pem",
                  "'\n",
                  "ETCDCTL_CERT='",
                    "/etc/ssl/certs/etcd-client.pem",
                  "'\n",
                  "ETCDCTL_KEY='",
                    "/etc/ssl/certs/etcd-client-key.pem",
                  "'\n",
                  "ETCDCTL_CA_FILE='",
                    "/etc/ssl/certs/etcd-trusted-ca.pem",
                  "'\n",
                  "ETCDCTL_CERT_FILE='",
                    "/etc/ssl/certs/etcd-client.pem",
                  "'\n",
                  "ETCDCTL_KEY_FILE='",
                    "/etc/ssl/certs/etcd-client-key.pem",
                  "'\n",
                  "ETCDADM_MEMBER_SYSTEMD_SERVICE_NAME='",
                    "etcd-member",
                  "'\n",
                  "ETCDADM_CLUSTER_SNAPSHOTS_S3_URI='",
                    { "Fn::Join" : [ "", ["s3://", {{$.EtcdSnapshotsS3PathRef}} ]] },
                  "'\n",
                  "ETCDADM_STATE_FILES_DIR='",
                    "/var/run/coreos/etcdadm",
                  "'\n",
                  "ETCDADM_MEMBER_ENV_FILE='",
                    "/var/run/coreos/etcdadm/etcd-member.env",
                  "'\n",
                  "ETCDADM_MEMBER_COUNT='",
                    "{{$.Etcd.Count}}",
                  "'\n",
                  "ETCDADM_MEMBER_INDEX='",
                    "{{$etcdIndex}}",
                  "'\n",
                  "ETCD_VERSION='",
                    "{{$.Etcd.Version}}",
                  "'\n"
                ]]}
              }
            }
          }
        }
      },
      "DependsOn": [
        {{if $etcdInstance.DependencyExists}}{{$etcdInstance.DependencyRef}},{{end}}
        {{if $etcdIndex}}"{{$.Etcd.LogicalName}}{{sub $etcdIndex 1}}",{{end}}
        {{if $etcdInstance.EIPManaged}}
        "{{$etcdInstance.EIPLogicalName}}",
        {{end}}
        {{if $etcdInstance.NetworkInterfaceManaged}}
        "{{$etcdInstance.NetworkInterfaceLogicalName}}",
        {{end}}
        {{if $etcdInstance.RecordSetManaged}}
        "{{$etcdInstance.RecordSetLogicalName}}",
        {{end}}
        "{{$etcdInstance.EBSLogicalName}}"
      ]
    },
    "{{$etcdInstance.LaunchConfigurationLogicalName}}": {
      "Properties": {
        "BlockDeviceMappings": [
          {
            "DeviceName": "/dev/xvda",
            "Ebs": {
              "VolumeSize": "{{$.Etcd.RootVolume.Size}}",
              {{if gt $.Etcd.RootVolume.IOPS 0}}
              "Iops": "{{$.Etcd.RootVolume.IOPS}}",
              {{end}}
              "VolumeType": "{{$.Etcd.RootVolume.Type}}"
            }
          }
          {{if $.Etcd.DataVolume.Ephemeral}}
          ,
          {
            "DeviceName": "/dev/xvdf",
            "VirtualName" : "ephemeral0"
          }
          {{end}}
        ],
        {{if $.Etcd.IAMConfig.InstanceProfile.Arn }}
        "IamInstanceProfile": "{{$.Etcd.IAMConfig.InstanceProfile.Arn}}",
        {{else}}
        "IamInstanceProfile": {
          "Ref": "IAMInstanceProfileEtcd"
        },
        {{end}}
        "ImageId": "{{$.AMI}}",
        "InstanceType": "{{$.Etcd.InstanceType}}",
        {{if $.KeyName}}"KeyName": "{{$.KeyName}}",{{end}}
        "SecurityGroups": [
          {{range $sgIndex, $sgRef := $.Etcd.SecurityGroupRefs}}
          {{if gt $sgIndex 0}},{{end}}
          {{$sgRef}}
          {{end}}
        ],
        "PlacementTenancy": "{{$.Etcd.Tenancy}}",
        "UserData": {{ $.UserDataEtcd.Parts.instance.Base64 true (dict "etcdIndex" $etcdIndex) | checkSizeLessThan 16384 | quote }}
      },
      "Type": "AWS::AutoScaling::LaunchConfiguration"
    },
    {{end}}
    {{if .Experimental.NodeDrainer.Enabled }}
    "{{.Controller.LogicalName}}NodeDrainerLH" : {
      "Properties" : {
        "AutoScalingGroupName" : {
          "Ref": "{{.Controller.LogicalName}}"
        },
        "DefaultResult" : "CONTINUE",
        "HeartbeatTimeout" : "{{.Experimental.NodeDrainer.DrainTimeoutInSeconds}}",
        "LifecycleTransition" : "autoscaling:EC2_INSTANCE_TERMINATING"
      },
      "Type" : "AWS::AutoScaling::LifecycleHook"
    },
    {{end}}
    "{{.Controller.LogicalName}}LC": {
      "Properties": {
        "BlockDeviceMappings": [
          {
            "DeviceName": "/dev/xvda",
            "Ebs": {
              "VolumeSize": "{{.Controller.RootVolume.Size}}",
              {{if gt .Controller.RootVolume.IOPS 0}}
              "Iops": "{{.Controller.RootVolume.IOPS}}",
              {{end}}
              "VolumeType": "{{.Controller.RootVolume.Type}}"
            }
          }
        ],
        {{if .Controller.IAMConfig.InstanceProfile.Arn }}
        "IamInstanceProfile": "{{.Controller.IAMConfig.InstanceProfile.Arn}}",
        {{else}}
        "IamInstanceProfile": {
          "Ref": "IAMInstanceProfileController"
        },
        {{end}}
        "ImageId": "{{.AMI}}",
        "InstanceType": "{{.Controller.InstanceType}}",
        {{if .KeyName}}"KeyName": "{{.KeyName}}",{{end}}
        "SecurityGroups": [
          {{range $sgIndex, $sgRef := $.Controller.SecurityGroupRefs}}
          {{if gt $sgIndex 0}},{{end}}
          {{$sgRef}}
          {{end}}
        ],
        "PlacementTenancy": "{{ .Controller.Tenancy }}",
        "UserData": {{ $.UserDataController.Parts.instance.Base64 true | checkSizeLessThan 16384 | quote }}
      },
  {{ if .Experimental.AwsEnvironment.Enabled }}
      "Metadata" : {
        "AWS::CloudFormation::Init" : {
          "config" : {
            "commands": {
              "write-environment": {
                "command": { "Fn::Join" : ["", [ "echo '",
{{range $variable, $function := .Experimental.AwsEnvironment.Environment}}
"{{$variable}}=", {{$function}} , "\n",
{{end}}
"' > /etc/aws-environment" ] ] }
              }
            }
          }
        }
      },
  {{end}}
      "Type": "AWS::AutoScaling::LaunchConfiguration"
    },
    {{range $i, $apiEndpoint := $.APIEndpoints -}}
    {{if .LoadBalancer.ManageELB -}}
    {{if .LoadBalancer.ManageELBRecordSet -}}
    "{{.LoadBalancer.RecordSetLogicalName}}": {
      "Type": "AWS::Route53::RecordSet",
      "Properties": {
        "HostedZoneId": "{{.LoadBalancer.HostedZoneRef}}",
        "Name": "{{$apiEndpoint.DNSName}}",
        "TTL": {{.LoadBalancer.RecordSetTTL}},
        "ResourceRecords": [{{.LoadBalancer.DNSNameRef}}],
        "Type": "CNAME"
      }
    },
    {{ end -}}
    {{ if .LoadBalancer.NetworkLoadBalancer }}
    "{{.LoadBalancer.LogicalName}}TargetGroup": {
      "Type": "AWS::ElasticLoadBalancingV2::TargetGroup",
      "Properties": {
        "HealthCheckIntervalSeconds": "10",
        "HealthyThresholdCount": "3",
        "UnhealthyThresholdCount": "3",
        "Port": "443",
        "VpcId": {{$.VPCRef}},
        "Protocol": "TCP"
      }
    },
    "{{.LoadBalancer.LogicalName}}Listener": {
      "Type": "AWS::ElasticLoadBalancingV2::Listener",
      "Properties": {
        "DefaultActions": [
          {
            "TargetGroupArn": {"Ref": "{{.LoadBalancer.LogicalName}}TargetGroup"},
            "Type": "forward"
          }
        ],
        "LoadBalancerArn": {"Ref": "{{.LoadBalancer.LogicalName}}"},
        "Port": "443",
        "Protocol": "TCP"
      }
    },
    "{{.LoadBalancer.LogicalName}}" : {
      "Type" : "AWS::ElasticLoadBalancingV2::LoadBalancer",
      "Properties" : {
        "Type": "network",
        "Subnets" : [
          {{range $index, $subnet := .LoadBalancer.Subnets}}
          {{if gt $index 0}},{{end}}
          {{$subnet.Ref}}
          {{end}}
        ],
        {{if .LoadBalancer.Private}}
        "Scheme": "internal"
        {{else}}
        "Scheme": "internet-facing"
        {{end}}
      }
    },
    {{ else }}
    "{{.LoadBalancer.LogicalName}}" : {
      "Type" : "AWS::ElasticLoadBalancing::LoadBalancer",
      "Properties" : {
        "CrossZone" : true,
        "HealthCheck" : {
          "HealthyThreshold" : "3",
          "Interval" : "10",
          "Target" : "SSL:443",
          "Timeout" : "8",
          "UnhealthyThreshold" : "3"
        },
        "ConnectionSettings" : {
          "IdleTimeout" : "3600"
        },
        "Subnets" : [
          {{range $index, $subnet := .LoadBalancer.Subnets}}
          {{if gt $index 0}},{{end}}
          {{$subnet.Ref}}
          {{end}}
        ],
        "Listeners" : [
          {
            "InstancePort" : "443",
            "InstanceProtocol" : "TCP",
            "LoadBalancerPort" : "443",
            "Protocol" : "TCP"
          }
        ],
        {{if .LoadBalancer.Private}}
        "Scheme": "internal",
        {{else}}
        "Scheme": "internet-facing",
        {{end}}
        "SecurityGroups": [
          {{range $sgIndex, $sgRef := .LoadBalancer.SecurityGroupRefs}}
          {{if gt $sgIndex 0}},{{end}}
          {{$sgRef}}
          {{end}}
        ]
      }
    },
    {{ end }}
    {{if .LoadBalancer.ManageSecurityGroup -}}
    "{{.LoadBalancer.SecurityGroupLogicalName}}" : {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupIngress": [
          {{ range $j, $r := .LoadBalancer.APIAccessAllowedSourceCIDRs -}}
          {{if gt $j 0}},{{end}}
          {
            "CidrIp": "{{$r}}",
            "FromPort": 443,
            "IpProtocol": "tcp",
            "ToPort": 443
          }
          {{end}}
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-api-endpoint-{{$i}}"
          }
        ],
        "VpcId": {{$.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    },
    {{end -}}
    {{end -}}
    {{end -}}
    "SecurityGroupElbAPIServer" : {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupIngress": [
          {
             "CidrIp": "0.0.0.0/0",
             "FromPort": -1,
             "IpProtocol": "icmp",
             "ToPort": -1
          }
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-elb-api-server"
          }
        ],
        "VpcId": {{$.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    },
    "SecurityGroupController": {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupEgress": [
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": -1,
            "IpProtocol": "icmp",
            "ToPort": -1
          },
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "tcp",
            "ToPort": 65535
          },
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "udp",
            "ToPort": 65535
          }
        ],
        "SecurityGroupIngress": [
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": -1,
            "IpProtocol": "icmp",
            "ToPort": -1
          },
          {{ range $_, $r := $.SSHAccessAllowedSourceCIDRs -}}
          {
            "CidrIp": "{{$r}}",
            "FromPort": 22,
            "IpProtocol": "tcp",
            "ToPort": 22
          },
          {{end -}}
          {{ if $.KubeClusterSettings.APIEndpointConfigs.HasNetworkLoadBalancers }}
          {{/* Needed for NLB health checks and to allow worker nodes to contact the API servers. */}}
          {
            "CidrIp": "{{.VPCCIDR}}",
            "FromPort": 443,
            "IpProtocol": "tcp",
            "ToPort": 443
          },
          {{ range $_, $r := $.APIAccessAllowedSourceCIDRsForControllerSG -}}
          {
            "CidrIp": "{{$r}}",
            "FromPort": 443,
            "IpProtocol": "tcp",
            "ToPort": 443
          },
          {{ end -}}
          {{ end }}
          {
            "SourceSecurityGroupId" : { "Ref" : "SecurityGroupElbAPIServer" },
            "FromPort": 443,
            "IpProtocol": "tcp",
            "ToPort": 443
          },
          {
            "SourceSecurityGroupId" : { "Ref" : "SecurityGroupWorker" },
            "FromPort": 443,
            "IpProtocol": "tcp",
            "ToPort": 443
          }
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-controller"
          }
        ],
        "VpcId": {{.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    },
    {{/* 443 ingress from controller to controller is required by:
      - API server endpoint with HA controllers
      - calico-policy-controller when calico is enabled
      See https://github.com/kubernetes-incubator/kube-aws/issues/494#issuecomment-291687137
      and https://github.com/kubernetes-incubator/kube-aws/issues/512 */}}
    "SecurityGroupControllerIngressFromControllerToController": {
      "Properties": {
        "FromPort": 443,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 443
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupControllerIngressFromControllerToKubelet": {
      "Properties": {
        "FromPort": 10250,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 10250
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupControllerIngressFromWorkerToEtcd": {
      "Properties": {
        "FromPort": 2379,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 2379
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorker": {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupEgress": [
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": -1,
            "IpProtocol": "icmp",
            "ToPort": -1
          },
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "tcp",
            "ToPort": 65535
          },
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "udp",
            "ToPort": 65535
          }
        ],
        "SecurityGroupIngress": [
          {{ range $_, $r := $.SSHAccessAllowedSourceCIDRs -}}
          {
            "CidrIp": "{{$r}}",
            "FromPort": 22,
            "IpProtocol": "tcp",
            "ToPort": 22
          },
          {{end -}}
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": -1,
            "IpProtocol": "icmp",
            "ToPort": -1
          }
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-worker"
          }
        ],
        "VpcId": {{.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    },
    "SecurityGroupWorkerIngressFromControllerToFlannel": {
      "Properties": {
        "FromPort": 8472,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "udp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 8472
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromFlannelToController": {
      "Properties": {
        "FromPort": 8472,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "udp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 8472
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromControllerToKubelet": {
      "Properties": {
        "FromPort": 10250,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 10250
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromControllerTocAdvisor": {
      "Properties": {
        "FromPort": 4194,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 4194
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupEtcdIngressFromControllerToEtcd": {
      "Properties": {
        "FromPort": 2379,
        "GroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupController"
        },
        "ToPort": 2379
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupEtcdIngressFromWorkerToEtcd": {
      "Properties": {
        "FromPort": 2379,
        "GroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 2379
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToFlannel": {
      "Properties": {
        "FromPort": 8472,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "udp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 8472
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToWorkerKubeletReadOnly": {
      "Properties": {
        "FromPort": 10255,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10255
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToControllerKubeletReadOnly": {
      "Properties": {
        "FromPort": 10255,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10255
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    {{if .Addons.Prometheus.SecurityGroupsEnabled}}
    "SecurityGroupWorkerIngressFromWorkerToControllerKubelet": {
      "Properties": {
        "FromPort": 10250,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10250
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToWorkerKubelet": {
      "Properties": {
        "FromPort": 10250,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10250
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToWorkerCadvisor": {
      "Properties": {
        "FromPort": 4194,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 4194
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToControllerCadvisor": {
      "Properties": {
        "FromPort": 4194,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 4194
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToWorkerNodeExporter": {
      "Properties": {
        "FromPort": 9100,
        "GroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 9100
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToControllerNodeExporter": {
      "Properties": {
        "FromPort": 9100,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 9100
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToControllerControllerManager": {
      "Properties": {
        "FromPort": 10252,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10252
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupWorkerIngressFromWorkerToControllerScheduleManager": {
      "Properties": {
        "FromPort": 10251,
        "GroupId": {
          "Ref": "SecurityGroupController"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupWorker"
        },
        "ToPort": 10251
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    {{ end }}
    "SecurityGroupEtcd": {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupEgress": [
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "tcp",
            "ToPort": 65535
          },
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 0,
            "IpProtocol": "udp",
            "ToPort": 65535
          }
        ],
        "SecurityGroupIngress": [
          {{ range $_, $r := $.SSHAccessAllowedSourceCIDRs -}}
          {
            "CidrIp": "{{$r}}",
            "FromPort": 22,
            "IpProtocol": "tcp",
            "ToPort": 22
          },
          {{end -}}
          {
            "CidrIp": "0.0.0.0/0",
            "FromPort": 3,
            "IpProtocol": "icmp",
            "ToPort": -1
          }
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-etcd"
          }
        ],
        "VpcId": {{.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    },
    "SecurityGroupEtcdPeerHealthCheckIngress": {
      "Properties": {
        "FromPort": 2379,
        "GroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "ToPort": 2379
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    },
    "SecurityGroupEtcdPeerIngress": {
      "Properties": {
        "FromPort": 2380,
        "GroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "IpProtocol": "tcp",
        "SourceSecurityGroupId": {
          "Ref": "SecurityGroupEtcd"
        },
        "ToPort": 2380
      },
      "Type": "AWS::EC2::SecurityGroupIngress"
    }
    {{if or $.ElasticFileSystemID .SharedPersistentVolume}}
    ,
    "SecurityGroupMountTarget": {
      "Properties": {
        "GroupDescription": {
          "Ref": "AWS::StackName"
        },
        "SecurityGroupIngress": [
          {
            "SourceSecurityGroupId": { "Ref": "SecurityGroupWorker" },
            "FromPort": 2049,
            "IpProtocol": "tcp",
            "ToPort": 2049
          },
          {
            "SourceSecurityGroupId": { "Ref": "SecurityGroupController" },
            "FromPort": 2049,
            "IpProtocol": "tcp",
            "ToPort": 2049
          }
        ],
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-sg-mount-target"
          }
        ],
        "VpcId": {{.VPCRef}}
      },
      "Type": "AWS::EC2::SecurityGroup"
    }
    {{ if .SharedPersistentVolume }}
    ,
      "FileSystemCustom": {
        "Type": "AWS::EFS::FileSystem",
        "Properties": {
          "PerformanceMode": "maxIO",
          "FileSystemTags": [
            {
              "Key": "Name",
              "Value": "SharedData"
            }
          ]
        }
      }
      {{range $index, $subnet := .Subnets}}
      ,
      "{{$subnet.LogicalName}}MountTargetCustom": {
        "Properties" : {
          "FileSystemId": { "Ref": "FileSystemCustom" },
          "SubnetId": {{$subnet.Ref}},
          "SecurityGroups": [ { "Ref": "SecurityGroupMountTarget" } ]
        },
        "Type" : "AWS::EFS::MountTarget"
      }
      {{end}}
    {{end}}
    {{end}}

    {{range $index, $subnet := .Subnets}}
    {{if $subnet.ManageSubnet}}
    ,
    "{{$subnet.LogicalName}}": {
      "Properties": {
        "AvailabilityZone": "{{$subnet.AvailabilityZone}}",
        "CidrBlock": "{{$subnet.InstanceCIDR}}",
        "MapPublicIpOnLaunch": {{$subnet.MapPublicIPs}},
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-{{$subnet.LogicalName}}"
          }
        ],
        "VpcId": {{$.VPCRef}}
      },
      "Type": "AWS::EC2::Subnet"
    }
    ,
    "{{$subnet.LogicalName}}RouteTableAssociation": {
      "Properties": {
        "RouteTableId": {{$subnet.RouteTableRef}},
        "SubnetId": {{$subnet.Ref}}
      },
      "Type": "AWS::EC2::SubnetRouteTableAssociation"
    }
    {{if $subnet.ManageRouteTable}}
    ,
    "{{$subnet.RouteTableLogicalName}}": {
      "Properties": {
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-{{$subnet.RouteTableLogicalName}}"
          }
        ],
        "VpcId": {{$.VPCRef}}
      },
      "Type": "AWS::EC2::RouteTable"
    }
    {{end}}
    {{if $.ElasticFileSystemID}}
    ,
    "{{$subnet.LogicalName}}MountTarget": {
      "Properties" : {
        "FileSystemId": "{{$.ElasticFileSystemID}}",
        "SubnetId": {{$subnet.Ref}},
        "SecurityGroups": [ { "Ref": "SecurityGroupMountTarget" } ]
      },
      "Type" : "AWS::EFS::MountTarget"
    }
    {{end}}
    {{if $subnet.ManageRouteToInternet}}
    ,
    "{{$subnet.InternetGatewayRouteLogicalName}}": {
      "Properties": {
        "DestinationCidrBlock": "0.0.0.0/0",
        "GatewayId": {{$.InternetGatewayRef}},
        "RouteTableId": {{$subnet.RouteTableRef}}
      },
      "Type": "AWS::EC2::Route"
    }
    {{end}}
    {{end}}
    {{end}}

    {{range $i, $ngw := .NATGateways}}
    {{if $ngw.ManageEIP}}
    ,
    "{{$ngw.EIPLogicalName}}": {
      "Properties": {
        "Domain": "vpc"
      },
      "Type": "AWS::EC2::EIP"
    }
    {{end}}
    {{if $ngw.ManageNATGateway}}
    ,
    "{{$ngw.LogicalName}}": {
      "Properties": {
        "AllocationId": {{$ngw.EIPAllocationIDRef}},
        "SubnetId": {{$ngw.PublicSubnetRef}}
      },
      "Type": "AWS::EC2::NatGateway"
    }
    {{end}}
    {{range $_, $s := $ngw.PrivateSubnets}}
    {{if $s.ManageRouteToNATGateway}}
    ,
    "{{$s.NATGatewayRouteLogicalName}}": {
      "Properties": {
        "DestinationCidrBlock": "0.0.0.0/0",
        "NatGatewayId": {{$ngw.Ref}},
        "RouteTableId": {{$s.RouteTableRef}}
      },
      "Type": "AWS::EC2::Route"
    }
    {{end}}
    {{end}}
    {{end}}

    {{if .VPCManaged}}
    ,
    "{{.InternetGatewayLogicalName}}": {
      "Properties": {
        "Tags": [
          {
            "Key": "Name",
            "Value": "{{$.ClusterName}}-{{.InternetGatewayLogicalName}}"
          }
        ]
      },
      "Type": "AWS::EC2::InternetGateway"
    }
    ,
    "{{.VPCLogicalName}}": {
      "Properties": {
        "CidrBlock": "{{.VPCCIDR}}",
        "EnableDnsHostnames": true,
        "EnableDnsSupport": true,
        "InstanceTenancy": "default",
        "Tags": [
          {
            "Key": "kubernetes.io/cluster/{{.ClusterName}}",
            "Value": "owned"
          },
          {
            "Key": "Name",
            "Value": "{{.ClusterName}}-vpc"
          }
        ]
      },
      "Type": "AWS::EC2::VPC"
    },
    "VPCGatewayAttachment": {
      "Properties": {
        "InternetGatewayId": {{.InternetGatewayRef}},
        "VpcId": {{.VPCRef}}
      },
      "Type": "AWS::EC2::VPCGatewayAttachment"
    }
    {{end}}
    {{range $n, $r := .ExtraCfnResources}}
    ,
    {{quote $n}}: {{toJSON $r}}
    {{end}}
  },

  "Outputs": {
    {{if .VPCManaged}}
    "VPC" : {
      "Description" : "The VPC managed by this stack",
      "Value" :  { "Ref" : "{{.VPCLogicalName}}" },
      "Export" : { "Name" : {"Fn::Sub": "${AWS::StackName}-VPC" }}
    },
    {{end}}
    {{range $index, $subnet := .Subnets}}
    {{if $subnet.ManageRouteTable}}
    "{{$subnet.RouteTableLogicalName}}" : {
      "Description" : "The route table assigned to the subnet {{$subnet.LogicalName}}",
      "Value" :  {{$subnet.RouteTableRef}},
      "Export" : { "Name" : {"Fn::Sub": "${AWS::StackName}-{{$subnet.RouteTableLogicalName}}" }}
    },
    {{end}}
    {{if $subnet.ManageSubnet}}
    "{{$subnet.LogicalName}}" : {
      "Description" : "The subnet id of {{$subnet.LogicalName}}",
      "Value" :  {{$subnet.Ref}},
      "Export" : { "Name" : {"Fn::Sub": "${AWS::StackName}-{{$subnet.LogicalName}}" }}
    },
    {{end}}
    {{end}}
    {{range $index, $etcdInstance := $.EtcdNodes}}
    {{if $etcdInstance.EIPManaged}}
    "{{$etcdInstance.EIPLogicalName}}": {
      "Description": "The EIP for etcd node {{$index}}",
      "Value": {{$etcdInstance.EIPRef}},
      "Export": { "Name" : {"Fn::Sub": "${AWS::StackName}-{{$etcdInstance.EIPLogicalName}}" }}
    },
    {{end}}
    {{if $etcdInstance.NetworkInterfaceManaged}}
    "{{$etcdInstance.NetworkInterfacePrivateIPLogicalName}}": {
      "Description": "The private IP for etcd node {{$index}}",
      "Value": {{$etcdInstance.NetworkInterfacePrivateIPRef}},
      "Export": { "Name" : {"Fn::Sub": "${AWS::StackName}-{{$etcdInstance.NetworkInterfacePrivateIPLogicalName}}" }}
    },
    {{end}}
    {{end}}
    {{ if not .Controller.IAMConfig.InstanceProfile.Arn }}
    "ControllerIAMRoleArn": {
      "Description": "The ARN of the IAM role for Controllers",
      "Value": { "Fn::GetAtt": ["IAMRoleController", "Arn"] },
      "Export": { "Name": { "Fn::Sub": "${AWS::StackName}-ControllerIAMRoleArn" } }
    },
    {{end}}
    "WorkerSecurityGroup" : {
      "Description" : "The security group assigned to worker nodes",
      "Value" :  { "Ref" : "SecurityGroupWorker" },
      "Export" : { "Name" : {"Fn::Sub": "${AWS::StackName}-WorkerSecurityGroup" }}
    },
    "StackName": {
      "Description": "The name of this stack which is used by node pool stacks to import outputs from this stack",
      "Value": { "Ref": "AWS::StackName" }
    }
  }
}`)
