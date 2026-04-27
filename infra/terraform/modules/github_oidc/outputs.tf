output "role_arn" {
  description = "IAM role ARN to put in the GitHub Actions workflow's role-to-assume"
  value       = aws_iam_role.deploy.arn
}

output "oidc_provider_arn" {
  description = "OIDC provider ARN (created or passed in)"
  value       = local.oidc_provider_arn
}
