data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
}

resource "aws_vpc" "main" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "firstline" }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.0.0/24"
  availability_zone = "ap-northeast-1a"
  tags              = { Name = "firstline-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.ap-northeast-1.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.public.id]
}

resource "aws_security_group" "host" {
  name   = "firstline-host"
  vpc_id = aws_vpc.main.id
}

resource "aws_vpc_security_group_ingress_rule" "web" {
  for_each = { https = 443, http = 80 }

  security_group_id = aws_security_group.host.id
  description       = each.key
  ip_protocol       = "tcp"
  from_port         = each.value
  to_port           = each.value
  cidr_ipv4         = "0.0.0.0/0"
}

# Let's Encrypt, Session Manager, CloudWatch and mail all leave from the host.
resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.host.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_s3_bucket" "backup" {
  bucket        = var.backup_bucket
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "backup" {
  bucket = aws_s3_bucket.backup.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id

  rule {
    id     = "expire-old-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }

    expiration {
      expired_object_delete_marker = true
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  # A lifecycle rule on noncurrent versions needs versioning in place first.
  depends_on = [aws_s3_bucket_versioning.backup]
}

resource "aws_s3_bucket_policy" "backup" {
  bucket = aws_s3_bucket.backup.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.backup.arn, "${aws_s3_bucket.backup.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })

  # A bucket policy is refused while the public access block is being created.
  depends_on = [aws_s3_bucket_public_access_block.backup]
}

resource "aws_s3_bucket_public_access_block" "backup" {
  bucket                  = aws_s3_bucket.backup.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_iam_role" "host" {
  name = "firstline-host"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = ["sts:AssumeRole"]
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}

# AmazonSSMManagedInstanceCore already allows ssm:GetParameter on every parameter, and
# the AWS managed key aws/ssm lets every principal of the account decrypt, so reading
# the SecureString /firstline/token-key needs no statement of its own. The same breadth
# would let a compromised host read another system's SecureString; the parameters
# policy below denies everything outside /firstline/.
resource "aws_iam_role_policy_attachment" "managed" {
  for_each = toset([
    "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy",
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ])

  role       = aws_iam_role.host.name
  policy_arn = each.value
}

# No s3:DeleteObjectVersion and no bucket-settings actions: a compromised host can
# overwrite or delete, but the previous versions stay for the lifecycle's 30 days.
resource "aws_iam_role_policy" "litestream" {
  name = "litestream-replica"
  role = aws_iam_role.host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:GetBucketLocation", "s3:ListBucket"]
        Resource = [aws_s3_bucket.backup.arn]
      },
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Resource = ["${aws_s3_bucket.backup.arn}/*"]
      },
    ]
  })
}

# An explicit Deny overrides the managed policy's Allow. A parameter whose name starts
# with a slash has a single slash after "parameter" in its ARN. This also denies the
# AmazonCloudWatch-* parameters CloudWatchAgentServerPolicy allows; the agent here
# reads its config from a file instead.
resource "aws_iam_role_policy" "parameters" {
  name = "firstline-parameters-only"
  role = aws_iam_role.host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect      = "Deny"
      Action      = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"]
      NotResource = ["arn:aws:ssm:*:*:parameter/firstline/*"]
    }]
  })
}

resource "aws_iam_instance_profile" "host" {
  name = "firstline-host"
  role = aws_iam_role.host.name
}

# No secret here: the host reads FIRSTLINE_TOKEN_KEY from Parameter Store itself, so
# neither the user data nor the state holds it. The ACME account takes the address
# the alarms use.
locals {
  user_data = "#cloud-config\n${yamlencode({
    write_files = [
      {
        path        = "/etc/firstline/install.env"
        permissions = "0644"
        content     = <<-EOT
          FIRSTLINE_VERSION=${var.firstline_version}
          FIRSTLINE_SHA256=${var.firstline_sha256}
          FIRSTLINE_DOMAIN=${var.domain}
          FIRSTLINE_ACME_EMAIL=${var.alert_email}
          LITESTREAM_BUCKET=${aws_s3_bucket.backup.bucket}
          FIRSTLINE_TOKEN_PARAM=/firstline/token-key
        EOT
      },
      {
        path        = "/usr/local/sbin/firstline-install"
        permissions = "0755"
        content     = file("${path.module}/../firstline-install")
      },
    ]
    runcmd = [["/usr/local/sbin/firstline-install"]]
  })}"
}

resource "aws_instance" "host" {
  ami                         = data.aws_ssm_parameter.ubuntu.value
  instance_type               = "t4g.small"
  subnet_id                   = aws_subnet.public.id
  vpc_security_group_ids      = [aws_security_group.host.id]
  iam_instance_profile        = aws_iam_instance_profile.host.name
  associate_public_ip_address = false

  disable_api_termination = true

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 20
    encrypted   = true
  }

  tags = { Name = "firstline" }

  # cloud-init runs the user data at first boot only; upgrades rerun firstline-install
  # on the host, so a change here must neither replace nor restart the host.
  user_data_base64            = base64gzip(local.user_data)
  user_data_replace_on_change = false

  # The SQLite database lives on the root disk, so terraform must refuse any plan
  # that replaces or destroys the host. The SSM parameter moves to each new Ubuntu
  # image; following it would be such a replacement.
  lifecycle {
    prevent_destroy = true
    ignore_changes  = [ami, user_data_base64]
  }
}

resource "aws_eip" "host" {
  domain = "vpc"

  # A new address would break the DNS A record.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_eip_association" "host" {
  instance_id   = aws_instance.host.id
  allocation_id = aws_eip.host.id
}
