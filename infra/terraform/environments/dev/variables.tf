variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Name prefix for all resources"
  type        = string
  default     = "relayer-dev"
}

variable "availability_zones" {
  description = "AZs to deploy into (>=2)"
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}

variable "vpc_cidr" {
  description = "VPC CIDR"
  type        = string
  default     = "10.10.0.0/16"
}

variable "image" {
  description = "Container image URI for the relayer (ECR repo:tag)"
  type        = string
}

variable "desired_count" {
  description = "Number of ECS tasks"
  type        = number
  default     = 1
}

variable "task_cpu" {
  type    = number
  default = 256
}

variable "task_memory" {
  type    = number
  default = 512
}

variable "alb_allowed_cidrs" {
  description = "CIDRs that may hit the ALB. Lock this down for dev."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "certificate_arn" {
  description = "ACM cert ARN for HTTPS. Empty = HTTP-only (dev convenience)."
  type        = string
  default     = ""
}

variable "secrets" {
  description = "Env var -> SSM/Secrets ARN map (e.g. GEMINI_API_KEY for a target's secret_headers)"
  type        = map(string)
  default     = {}
}

variable "alarm_email" {
  description = "Email subscribed to the CloudWatch alarm SNS topic. Empty = no subscription."
  type        = string
  default     = ""
}

variable "github_owner" {
  description = "GitHub owner (user or org) hosting the repo"
  type        = string
  default     = "ricardo1slas"
}

variable "github_repo" {
  description = "GitHub repository name"
  type        = string
  default     = "private_relayer"
}
