package config

var StackTemplateTemplate = []byte(`{{define "Metadata"}}
{
  "AWS::CloudFormation::Init" : {
    "configSets" : {
      "etcd-client": [ "etcd-client-env" ]{{ if .AwsEnvironment.Enabled }},
      "aws-environment": [ "aws-environment-env" ]{{end}}
    },
    {{ if .AwsEnvironment.Enabled }}
    "aws-environment-env" : {
      "commands": {
         "write-environment": {
          "command": {
            "Fn::Join" : ["", [ "echo '",
            {{range $variable, $function := .AwsEnvironment.Environment}}
            "{{$variable}}=", {{$function}} , "\n",
            {{end}}
            "' > /etc/aws-environment" ] ]
          }
        }
      }
    },
    {{end}}
    "etcd-client-env": {
      "files" : {
        "/var/run/coreos/etcd-environment": {
          "content": { "Fn::Join" : [ "", [
            "ETCD_ENDPOINTS='",
            {{range $index, $instance := $.EtcdNodes}}
            {{if $index}}",", {{end}} "https://",
            {{$instance.ImportedAdvertisedFQDNRef}},
            ":2379",
            {{end}}
            "'\n"
          ]]}
        }
      }
    }
  }
}
{{end}}
{{define "SpotFleet"}}
  "{{.LogicalName}}": {
    "Type": "AWS::EC2::SpotFleet",
    "Properties": {
      "SpotFleetRequestConfigData": {
        "IamFleetRole": {{$.SpotFleet.IAMFleetRoleRef}},
        "AllocationStrategy": "diversified",
        "TargetCapacity": {{$.SpotFleet.TargetCapacity}},
        "SpotPrice": "{{$.SpotFleet.SpotPrice}}",
        "LaunchSpecifications": [
          {{range $subnetIndex, $workerSubnet := $.Subnets}}
          {{range $specIndex, $spec := $.SpotFleet.LaunchSpecifications}}
          {{if or (gt $subnetIndex 0) (gt $specIndex 0) }},{{end}}
          {
            "ImageId": "{{$.AMI}}",
            "Monitoring": { "Enabled": "true" },
            "InstanceType": "{{$spec.InstanceType}}",
            {{if $.KeyName}}"KeyName": "{{$.KeyName}}",{{end}}
            "WeightedCapacity": {{$spec.WeightedCapacity}},
            {{if $spec.SpotPrice}}
            "SpotPrice": "{{$spec.SpotPrice}}",
            {{end}}
            {{if $.IAMConfig.InstanceProfile.Arn }}
            "IamInstanceProfile": {
              "Arn": "{{$.IAMConfig.InstanceProfile.Arn}}" 
            },
            {{else}}
            "IamInstanceProfile": {
              "Arn": {
                "Fn::GetAtt" : ["IAMInstanceProfileWorker", "Arn"]
              }
            },
            {{end}}
            "BlockDeviceMappings": [
              {
                "DeviceName": "/dev/xvda",
                "Ebs": {
                  "VolumeSize": "{{$spec.RootVolume.Size}}",
                  {{if gt $spec.RootVolume.IOPS 0}}
                  "Iops": "{{$spec.RootVolume.IOPS}}",
                  {{end}}
                  "VolumeType": "{{$spec.RootVolume.Type}}"
                }
              }{{range $volumeMountSpecIndex, $volumeMountSpec := $.VolumeMounts}},
              {
                "DeviceName": "{{$volumeMountSpec.Device}}",
                "Ebs": {
                  "VolumeSize": "{{$volumeMountSpec.Size}}",
                  {{if gt $volumeMountSpec.Iops 0}}
                  "Iops": "{{$volumeMountSpec.Iops}}",
                  {{end}}
                  "VolumeType": "{{$volumeMountSpec.Type}}"
                }
              }
              {{- end -}}
            ],
            "SecurityGroups": [
              {{range $sgIndex, $sgRef := $.SecurityGroupRefs}}
              {{if gt $sgIndex 0}},{{end}}
              {"GroupId":{{$sgRef}}}
              {{end}}
            ],
            "SubnetId": {{$workerSubnet.Ref}},
            "UserData": {{ $.UserDataWorker.Parts.instance.Template }}
          }
          {{end}}
          {{end}}
        ]
      }
    },
    "Metadata": {{template "Metadata" .}}
  },
{{end}}
{{define "AutoScaling"}}
    "{{.LogicalName}}": {
      "Properties": {
        "HealthCheckGracePeriod": 600,
        "HealthCheckType": "EC2",
        "LaunchConfigurationName": {
          "Ref": "{{.LogicalName}}LC"
        },
        "MaxSize": "{{.MaxCount}}",
        "MetricsCollection": [
          {
            "Granularity": "1Minute"
          }
        ],
        "MinSize": "{{.MinCount}}",
        "Tags": [
          {{if .Autoscaling.ClusterAutoscaler.Enabled}}
          {
            "Key": "{{.Autoscaling.ClusterAutoscaler.AutoDiscoveryTagKey}}",
            "PropagateAtLaunch": "false",
            "Value": ""
          },
          {{end}}
          {{range $k, $v := .InstanceTags -}}
          {
            "Key": "{{$k}}",
            "PropagateAtLaunch": "true",
            "Value": "{{$v}}"
          },
          {{end -}}
          {
            "Key": "kubernetes.io/cluster/{{ .ClusterName }}",
            "PropagateAtLaunch": "true",
            "Value": "owned"
          },
          {
            "Key": "kube-aws:node-pool:name",
            "PropagateAtLaunch": "true",
            "Value": "{{.NodePoolName}}"
          },
          {
            "Key": "Name",
            "PropagateAtLaunch": "true",
            "Value": "{{.ClusterName}}-{{.StackName}}-kube-aws-worker"
          }
        ],
        {{if .LoadBalancer.Enabled}}
        "LoadBalancerNames" : [
          {{range $index, $elb := .LoadBalancer.Names}}
          {{if $index}},{{end}}
          "{{$elb}}"
          {{end}}
        ],
        {{end}}
        {{if .TargetGroup.Enabled}}
        "TargetGroupARNs" : [
          {{range $index, $tg := .TargetGroup.Arns}}
          {{if $index}},{{end}}
          "{{$tg}}"
          {{end}}
        ],
        {{end}}
        "VPCZoneIdentifier": [
          {{range $index, $subnet := .Subnets}}
          {{if gt $index 0}},{{end}}
          {{$subnet.Ref}}
          {{end}}
        ]
      },
      "Type": "AWS::AutoScaling::AutoScalingGroup",
      {{if .WaitSignal.Enabled}}
      "CreationPolicy" : {
        "ResourceSignal" : {
          "Count" : "{{.MinCount}}",
          "Timeout" : "{{.CreateTimeout}}"
        }
      },
      {{end}}
      "UpdatePolicy" : {
        "AutoScalingRollingUpdate" : {
          "MinInstancesInService" :
          {{if .SpotPrice}}
          "0"
          {{else}}
          "{{.RollingUpdateMinInstancesInService}}"
          {{end}},
          {{if .WaitSignal.Enabled}}
          "WaitOnResourceSignals" : "true",
          "MaxBatchSize" : "{{.WaitSignal.MaxBatchSize}}",
          "PauseTime": "{{.CreateTimeout}}"
          {{else}}
          "MaxBatchSize" : "1",
          "PauseTime": "PT2M"
          {{end}}
        }
      },
      "Metadata": {{template "Metadata" .}}
    },
    {{if .NodeDrainer.Enabled }}
    "{{.LogicalName}}NodeDrainerLH" : {
      "Properties" : {
        "AutoScalingGroupName" : {
          "Ref": "{{.LogicalName}}"
        },
        "DefaultResult" : "CONTINUE",
        "HeartbeatTimeout" : "{{.NodeDrainer.DrainTimeoutInSeconds}}",
        "LifecycleTransition" : "autoscaling:EC2_INSTANCE_TERMINATING"
      },
      "Type" : "AWS::AutoScaling::LifecycleHook"
    },
    {{end}}
    "{{.LogicalName}}LC": {
      "Properties": {
        "BlockDeviceMappings": [
          {
            "DeviceName": "/dev/xvda",
            "Ebs": {
              "VolumeSize": "{{.RootVolume.Size}}",
              {{if gt .RootVolume.IOPS 0}}
              "Iops": "{{.RootVolume.IOPS}}",
              {{end}}
              "VolumeType": "{{.RootVolume.Type}}"
            }
          }{{range $volumeMountSpecIndex, $volumeMountSpec := .VolumeMounts}},
          {
            "DeviceName": "{{$volumeMountSpec.Device}}",
            "Ebs": {
              "VolumeSize": "{{$volumeMountSpec.Size}}",
              {{if gt $volumeMountSpec.Iops 0}}
              "Iops": "{{$volumeMountSpec.Iops}}",
              {{end}}
              "VolumeType": "{{$volumeMountSpec.Type}}"
            }
          }
          {{- end -}}
        ],
         {{if .IAMConfig.InstanceProfile.Arn }}
        "IamInstanceProfile": "{{.IAMConfig.InstanceProfile.Arn}}",
        {{else}}
        "IamInstanceProfile": { "Ref": "IAMInstanceProfileWorker" },
        {{end}}
        "ImageId": "{{.AMI}}",
        "InstanceType": "{{.InstanceType}}",
        {{if .KeyName}}"KeyName": "{{.KeyName}}",{{end}}
        "SecurityGroups": [
          {{range $sgIndex, $sgRef := $.SecurityGroupRefs}}
          {{if gt $sgIndex 0}},{{end}}
          {{$sgRef}}
          {{end}}
        ],
        {{if .SpotPrice}}
        "SpotPrice": {{.SpotPrice}},
        {{else}}
        "PlacementTenancy": "{{.Tenancy}}",
        {{end}}
        "UserData": {{ .UserDataWorker.Parts.instance.Template }}
      },
      "Type": "AWS::AutoScaling::LaunchConfiguration"
    {{if not .IAMConfig.InstanceProfile.Arn}}
    },
    {{else}}
    }
    {{end}}
{{end}}
{{define "IAMRole"}}
    "IAMInstanceProfileWorker": {
      "Properties": {
        "Path": "/",
        "Roles": [
          {
            "Ref": "IAMRoleWorker"
          }
        ]
      },
      "Type": "AWS::IAM::InstanceProfile"
    },
    "IAMManagedPolicyWorker" : {
      "Type" : "AWS::IAM::ManagedPolicy",
      "Properties" : {
        "Description" : "Policy for managing kube-aws k8s Node Pool {{.NodePoolName}} ",
        "Path" : "/",
        "PolicyDocument" :   {
          "Version":"2012-10-17",
          "Statement": [
                {{range $s := .IAMConfig.Policy.Statements }}
                {
                  "Action": {{toJSON $s.Actions}},
                  "Effect": {{toJSON $s.Effect}},
                  "Resource": {{toJSON $s.Resources}}
                },
                {{end}}
                {
                  "Action": "ec2:Describe*",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {
                  "Action": "ec2:AttachVolume",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {
                  "Action": "ec2:DetachVolume",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {{- if $.UserDataWorker.Parts.s3 }}
                {
                  "Effect": "Allow",
                  "Action": [
                    "s3:GetObject"
                  ],
                  "Resource": "arn:{{.Region.Partition}}:s3:::{{ $.UserDataWorker.Parts.s3.Asset.S3Prefix }}*"
                },
                {{- end }}
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
                },
                {{ end }}
                {{ if .KubeResourcesAutosave.Enabled }}
                {
                  "Effect": "Allow",
                  "Action": [
                    "s3:PutObject"
                  ],
                  "Resource": "arn:{{.Region.Partition}}:s3:::{{ .KubeResourcesAutosave.S3Path }}/*"
                },
                {{end}}
                {{if .Kube2IamSupport.Enabled }}
                {
                  "Action": "sts:AssumeRole",
                  "Effect":"Allow",
                  "Resource":"*"
                },
                {{end}}
                {{if .AssetsEncryptionEnabled }}
                {
                  "Action" : "kms:Decrypt",
                  "Effect" : "Allow",
                  "Resource" : "{{.KMSKeyARN}}"
                },
                {{end}}
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
                {{if .AwsNodeLabels.Enabled}}
                {
                  "Action": "autoscaling:Describe*",
                  "Effect": "Allow",
                  "Resource": [ "*" ]
                },
                {{end}}
                {{if .ClusterAutoscalerSupport.Enabled}}
                {
                  "Action": [
                    "autoscaling:DescribeAutoScalingGroups",
                    "autoscaling:DescribeAutoScalingInstances",
                    "autoscaling:DescribeTags"
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
                {{if .SpotFleet.Enabled}}
                {
                  "Action": "ec2:CreateTags",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {{end}}
                {{if or .LoadBalancer.Enabled  .TargetGroup.Enabled}}
                {
                  "Action": "elasticloadbalancing:*",
                  "Effect": "Allow",
                  "Resource": "*"
                },
                {{end}}
                {{if .NodeDrainer.Enabled }}
                {
                  "Action": [
                    "autoscaling:DescribeAutoScalingInstances",
                    "autoscaling:DescribeLifecycleHooks"
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
    "IAMRoleWorker": {
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
        {{if .IAMConfig.Role.Name }}
        "RoleName":  {"Fn::Join": ["",[{"Ref": "AWS::Region"},"-","{{.IAMConfig.Role.Name}}"]]},
        {{end}}
        "ManagedPolicyArns": [ 
          {{range $policyIndex, $policyArn := .IAMConfig.Role.ManagedPolicies }}
            "{{$policyArn.Arn}}",
          {{end}}
          {"Ref": "IAMManagedPolicyWorker"}
        ]
      },
      "Type": "AWS::IAM::Role"
    }
{{end}}
{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Description": "kube-aws Kubernetes node pool {{.ClusterName}} {{.NodePoolName}}",
  "Parameters" : {
    "ControlPlaneStackName": {
      "Type": "String",
      "Description": "The name of a control-plane stack used to import values into this stack"
    }
    {{if .CloudWatchLogging.Enabled}}
    ,
    "CloudWatchLogGroupARN": {
      "Type": "String",
      "Description": "CloudWatch LogGroup to send journald logs to"
    }
    {{ end }}
  },
  "Resources": {
    {{if .SpotFleet.Enabled}}
    {{template "SpotFleet" .}}
    {{else}}
    {{template "AutoScaling" .}}
    {{end}}
    {{if not .IAMConfig.InstanceProfile.Arn}}
    {{template "IAMRole" .}}
    {{end}}
    {{range $n, $r := .ExtraCfnResources}}
    ,
    {{quote $n}}: {{toJSON $r}}
    {{end}}
  },
  "Outputs": {
    {{if not .IAMConfig.InstanceProfile.Arn }}
    "WorkerIAMRoleArn": {
      "Description": "The ARN of the IAM role for this Node Pool",
      "Value": { "Fn::GetAtt": ["IAMRoleWorker", "Arn"] },
      "Export": { "Name": { "Fn::Sub": "${AWS::StackName}-WorkerIAMRoleArn" } }
    },
    {{end}}
    "StackName": {
      "Description": "The name of this stack",
      "Value": { "Ref": "AWS::StackName" }
    }
  }
}`)
