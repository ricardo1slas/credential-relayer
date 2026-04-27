terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.42"
    }
  }

  # Remote state backend. Configure via `terraform init -backend-config=...`
  # before first apply.
  backend "s3" {}
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "private-relayer"
      Environment = "prod"
      ManagedBy   = "terraform"
    }
  }
}
