output "repository_url" {
  description = "ECR repository URL (use as base for image tags)"
  value       = aws_ecr_repository.this.repository_url
}

output "repository_arn" {
  value = aws_ecr_repository.this.arn
}

output "repository_name" {
  value = aws_ecr_repository.this.name
}
