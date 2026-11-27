# The mock provider makes these runs free of AWS credentials and of real resources;
# they check what the configuration asks AWS for, against the deployment table in
# docs/design/basic-design.md and ADR-0006. What a mock cannot show (the rule's
# empty filter, ignore_changes on the AMI and the user data) is checked in scripts/deploy_test.sh.
mock_provider "aws" {
  # The provider validates alarm actions as ARNs, which a random mock value is not.
  mock_resource "aws_sns_topic" {
    defaults = {
      arn = "arn:aws:sns:ap-northeast-1:123456789012:firstline-alerts"
    }
  }
}

variables {
  backup_bucket            = "firstline-backup-test"
  alert_email              = "ops@example.com"
  limit_load_items_per_sec = 400
  domain                   = "tickets.example.com"
  firstline_version        = "v1.2.3"
  firstline_sha256         = format("%064d", 7)
}

run "backup_bucket_keeps_old_versions_for_30_days" {
  command = apply

  assert {
    condition     = aws_s3_bucket_versioning.backup.versioning_configuration[0].status == "Enabled"
    error_message = "versioning must be Enabled, or an overwrite from a compromised host destroys the backup"
  }
  assert {
    condition     = aws_s3_bucket.backup.force_destroy == false
    error_message = "force_destroy would let terraform destroy delete every version"
  }
  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.backup.rule) == 1
    error_message = "one lifecycle rule is expected"
  }
  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.backup.rule[0].status == "Enabled"
    error_message = "the lifecycle rule must be Enabled"
  }
  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.backup.rule[0].noncurrent_version_expiration[0].noncurrent_days == 30
    error_message = "noncurrent versions must expire after 30 days"
  }
  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.backup.rule[0].expiration[0].expired_object_delete_marker == true
    error_message = "expired delete markers must be cleaned up"
  }
}

run "backup_bucket_is_private" {
  command = apply

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.backup.block_public_acls,
      aws_s3_bucket_public_access_block.backup.block_public_policy,
      aws_s3_bucket_public_access_block.backup.ignore_public_acls,
      aws_s3_bucket_public_access_block.backup.restrict_public_buckets,
    ])
    error_message = "all four public access blocks must be on"
  }
}

run "role_can_replicate_but_not_remove_versions" {
  command = apply

  assert {
    condition = toset(flatten([for s in jsondecode(aws_iam_role_policy.litestream.policy).Statement : s.Action])) == toset([
      "s3:GetBucketLocation", "s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject",
    ])
    error_message = "the role gets exactly the five S3 actions of the design; s3:DeleteObjectVersion and bucket settings stay out"
  }
  assert {
    condition = toset(flatten([for s in jsondecode(aws_iam_role_policy.litestream.policy).Statement : s.Resource])) == toset([
      aws_s3_bucket.backup.arn, "${aws_s3_bucket.backup.arn}/*",
    ])
    error_message = "the S3 actions apply to the backup bucket only"
  }
  assert {
    condition     = alltrue([for s in jsondecode(aws_iam_role_policy.litestream.policy).Statement : s.Effect == "Allow"])
    error_message = "every statement is an Allow"
  }
  assert {
    condition = toset([for a in aws_iam_role_policy_attachment.managed : a.policy_arn]) == toset([
      "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy",
      "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
    ])
    error_message = "only the CloudWatch agent and Session Manager managed policies are attached"
  }
}

run "only_443_and_80_are_open" {
  command = apply

  assert {
    condition     = toset([for r in aws_vpc_security_group_ingress_rule.web : r.from_port]) == toset([80, 443])
    error_message = "ingress opens 443 and 80 only; 22 stays closed"
  }
  assert {
    condition     = alltrue([for r in aws_vpc_security_group_ingress_rule.web : r.from_port == r.to_port && r.ip_protocol == "tcp"])
    error_message = "each ingress rule is a single TCP port"
  }
}

run "host_disk_and_metadata" {
  command = apply

  assert {
    condition     = aws_instance.host.instance_type == "t4g.small"
    error_message = "ADR-0006 sizes the host at t4g.small"
  }
  assert {
    condition     = aws_instance.host.metadata_options[0].http_tokens == "required"
    error_message = "IMDSv2 must be required"
  }
  assert {
    condition = (
      aws_instance.host.root_block_device[0].volume_type == "gp3" &&
      aws_instance.host.root_block_device[0].volume_size == 20 &&
      aws_instance.host.root_block_device[0].encrypted == true
    )
    error_message = "the root disk is an encrypted 20 GB gp3"
  }
  assert {
    condition     = aws_instance.host.associate_public_ip_address == false
    error_message = "the host takes its public address from the Elastic IP only"
  }
  assert {
    condition     = aws_eip_association.host.instance_id == aws_instance.host.id
    error_message = "the Elastic IP is attached to the host"
  }
}

