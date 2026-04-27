variable "name" {
  description = "Name prefix for ALB resources"
  type        = string
}

variable "vpc_id" {
  description = "VPC the ALB lives in"
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnets for the ALB (must span >= 2 AZs)"
  type        = list(string)
}

variable "target_port" {
  description = "Port the ECS tasks listen on"
  type        = number
  default     = 8080
}

variable "health_check_path" {
  description = "HTTP path the ALB uses to verify task health"
  type        = string
  default     = "/healthz"
}

variable "allowed_cidrs" {
  description = "CIDRs allowed to reach the ALB on 80/443"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "enable_http" {
  description = "Expose port 80. When a cert is set, port 80 redirects to 443."
  type        = bool
  default     = true
}

variable "certificate_arn" {
  description = "ACM certificate ARN. Empty string disables HTTPS (HTTP-only setup)."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
