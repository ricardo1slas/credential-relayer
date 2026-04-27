module "vpc" {
  source = "../../modules/vpc"

  name               = var.name
  cidr_block         = var.vpc_cidr
  availability_zones = var.availability_zones
  single_nat_gateway = true # cost saver for dev
}

module "alb" {
  source = "../../modules/alb"

  name              = var.name
  vpc_id            = module.vpc.vpc_id
  public_subnet_ids = module.vpc.public_subnet_ids
  allowed_cidrs     = var.alb_allowed_cidrs
  certificate_arn   = var.certificate_arn
}

module "ecs" {
  source = "../../modules/ecs"

  name                  = var.name
  region                = var.region
  vpc_id                = module.vpc.vpc_id
  private_subnet_ids    = module.vpc.private_subnet_ids
  alb_security_group_id = module.alb.security_group_id
  target_group_arn      = module.alb.target_group_arn
  image                 = var.image
  desired_count         = var.desired_count
  task_cpu              = var.task_cpu
  task_memory           = var.task_memory

  environment = {
    RELAYER_QUEUE_REDIS_ADDR = "${module.redis.primary_endpoint}:6379"
  }
  secrets = var.secrets
}

module "redis" {
  source = "../../modules/redis"

  name                      = var.name
  vpc_id                    = module.vpc.vpc_id
  private_subnet_ids        = module.vpc.private_subnet_ids
  client_security_group_ids = [module.ecs.task_security_group_id]
  num_cache_clusters        = 1
  apply_immediately         = true
}

module "ecr" {
  source = "../../modules/ecr"

  name         = var.name # repo name == env prefix (e.g. "relayer-dev")
  force_delete = true     # dev only: lets `terraform destroy` clean up images
}

module "github_oidc" {
  source = "../../modules/github_oidc"

  github_owner = var.github_owner
  github_repo  = var.github_repo
  allowed_refs = ["ref:refs/heads/main"]
  role_name    = "${var.name}-github-deploy"

  ecr_repository_arn = module.ecr.repository_arn
  ecs_cluster_arn    = module.ecs.cluster_arn
  ecs_service_arn    = "arn:aws:ecs:${var.region}:${data.aws_caller_identity.current.account_id}:service/${module.ecs.cluster_name}/${module.ecs.service_name}"
  passable_role_arns = [module.ecs.task_role_arn, module.ecs.execution_role_arn]
}

data "aws_caller_identity" "current" {}
