terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.42"
    }
  }
}

locals {
  tags = merge(var.tags, { Module = "github_oidc" })
  # GitHub's OIDC subject is unique per repo + ref. We pin to specific refs
  # so a fork or feature branch can't assume this role.
  subjects = [for ref in var.allowed_refs : "repo:${var.github_owner}/${var.github_repo}:${ref}"]
}

# One OIDC provider per AWS account. If you already have one, set
# create_oidc_provider = false and pass its ARN via existing_oidc_provider_arn.
resource "aws_iam_openid_connect_provider" "github" {
  count          = var.create_oidc_provider ? 1 : 0
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  # AWS validates the GitHub OIDC cert chain itself; thumbprint is no longer
  # used for validation, but the field is still required. Fixed value is fine.
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
  tags            = local.tags
}

locals {
  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.existing_oidc_provider_arn
}

data "aws_iam_policy_document" "assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = local.subjects
    }
  }
}

resource "aws_iam_role" "deploy" {
  name               = var.role_name
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = local.tags
}

# Minimum policy: push to one ECR repo + register task defs for one family
# + update one ECS service. Nothing else.
data "aws_iam_policy_document" "deploy" {
  # ECR auth (must be * per AWS API)
  statement {
    sid       = "ECRAuth"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  # ECR push to the specific repo
  statement {
    sid    = "ECRPush"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
      "ecr:DescribeImages",
    ]
    resources = [var.ecr_repository_arn]
  }

  # Register new task definition revisions for the one family
  statement {
    sid       = "RegisterTaskDef"
    effect    = "Allow"
    actions   = ["ecs:RegisterTaskDefinition", "ecs:DescribeTaskDefinition"]
    resources = ["*"] # RegisterTaskDefinition does not support resource-level perms
  }

  # Update + describe the one service in the one cluster
  statement {
    sid    = "UpdateService"
    effect = "Allow"
    actions = [
      "ecs:UpdateService",
      "ecs:DescribeServices",
    ]
    resources = [var.ecs_service_arn]
  }

  statement {
    sid       = "DescribeCluster"
    effect    = "Allow"
    actions   = ["ecs:DescribeClusters"]
    resources = [var.ecs_cluster_arn]
  }

  # PassRole for the task + execution roles when registering a task def
  statement {
    sid       = "PassRoles"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = var.passable_role_arns
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_policy" "deploy" {
  name   = "${var.role_name}-policy"
  policy = data.aws_iam_policy_document.deploy.json
  tags   = local.tags
}

resource "aws_iam_role_policy_attachment" "deploy" {
  role       = aws_iam_role.deploy.name
  policy_arn = aws_iam_policy.deploy.arn
}
