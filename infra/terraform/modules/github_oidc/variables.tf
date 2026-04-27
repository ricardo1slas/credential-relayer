variable "github_owner" {
  description = "GitHub user or org that owns the repo"
  type        = string
}

variable "github_repo" {
  description = "GitHub repository name"
  type        = string
}

variable "allowed_refs" {
  description = "GitHub ref patterns that may assume the role (e.g. \"ref:refs/heads/main\")"
  type        = list(string)
  default     = ["ref:refs/heads/main"]
}

variable "role_name" {
  description = "Name for the IAM role GitHub Actions assumes"
  type        = string
}

variable "create_oidc_provider" {
  description = "Whether to create the OIDC provider. Set false if one already exists in the account."
  type        = bool
  default     = true
}

variable "existing_oidc_provider_arn" {
  description = "ARN of an existing GitHub OIDC provider (used when create_oidc_provider = false)"
  type        = string
  default     = ""
}

variable "ecr_repository_arn" {
  description = "ARN of the ECR repository the workflow may push to"
  type        = string
}

variable "ecs_cluster_arn" {
  description = "ARN of the ECS cluster the workflow may describe"
  type        = string
}

variable "ecs_service_arn" {
  description = "ARN of the ECS service the workflow may update"
  type        = string
}

variable "passable_role_arns" {
  description = "Task + execution role ARNs the workflow may PassRole to ECS"
  type        = list(string)
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