run "s3_goes_through_the_gateway_endpoint" {
  command = apply

  assert {
    condition     = aws_vpc_endpoint.s3.vpc_endpoint_type == "Gateway"
    error_message = "the S3 endpoint is the free gateway type"
  }
  assert {
    condition     = aws_vpc_endpoint.s3.service_name == "com.amazonaws.ap-northeast-1.s3"
    error_message = "the endpoint serves S3 in Tokyo"
  }
  assert {
    condition     = contains(aws_vpc_endpoint.s3.route_table_ids, aws_route_table.public.id)
    error_message = "the subnet's route table uses the endpoint"
  }
}

run "alarms_follow_the_design_table" {
  command = apply

  assert {
    condition = {
      for k, a in aws_cloudwatch_metric_alarm.this :
      k => [a.namespace, a.metric_name, a.statistic, a.comparison_operator, a.threshold, a.treat_missing_data]
      } == {
      errors = ["firstline", "errors", "Sum", "GreaterThanOrEqualToThreshold", 1, "notBreaching"]
      queue  = ["firstline", "queue_usage_avg_5m", "Maximum", "GreaterThanThreshold", 0.5, "breaching"]
      scale  = ["firstline", "sync_items_per_sec_max_5m", "Maximum", "GreaterThanThreshold", 200, "breaching"]
      wal    = ["firstline", "wal_bytes", "Maximum", "GreaterThanThreshold", 1073741824, "breaching"]
      disk   = ["CWAgent", "disk_used_percent", "Maximum", "GreaterThanThreshold", 50, "breaching"]
      cert   = ["firstline", "tls_cert_days_left", "Minimum", "LessThanThreshold", 14, "breaching"]
    }
    error_message = "the six alarms must match the alarm table of docs/design/basic-design.md"
  }
  assert {
    condition     = alltrue([for a in aws_cloudwatch_metric_alarm.this : a.period == 300 && a.evaluation_periods == 1 && try(length(a.dimensions), 0) == 0])
    error_message = "each alarm looks at one 5-minute period of a metric without dimensions"
  }
  assert {
    condition = alltrue([
      for a in aws_cloudwatch_metric_alarm.this :
      a.alarm_actions == toset([aws_sns_topic.alerts.arn]) && a.ok_actions == toset([aws_sns_topic.alerts.arn])
    ])
    error_message = "every alarm mails the operator when it fires and when it clears"
  }
  assert {
    condition     = aws_sns_topic_subscription.operator.protocol == "email" && aws_sns_topic_subscription.operator.endpoint == "ops@example.com"
    error_message = "the topic mails the address given in alert_email"
  }
  assert {
    condition = (
      aws_cloudwatch_log_metric_filter.errors.pattern == "{ $.level = \"ERROR\" }" &&
      aws_cloudwatch_log_metric_filter.errors.log_group_name == aws_cloudwatch_log_group.app.name &&
      aws_cloudwatch_log_metric_filter.errors.metric_transformation[0].namespace == "firstline" &&
      aws_cloudwatch_log_metric_filter.errors.metric_transformation[0].name == "errors" &&
      aws_cloudwatch_log_metric_filter.errors.metric_transformation[0].value == "1" &&
      aws_cloudwatch_log_metric_filter.errors.metric_transformation[0].default_value == "0"
    )
    error_message = "the error metric counts ERROR lines and reports 0 when logs arrive without one"
  }
  assert {
    condition     = aws_cloudwatch_log_group.app.name == "/firstline/app" && aws_cloudwatch_log_group.app.retention_in_days == 30
    error_message = "the app log group matches deploy/cloudwatch-agent.json"
  }
}

run "alert_email_must_be_an_address" {
  command = plan
  variables {
    alert_email = "ops"
  }
  expect_failures = [var.alert_email]
}

run "limit_load_must_be_positive" {
  command = plan
  variables {
    limit_load_items_per_sec = 0
  }
  expect_failures = [var.limit_load_items_per_sec]
}

run "scale_alarm_waits_for_the_measured_limit" {
  command = apply
  variables {
    limit_load_items_per_sec = null
  }

  assert {
    condition     = keys(aws_cloudwatch_metric_alarm.this) == ["cert", "disk", "errors", "queue", "wal"]
    error_message = "before the load test of ADR-0003 there is no limit, so only the scale alarm is left out"
  }
}

run "backup_bucket_must_be_a_valid_s3_name" {
  command = plan
  variables {
    backup_bucket = "Bad_Name!"
  }
  expect_failures = [var.backup_bucket]
}

run "backup_bucket_must_not_hold_adjacent_periods" {
  command = plan
  variables {
    backup_bucket = "fire..line"
  }
  expect_failures = [var.backup_bucket]
}

