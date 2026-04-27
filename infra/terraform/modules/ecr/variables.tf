variable "name" {
  description = "ECR repository name"
  type        = string
}

variable "keep_tagged_count" {
  description = "Number of most-recent tagged images to keep"
  type        = number
  default     = 10
}

variable "untagged_expiry_days" {
  description = "Days after which untagged images are expired"
  type        = number
  default     = 7
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
