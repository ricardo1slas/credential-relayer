variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Name prefix for all resources"
  type        = string
  default     = "relayer-prod"
}

variable "availability_zones" {
  description = "AZs to deploy into (>=2)"
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}

variable "vpc_cidr" {
  description = "VPC CIDR"
  type        = string
  default     = "10.20.0.0/16"
}

variable "image" {
  description = "Container image URI for the relayer (ECR repo:tag)"
  type        = string
}

variable "desired_count" {
  description = "Number of ECS tasks"
  type        = number
  default     = 2
}

variable "task_cpu" {
  type    = number
  default = 512
}

variable "task_memory" {
  type    = number
  default = 1024
}

variable "alb_allowed_cidrs" {
  description = "CIDRs allowed to hit the ALB"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "certificate_arn" {
  description = "ACM cert ARN for HTTPS. Required for prod."
  type        = string
}

variable "secrets" {
  description = "Env var -> SSM/Secrets ARN map"
  type        = map(string)
  default     = {}
}