run "alert_email_must_be_one_address" {
  command = plan
  variables {
    alert_email = "ops@example.com,other"
  }
  expect_failures = [var.alert_email]
}

run "only_ec2_assumes_the_role" {
  command = apply

  assert {
    condition = jsondecode(aws_iam_role.host.assume_role_policy).Statement == [{
      Effect    = "Allow"
      Action    = ["sts:AssumeRole"]
      Principal = { Service = "ec2.amazonaws.com" }
    }]
    error_message = "only the EC2 service may assume the host role"
  }
}

run "host_hardening" {
  command = apply

  assert {
    condition     = aws_instance.host.metadata_options[0].http_put_response_hop_limit == 1
    error_message = "a hop limit of 1 keeps the role's credentials away from containers on the host"
  }
  assert {
    condition     = aws_instance.host.disable_api_termination == true
    error_message = "termination protection must be on"
  }
  assert {
    condition     = strcontains(data.aws_ssm_parameter.ubuntu.name, "/24.04/stable/current/arm64/")
    error_message = "t4g is Arm, so the AMI is Ubuntu 24.04 for arm64"
  }
}

run "backup_bucket_hygiene" {
  command = apply

  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.backup.rule[0].abort_incomplete_multipart_upload[0].days_after_initiation == 7
    error_message = "unfinished multipart uploads must be cleaned up after 7 days"
  }
  assert {
    condition = jsondecode(aws_s3_bucket_policy.backup.policy).Statement == [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.backup.arn, "${aws_s3_bucket.backup.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
    error_message = "the bucket must refuse requests without TLS"
  }
  assert {
    condition     = aws_cloudwatch_log_group.metrics.name == "/firstline/metrics" && aws_cloudwatch_log_group.metrics.retention_in_days == 30
    error_message = "the EMF log group keeps 30 days like the other groups"
  }
}

# Terraform has no base64gunzip, so the checks read local.user_data and then pin the
# instance's user_data_base64 to its gzip.
run "user_data_installs_the_release_without_a_secret" {
  command = apply

  assert {
    condition     = aws_instance.host.user_data_base64 == base64gzip(local.user_data)
    error_message = "the host boots with the gzipped cloud-config of local.user_data"
  }
  assert {
    condition     = startswith(local.user_data, "#cloud-config\n")
    error_message = "cloud-init reads the user data as cloud-config only after the #cloud-config line"
  }
  assert {
    condition = yamldecode(local.user_data).write_files[0] == {
      path        = "/etc/firstline/install.env"
      permissions = "0644"
      content     = <<-EOT
        FIRSTLINE_VERSION=v1.2.3
        FIRSTLINE_SHA256=${format("%064d", 7)}
        FIRSTLINE_DOMAIN=tickets.example.com
        FIRSTLINE_ACME_EMAIL=ops@example.com
        LITESTREAM_BUCKET=firstline-backup-test
        FIRSTLINE_TOKEN_PARAM=/firstline/token-key
      EOT
    }
    error_message = "install.env carries the version, hash, domain, mail address, bucket and the parameter name, and no secret"
  }
  assert {
    condition = (
      yamldecode(local.user_data).write_files[1].path == "/usr/local/sbin/firstline-install" &&
      yamldecode(local.user_data).write_files[1].permissions == "0755" &&
      yamldecode(local.user_data).write_files[1].content == file("../firstline-install")
    )
    error_message = "the install script is written verbatim from deploy/firstline-install"
  }
  assert {
    condition     = yamldecode(local.user_data).runcmd == [["/usr/local/sbin/firstline-install"]]
    error_message = "cloud-init runs the install script once"
  }
  assert {
    condition     = length(keys(yamldecode(local.user_data))) == 2 && length(yamldecode(local.user_data).write_files) == 2
    error_message = "the user data holds only the two files and the command"
  }
  assert {
    # EC2 takes at most 16 KB of user data before base64.
    condition     = length(aws_instance.host.user_data_base64) * 3 / 4 < 16384
    error_message = "the user data must stay under the 16 KB limit of EC2"
  }
  assert {
    condition     = aws_instance.host.user_data_replace_on_change == false
    error_message = "a change of the user data must never replace the host"
  }
}

run "domain_must_be_a_host_name" {
  command = plan
  variables {
    domain = "https://tickets.example.com"
  }
  expect_failures = [var.domain]
}

run "firstline_version_must_be_a_release_tag" {
  command = plan
  variables {
    firstline_version = "1.2.3"
  }
  expect_failures = [var.firstline_version]
}

run "firstline_sha256_must_be_64_hex_characters" {
  command = plan
  variables {
    firstline_sha256 = "abc"
  }
  expect_failures = [var.firstline_sha256]
}
